package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"knox-media/internal/coreiface"
	"knox-media/internal/store"
)

// AdmissionRequest carries all inputs for an atomic claim admission attempt.
type AdmissionRequest struct {
	Family    string
	TaskType  string
	TaskTypes []string
	Owner     string
	Registry  coreiface.CapabilityRegistry
	Metrics   *store.SQLiteMetrics
	// afterCommit is called after COMMIT succeeds; used for uncertainty reconciliation.
	afterCommit func() error
}

// AdmissionResult is the output of a successful admission claim.
type AdmissionResult struct {
	QueueID       int64
	TaskType      string
	Owner         string
	MediaID       int64
	ReservationID int64
	ExecutionID   string
	LeaseUntil    string
}

// AdmissionBlocker describes a reason why a candidate could not be admitted.
type AdmissionBlocker struct {
	Reason   string
	TaskType string
}

// AdmissionBlockers is a collection of admission blockers returned when
// candidates exist but none fit.
type AdmissionBlockers []AdmissionBlocker

// ActiveReservationCount returns the count of unreleased non-expired
// reservations for a task type visible through the given executor.
func ActiveReservationCount(ctx context.Context, tx store.SQLExecutor, taskType string, now time.Time) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scheduler_reservation WHERE task_type=? AND status='active' AND (lease_until IS NULL OR lease_until > ?)`,
		taskType, now).Scan(&count)
	return count, err
}

// ActiveResourceUsage returns the total resource usage across all active
// reservations visible through tx, summed by resource kind.
func ActiveResourceUsage(ctx context.Context, tx store.SQLExecutor, now time.Time) (map[ResourceKind]int, error) {
	usage := make(map[ResourceKind]int)
	rows, err := tx.QueryContext(ctx,
		`SELECT task_type, reserved_units FROM scheduler_reservation WHERE status='active' AND (lease_until IS NULL OR lease_until > ?)`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var taskType string
		var units int
		if err := rows.Scan(&taskType, &units); err != nil {
			return nil, err
		}
		desc, ok := Registry[taskType]
		if !ok {
			continue
		}
		for rk, count := range desc.Resources {
			usage[rk] += count * units
		}
	}
	return usage, rows.Err()
}

// ActiveProviderUsage returns the total provider usage across all active
// reservations visible through tx.
func ActiveProviderUsage(ctx context.Context, tx store.SQLExecutor, now time.Time) (map[string]int, error) {
	usage := make(map[string]int)
	rows, err := tx.QueryContext(ctx,
		`SELECT task_type, reserved_units FROM scheduler_reservation WHERE status='active' AND (lease_until IS NULL OR lease_until > ?)`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var taskType string
		var units int
		if err := rows.Scan(&taskType, &units); err != nil {
			return nil, err
		}
		desc, ok := Registry[taskType]
		if !ok {
			continue
		}
		if desc.Provider != "" {
			usage[desc.Provider] += units
		}
	}
	return usage, rows.Err()
}

// CheckControlState returns whether a task type is blocked by its control state.
// Returns ("", nil) if running, or ("paused"/"draining", blocker) if blocked.
func CheckControlState(ctx context.Context, tx store.SQLExecutor, taskType string) (string, *AdmissionBlocker, error) {
	var state string
	err := tx.QueryRowContext(ctx,
		`SELECT state FROM scheduler_control WHERE task_type=?`, taskType).Scan(&state)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil, nil
		}
		return "", nil, err
	}
	switch state {
	case "paused":
		return state, &AdmissionBlocker{
			Reason:   fmt.Sprintf("type %q is paused", taskType),
			TaskType: taskType,
		}, nil
	case "draining":
		return state, &AdmissionBlocker{
			Reason:   fmt.Sprintf("type %q is draining", taskType),
			TaskType: taskType,
		}, nil
	case "running":
		return state, nil, nil
	default:
		return state, &AdmissionBlocker{
			Reason:   fmt.Sprintf("type %q has unknown control state %q", taskType, state),
			TaskType: taskType,
		}, nil
	}
}

// CheckTypeConcurrency checks if admitting another task of taskType would
// exceed the type concurrency limit. Returns a blocker if blocked.
func CheckTypeConcurrency(ctx context.Context, tx store.SQLExecutor, taskType string, policy Policy, now time.Time) (*AdmissionBlocker, error) {
	limit, ok := policy.TypeConcurrency[taskType]
	if !ok {
		limit = 0
	}
	if limit <= 0 {
		return &AdmissionBlocker{
			Reason:   fmt.Sprintf("type %q has zero or no concurrency limit (%d)", taskType, limit),
			TaskType: taskType,
		}, nil
	}
	count, err := ActiveReservationCount(ctx, tx, taskType, now)
	if err != nil {
		return nil, err
	}
	if count >= limit {
		return &AdmissionBlocker{
			Reason:   fmt.Sprintf("type %q at concurrency limit (%d/%d)", taskType, count, limit),
			TaskType: taskType,
		}, nil
	}
	return nil, nil
}

// CheckResourceBudget checks if admitting taskType would exceed any resource budget.
func CheckResourceBudget(ctx context.Context, tx store.SQLExecutor, taskType string, policy Policy, now time.Time) (*AdmissionBlocker, error) {
	desc, ok := Registry[taskType]
	if !ok {
		return nil, nil
	}
	usage, err := ActiveResourceUsage(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	for rk, requested := range desc.Resources {
		capacity, hasCap := policy.ResourceCapacity[rk]
		if !hasCap || capacity <= 0 {
			continue
		}
		used := usage[rk]
		if used+requested > capacity {
			return &AdmissionBlocker{
				Reason:   fmt.Sprintf("resource %q budget exceeded: used %d + requested %d > capacity %d", rk, used, requested, capacity),
				TaskType: taskType,
			}, nil
		}
	}
	return nil, nil
}

// CheckProviderBudget checks if admitting taskType would exceed the provider limit.
func CheckProviderBudget(ctx context.Context, tx store.SQLExecutor, taskType string, policy Policy, now time.Time) (*AdmissionBlocker, error) {
	desc, ok := Registry[taskType]
	if !ok || desc.Provider == "" {
		return nil, nil
	}
	limit, hasLimit := policy.ProviderCapacity[desc.Provider]
	if !hasLimit || limit <= 0 {
		return nil, nil
	}
	usage, err := ActiveProviderUsage(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	used := usage[desc.Provider]
	if used+1 > limit {
		return &AdmissionBlocker{
			Reason:   fmt.Sprintf("provider %q at capacity: used %d + 1 > limit %d", desc.Provider, used, limit),
			TaskType: taskType,
		}, nil
	}
	return nil, nil
}

// CheckAllBudgets runs all budget checks and returns all blockers found.
func CheckAllBudgets(ctx context.Context, tx store.SQLExecutor, taskType string, policy Policy, now time.Time) (AdmissionBlockers, error) {
	var blockers AdmissionBlockers

	// Check control state first
	_, cb, err := CheckControlState(ctx, tx, taskType)
	if err != nil {
		return nil, err
	}
	if cb != nil {
		blockers = append(blockers, *cb)
	}

	// Check type concurrency
	tc, err := CheckTypeConcurrency(ctx, tx, taskType, policy, now)
	if err != nil {
		return nil, err
	}
	if tc != nil {
		blockers = append(blockers, *tc)
	}

	// Check resource budget
	rb, err := CheckResourceBudget(ctx, tx, taskType, policy, now)
	if err != nil {
		return nil, err
	}
	if rb != nil {
		blockers = append(blockers, *rb)
	}

	// Check provider budget
	pb, err := CheckProviderBudget(ctx, tx, taskType, policy, now)
	if err != nil {
		return nil, err
	}
	if pb != nil {
		blockers = append(blockers, *pb)
	}

	if len(blockers) == 0 {
		return nil, nil
	}
	return blockers, nil
}

// InsertAdmissionReservation creates a reservation in the given transaction.
func InsertAdmissionReservation(ctx context.Context, tx store.SQLExecutor, executionID, taskType string, reservedUnits int, policyRevisionID int64, leaseUntil time.Time) (int64, error) {
	result, err := tx.ExecContext(ctx,
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,?,?,?,'active',?)`,
		executionID, taskType, reservedUnits, policyRevisionID, leaseUntil)
	if err != nil {
		return 0, fmt.Errorf("insert reservation: %w", err)
	}
	return result.LastInsertId()
}

// GenerateExecutionID creates a unique execution ID for a reservation.
func GenerateExecutionID(owner string) string {
	return strings.TrimSpace(owner) + "/" + uuid.NewString()
}
