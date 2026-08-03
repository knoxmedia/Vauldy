package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// SyncReservation is a synchronous-stage reservation that holds scheduler
// tokens for the duration of a blocking operation (e.g. scan metadata probe,
// poster generation, scrape request, prepare external process, retirement
// disk I/O).
type SyncReservation struct {
	ExecutionID string
	TaskType    string
	LeaseUntil  time.Time
}

// ErrSyncBlocked is returned when a synchronous admission cannot proceed
// because one or more budgets are exhausted.
type ErrSyncBlocked struct {
	ExecutionID string
	TaskType    string
	Blocker     string
}

func (e ErrSyncBlocked) Error() string {
	return fmt.Sprintf("sync admission blocked for %q (%s): %s", e.TaskType, e.ExecutionID, e.Blocker)
}

// ErrSyncLeaseLost is returned when a heartbeat or release finds the
// reservation missing or already released.
var ErrSyncLeaseLost = errors.New("sync lease lost")

// SyncAdmissionRequest carries all inputs to acquire a synchronous scheduler
// token before a blocking stage begins.
type SyncAdmissionRequest struct {
	Owner       string
	TaskType    string
	Stage       string // descriptive label: "metadata", "poster", "scrape", "prepare", "retirement"
	LibraryID   int64
	SourceClass string
	Metadata    map[string]string
}

// syncAdmissionMu serializes AcquireSync calls so that the budget check
// and reservation insertion are atomic at the process level. Without this
// mutex concurrent callers can all pass the budget check before any
// reservation is committed, defeating the concurrency limit.
var syncAdmissionMu sync.Mutex

// syncTypeCounters tracks the number of active reservations per task type
// within this process. Together with syncAdmissionMu it ensures that
// concurrency limits are enforced without relying on SQLite snapshot
// consistency across connections.
var syncTypeCounters = make(map[string]int)

// syncResourceUsage tracks current resource consumption across all
// synchronous reservations, keyed by resource kind.
var syncResourceUsage = make(map[ResourceKind]int)

// syncReservationProfiles records the resource profile of each active
// synchronous reservation so that ReleaseSync can correctly adjust
// syncResourceUsage.
var syncReservationProfiles = make(map[string]ResourceRequest) // executionID -> profile

// syncReservationTypes records the task type of each active reservation
// so that ReleaseSync can correctly adjust syncTypeCounters without
// requiring a DB lookup.
var syncReservationTypes = make(map[string]string) // executionID -> taskType

// ResetSyncCounters resets all in-process admission counters to zero.
// Callers should use this only in test teardown; production code must
// never call it, as it would allow over-admission of running work.
func ResetSyncCounters() {
	syncAdmissionMu.Lock()
	defer syncAdmissionMu.Unlock()
	for k := range syncTypeCounters {
		delete(syncTypeCounters, k)
	}
	for k := range syncResourceUsage {
		delete(syncResourceUsage, k)
	}
	for k := range syncReservationProfiles {
		delete(syncReservationProfiles, k)
	}
	for k := range syncReservationTypes {
		delete(syncReservationTypes, k)
	}
}

// AcquireSync attempts to admit a synchronous reservation under the current
// policy. It checks control state, type concurrency, and all resource budgets,
// then creates a durable reservation if all checks pass.
//
// Admission is serialized per-process via syncAdmissionMu to ensure the
// budget check and reservation insertion are atomic.
//
// Lease until is computed as now + leaseTTL. The returned SyncReservation
// carries an ExecutionID that the caller must use for heartbeat and release.
func AcquireSync(ctx context.Context, db *sql.DB, req SyncAdmissionRequest, policy Policy, leaseTTL time.Duration) (*SyncReservation, error) {
	if strings.TrimSpace(req.Owner) == "" {
		return nil, errors.New("sync admission: owner is required")
	}
	if strings.TrimSpace(req.TaskType) == "" {
		return nil, errors.New("sync admission: task type is required")
	}
	if leaseTTL <= 0 {
		leaseTTL = 90 * time.Second
	}

	syncAdmissionMu.Lock()
	defer syncAdmissionMu.Unlock()

	executionID := GenerateExecutionID(strings.TrimSpace(req.Owner))

	// Check type concurrency against the in-memory counter.
	maxType := 0
	if v, ok := policy.TypeConcurrency[req.TaskType]; ok {
		maxType = v
	}
	if maxType > 0 {
		if current := syncTypeCounters[req.TaskType]; current >= maxType {
			return nil, ErrSyncBlocked{
				ExecutionID: executionID,
				TaskType:    req.TaskType,
				Blocker:     fmt.Sprintf("type concurrency at limit (%d/%d)", current, maxType),
			}
		}
	}

	// Determine the resource profile for this task type.
	profile := resourceProfileForSync(req.TaskType)

	// Check resource budgets against the in-memory counter.
	for rk, reqUnits := range profile {
		current := syncResourceUsage[rk]
		max := 0
		if v, ok := policy.ResourceCapacity[rk]; ok {
			max = v
		}
		if max > 0 && current+reqUnits > max {
			return nil, ErrSyncBlocked{
				ExecutionID: executionID,
				TaskType:    req.TaskType,
				Blocker:     fmt.Sprintf("resource %s at limit (%d+%d > %d)", rk, current, reqUnits, max),
			}
		}
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("sync admission: get conn: %w", err)
	}
	// conn is closed below only after the counter is updated.

	now := time.Now()

	if blockers, err := CheckAllBudgets(ctx, conn, req.TaskType, policy, now); err != nil {
		conn.Close()
		return nil, err
	} else if len(blockers) > 0 {
		conn.Close()
		reasons := make([]string, len(blockers))
		for i, b := range blockers {
			reasons[i] = b.Reason
		}
		return nil, ErrSyncBlocked{ExecutionID: executionID, TaskType: req.TaskType, Blocker: strings.Join(reasons, "; ")}
	}

	var revID sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT id FROM scheduler_policy_revision WHERE is_active=1`).Scan(&revID); err != nil {
		conn.Close()
		return nil, fmt.Errorf("sync admission: no active policy revision: %w", err)
	}

	leaseUntil := now.Add(leaseTTL)
	if _, err := InsertAdmissionReservation(ctx, conn, executionID, req.TaskType, 1, revID.Int64, leaseUntil); err != nil {
		conn.Close()
		return nil, err
	}
	conn.Close()

	// Now that the reservation is durable, update in-memory counters.
	syncTypeCounters[req.TaskType]++
	for rk, reqUnits := range profile {
		syncResourceUsage[rk] += reqUnits
	}
	syncReservationProfiles[executionID] = profile
	syncReservationTypes[executionID] = req.TaskType

	return &SyncReservation{
		ExecutionID: executionID,
		TaskType:    req.TaskType,
		LeaseUntil:  leaseUntil,
	}, nil
}

// resourceProfileForSync returns the resource requirements for a task type.
// It looks up the descriptor from the Registry and returns a copy of its
// resource profile so the caller can modify it without affecting the registry.
func resourceProfileForSync(taskType string) ResourceRequest {
	if desc, ok := Registry[taskType]; ok {
		cp := make(ResourceRequest, len(desc.Resources))
		for k, v := range desc.Resources {
			cp[k] = v
		}
		return cp
	}
	return ResourceRequest{}
}

// ReleaseSync releases a synchronous reservation, freeing its tokens for
// other callers. It is idempotent: when the reservation is already released
// or absent the call is a no-op that returns nil.
func ReleaseSync(ctx context.Context, db *sql.DB, executionID, reason, releasedBy string) error {
	if strings.TrimSpace(executionID) == "" {
		return errors.New("sync release: execution id is required")
	}
	err := ReleaseReservationTx(ctx, db, executionID, reason, releasedBy)
	if errors.Is(err, ErrReservationNotActive) {
		return nil
	}
	if err != nil {
		return err
	}
	// Decrement in-memory counters.
	syncAdmissionMu.Lock()
	if taskType, ok := syncReservationTypes[executionID]; ok {
		if syncTypeCounters[taskType] > 0 {
			syncTypeCounters[taskType]--
		}
		delete(syncReservationTypes, executionID)
	}
	if profile, ok := syncReservationProfiles[executionID]; ok {
		for rk, reqUnits := range profile {
			if syncResourceUsage[rk] >= reqUnits {
				syncResourceUsage[rk] -= reqUnits
			}
		}
		delete(syncReservationProfiles, executionID)
	}
	syncAdmissionMu.Unlock()
	return nil
}

// HeartbeatSync extends the lease of a synchronous reservation. Returns
// ErrSyncLeaseLost when the reservation is no longer active or owned.
func HeartbeatSync(ctx context.Context, db *sql.DB, executionID string, leaseTTL time.Duration) error {
	if strings.TrimSpace(executionID) == "" {
		return errors.New("sync heartbeat: execution id is required")
	}
	if leaseTTL <= 0 {
		leaseTTL = 90 * time.Second
	}
	modifier := fmt.Sprintf("+%d seconds", int64(leaseTTL/time.Second))
	result, err := db.ExecContext(ctx,
		`UPDATE scheduler_reservation SET lease_until=datetime(CURRENT_TIMESTAMP, ?), updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='active'`,
		modifier, executionID)
	if err != nil {
		return fmt.Errorf("sync heartbeat: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sync heartbeat rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrSyncLeaseLost, executionID)
	}
	return nil
}
