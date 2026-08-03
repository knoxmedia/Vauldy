package taskcontrol

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// OverviewStatusCounts groups task counts by normalized status.
type OverviewStatusCounts struct {
	Waiting   int64 `json:"waiting"`
	Running   int64 `json:"running"`
	Done      int64 `json:"done"`
	Failed    int64 `json:"failed"`
	Cancelled int64 `json:"cancelled"`
	Skipped   int64 `json:"skipped"`
}

// Total returns the sum of all status counts.
func (c OverviewStatusCounts) Total() int64 {
	return c.Waiting + c.Running + c.Done + c.Failed + c.Cancelled + c.Skipped
}

// OverviewTypeCounts groups task counts by task type.
type OverviewTypeCounts map[string]int64

// OverviewSection lists a subset of canonical projected rows for a
// dashboard section (e.g., oldest, blocked, cleanup).
type OverviewSection struct {
	Label string          `json:"label"`
	Items []ProjectionRow `json:"items"`
}

// ResourceBudget reports scheduler capacity usage for a resource kind.
type ResourceBudget struct {
	Kind  string `json:"kind"`
	Used  int    `json:"used"`
	Limit int    `json:"limit"`
}

// RecentOperation is a summary of a recent control audit or batch operation.
type RecentOperation struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	TaskType  string    `json:"task_type,omitempty"`
	ActorName string    `json:"actor_name"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Overview is the server-computed dashboard model derived from projection
// truth and scheduler snapshots.
type Overview struct {
	StatusCounts    OverviewStatusCounts `json:"status_counts"`
	TypeCounts      OverviewTypeCounts   `json:"type_counts"`
	Running         OverviewSection      `json:"running"`
	Oldest          OverviewSection      `json:"oldest"`
	Blocked         OverviewSection      `json:"blocked"`
	NoWorker        OverviewSection      `json:"no_worker"`
	Expired         OverviewSection      `json:"expired"`
	Recovery        OverviewSection      `json:"recovery"`
	Cleanup         OverviewSection      `json:"cleanup"`
	ResourceBudgets []ResourceBudget     `json:"resource_budgets,omitempty"`
	RecentOps       []RecentOperation    `json:"recent_ops,omitempty"`
	SnapshotRev     int64                `json:"snapshot_revision"`
}

// OverviewBuilder computes the Overview from the projection builder and
// query service, with optional Phase 3 scheduler budget snapshots and
// recent audit operations.
type OverviewBuilder struct {
	builder *ProjectionBuilder
	qs      *QueryService
}

// NewOverviewBuilder creates an overview builder.
func NewOverviewBuilder(builder *ProjectionBuilder) *OverviewBuilder {
	return &OverviewBuilder{
		builder: builder,
		qs:      NewQueryService(builder),
	}
}

// Compute builds the Overview model by querying counts and sections from
// the projection sources.
func (ob *OverviewBuilder) Compute(ctx context.Context) (*Overview, error) {
	o := &Overview{TypeCounts: make(OverviewTypeCounts)}

	// Compute status counts via count queries per status
	for _, status := range AllNormalizedStatuses {
		filter := QueryFilter{
			Status:  string(status),
			Removed: "exclude",
		}
		total, err := ob.qs.Total(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("overview count %s: %w", status, err)
		}
		switch status {
		case StatusWaiting:
			o.StatusCounts.Waiting = total
		case StatusRunning:
			o.StatusCounts.Running = total
		case StatusDone:
			o.StatusCounts.Done = total
		case StatusFailed:
			o.StatusCounts.Failed = total
		case StatusCancelled:
			o.StatusCounts.Cancelled = total
		case StatusSkipped:
			o.StatusCounts.Skipped = total
		}
	}

	// Compute type counts by querying per type in the registry
	if reg := ob.builder.Registry(); reg != nil {
		for _, g := range reg.Groups {
			for _, spec := range g.Types {
				if !spec.Available {
					continue
				}
				filter := QueryFilter{
					TaskType: spec.Type,
					Removed:  "exclude",
				}
				total, err := ob.qs.Total(ctx, filter)
				if err != nil {
					return nil, fmt.Errorf("overview type count %s: %w", spec.Type, err)
				}
				o.TypeCounts[spec.Type] = total
			}
		}
	}

	// Running section: most recently started running tasks
	runningRes, err := ob.qs.List(ctx, QueryFilter{Status: "running", Removed: "exclude"}, "", 5)
	if err != nil {
		return nil, fmt.Errorf("overview running: %w", err)
	}
	o.Running = OverviewSection{Label: "running", Items: runningRes.Items}

	// Oldest section: oldest waiting tasks by available_at
	oldestRes, err := ob.qs.List(ctx, QueryFilter{Status: "waiting", Removed: "exclude"}, "", 5)
	if err != nil {
		return nil, fmt.Errorf("overview oldest: %w", err)
	}
	o.Oldest = OverviewSection{Label: "oldest", Items: oldestRes.Items}

	// Blocked section: failed tasks (terminal with errors)
	blockedRes, err := ob.qs.List(ctx, QueryFilter{Status: "failed", Removed: "exclude"}, "", 5)
	if err != nil {
		return nil, fmt.Errorf("overview blocked: %w", err)
	}
	o.Blocked = OverviewSection{Label: "blocked", Items: blockedRes.Items}

	// No-worker section: tasks of types without capable workers
	// (in Phase 4, we approximate with types having 0 count)
	o.NoWorker = OverviewSection{Label: "no_worker"}

	// Expired section: cancelled tasks
	expiredRes, err := ob.qs.List(ctx, QueryFilter{Status: "cancelled", Removed: "exclude"}, "", 5)
	if err != nil {
		return nil, fmt.Errorf("overview expired: %w", err)
	}
	o.Expired = OverviewSection{Label: "expired", Items: expiredRes.Items}

	// Recovery section: tasks that are in retry rounds
	// (approximation: list failed tasks with retry_round > 0)
	o.Recovery = OverviewSection{Label: "recovery"}

	// Cleanup section: removed-only tasks
	cleanupRes, err := ob.qs.List(ctx, QueryFilter{Removed: "only"}, "", 5)
	if err != nil {
		return nil, fmt.Errorf("overview cleanup: %w", err)
	}
	o.Cleanup = OverviewSection{Label: "cleanup", Items: cleanupRes.Items}

	// Snapshot revision
	tx, err := ob.builder.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("overview snapshot tx: %w", err)
	}
	snapRev, err := ob.builder.snapshotRevision(ctx, tx)
	tx.Rollback()
	if err != nil {
		return nil, fmt.Errorf("overview snapshot revision: %w", err)
	}
	o.SnapshotRev = snapRev

	return o, nil
}

// SetResourceBudgets populates resource budget information from a
// Phase 3 scheduler BudgetSnapshot equivalent map.
func (o *Overview) SetResourceBudgets(budgets []ResourceBudget) {
	o.ResourceBudgets = budgets
}

// SetRecentOps populates recent operations from audit/batch tables.
func (o *Overview) SetRecentOps(ops []RecentOperation) {
	o.RecentOps = ops
}

// ValidateTotalParity checks that status count total equals type count total
// when both are computed from the same projection snapshot.
func (o *Overview) ValidateTotalParity() error {
	statusTotal := o.StatusCounts.Total()
	var typeTotal int64
	for _, c := range o.TypeCounts {
		typeTotal += c
	}
	if statusTotal != typeTotal {
		return fmt.Errorf("status total (%d) != type total (%d)", statusTotal, typeTotal)
	}
	return nil
}

// computeRecoveryTasks queries tasks in retry rounds (approximation).
func computeRecoveryTasks(ctx context.Context, db *sql.DB) ([]ProjectionRow, error) {
	// Tasks that are in a retry round > 0 and not yet done
	rows, err := db.QueryContext(ctx,
		`SELECT id, task_type, status, COALESCE(attempts,0), COALESCE(max_attempts,3),
			COALESCE(generation,1), COALESCE(retry_round,0),
			COALESCE(lease_owner,''), lease_until, COALESCE(last_error,''),
			COALESCE(base_priority,0), available_at, created_at, updated_at,
			media_id, library_id,
			removed_at, COALESCE(removed_by,''), COALESCE(remove_reason,''),
			COALESCE(run_now_expires, NULL)
		FROM post_ingest_task WHERE retry_round > 0 AND status = 'failed'
		ORDER BY created_at DESC LIMIT 5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProjectionRows(rows)
}

func scanProjectionRows(rows *sql.Rows) ([]ProjectionRow, error) {
	var out []ProjectionRow
	for rows.Next() {
		r := RawTaskRow{SourceKind: "orchestration"}
		var typ string
		var leaseUntil sql.NullTime
		var availableAt sql.NullTime
		var runNowExpires sql.NullTime
		var mediaID sql.NullInt64
		var libraryID sql.NullInt64
		var removedAt sql.NullTime
		if err := rows.Scan(&r.SourceID, &typ, &r.RawStatus, &r.Attempt, &r.MaxAttempts,
			&r.Generation, &r.RetryRound, &r.Owner, &leaseUntil, &r.TerminalReason,
			&r.BasePriority, &availableAt, &r.CreatedAt, &r.UpdatedAt,
			&mediaID, &libraryID,
			&removedAt, &r.RemovedBy, &r.RemoveReason,
			&runNowExpires); err != nil {
			return nil, err
		}
		r.TaskType = typ
		if leaseUntil.Valid {
			r.LeaseUntil = &leaseUntil.Time
		}
		if availableAt.Valid {
			r.AvailableAt = &availableAt.Time
		}
		if runNowExpires.Valid {
			r.RunNowExpires = &runNowExpires.Time
		}
		if mediaID.Valid {
			r.MediaID = &mediaID.Int64
		}
		if libraryID.Valid {
			r.LibraryID = &libraryID.Int64
		}
		if removedAt.Valid {
			r.RemovedAt = &removedAt.Time
		}
		row := ProjectionRow{
			TaskID:           BuildIdentity("orchestration", r.SourceID),
			SourceKind:       "orchestration",
			SourceID:         r.SourceID,
			TaskType:         r.TaskType,
			NormalizedStatus: normalizeStatus(r.RawStatus, false),
			RawStatus:        r.RawStatus,
			Generation:       r.Generation,
			RetryRound:       r.RetryRound,
			Attempt:          r.Attempt,
			MaxAttempts:      r.MaxAttempts,
			BasePriority:     r.BasePriority,
			EffectivePriority: r.BasePriority,
			CreatedAt:        r.CreatedAt,
			UpdatedAt:        r.UpdatedAt,
			TerminalReason:   r.TerminalReason,
		}
		if mediaID.Valid {
			row.MediaID = &mediaID.Int64
		}
		if libraryID.Valid {
			row.LibraryID = &libraryID.Int64
		}
		if availableAt.Valid {
			row.AvailableAt = &availableAt.Time
		}
		if removedAt.Valid {
			row.RemovedAt = &removedAt.Time
		}
		if r.Owner != "" {
			row.OwnerLease = &OwnerLeaseInfo{Owner: r.Owner}
			if leaseUntil.Valid {
				row.OwnerLease.LeaseUntil = &leaseUntil.Time
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
