package taskcontrol

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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

// Compute builds the Overview model by querying every available public task
// type. Public type filters are disjoint even when several types share a source.
func (ob *OverviewBuilder) Compute(ctx context.Context) (*Overview, error) {
	o := &Overview{TypeCounts: make(OverviewTypeCounts)}
	types := ob.availableTypes()

	for _, status := range AllNormalizedStatuses {
		var total int64
		for _, taskType := range types {
			count, err := ob.qs.Total(ctx, QueryFilter{TaskType: taskType, Status: string(status), Removed: "exclude"})
			if err != nil {
				return nil, fmt.Errorf("overview count %s/%s: %w", status, taskType, err)
			}
			total += count
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

	for _, taskType := range types {
		total, err := ob.qs.Total(ctx, QueryFilter{TaskType: taskType, Removed: "exclude"})
		if err != nil {
			return nil, fmt.Errorf("overview type count %s: %w", taskType, err)
		}
		o.TypeCounts[taskType] = total
	}

	var err error
	o.Running.Items, err = ob.sectionItems(ctx, types, StatusRunning, false)
	if err != nil {
		return nil, fmt.Errorf("overview running: %w", err)
	}
	o.Running.Label = "running"
	o.Oldest.Items, err = ob.sectionItems(ctx, types, StatusWaiting, true)
	if err != nil {
		return nil, fmt.Errorf("overview oldest: %w", err)
	}
	o.Oldest.Label = "oldest"
	o.Blocked.Items, err = ob.sectionItems(ctx, types, StatusFailed, false)
	if err != nil {
		return nil, fmt.Errorf("overview blocked: %w", err)
	}
	o.Blocked.Label = "blocked"
	o.Expired.Items, err = ob.sectionItems(ctx, types, StatusCancelled, false)
	if err != nil {
		return nil, fmt.Errorf("overview expired: %w", err)
	}
	o.Expired.Label = "expired"
	o.NoWorker = OverviewSection{Label: "no_worker"}
	o.Recovery = OverviewSection{Label: "recovery"}

	// Tombstones are currently authoritative only in the orchestration source.
	cleanupRes, err := ob.qs.List(ctx, QueryFilter{Removed: "only"}, "", 5)
	if err != nil {
		return nil, fmt.Errorf("overview cleanup: %w", err)
	}
	o.Cleanup = OverviewSection{Label: "cleanup", Items: cleanupRes.Items}

	tx, err := ob.builder.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("overview snapshot tx: %w", err)
	}
	snapRev, err := ob.builder.snapshotRevision(ctx, tx)
	_ = tx.Rollback()
	if err != nil {
		return nil, fmt.Errorf("overview snapshot revision: %w", err)
	}
	o.SnapshotRev = snapRev
	return o, nil
}

func (ob *OverviewBuilder) availableTypes() []string {
	reg := ob.builder.Registry()
	if reg == nil {
		return nil
	}
	var types []string
	for _, group := range reg.Groups {
		for _, spec := range group.Types {
			if spec.Available {
				types = append(types, spec.Type)
			}
		}
	}
	return types
}

func (ob *OverviewBuilder) sectionItems(ctx context.Context, types []string, status NormalizedStatus, oldestFirst bool) ([]ProjectionRow, error) {
	byID := make(map[string]ProjectionRow)
	for _, taskType := range types {
		filter := QueryFilter{TaskType: taskType, Status: string(status), Removed: "exclude"}
		for cursor := ""; ; {
			result, err := ob.qs.List(ctx, filter, cursor, 200)
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", status, taskType, err)
			}
			for _, item := range result.Items {
				byID[item.TaskID] = item
			}
			if !result.HasMore || result.NextCursor == "" {
				break
			}
			cursor = result.NextCursor
		}
	}
	items := make([]ProjectionRow, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if oldestFirst {
			iAt, jAt := items[i].CreatedAt, items[j].CreatedAt
			if items[i].AvailableAt != nil {
				iAt = *items[i].AvailableAt
			}
			if items[j].AvailableAt != nil {
				jAt = *items[j].AvailableAt
			}
			if !iAt.Equal(jAt) {
				return iAt.Before(jAt)
			}
		} else if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		} else if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].TaskID < items[j].TaskID
	})
	if len(items) > 5 {
		items = items[:5]
	}
	return items, nil
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
			run_now_expires
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
			TaskID:            BuildIdentity("orchestration", r.SourceID),
			SourceKind:        "orchestration",
			SourceID:          r.SourceID,
			TaskType:          r.TaskType,
			NormalizedStatus:  normalizeStatus(r.RawStatus, false),
			RawStatus:         r.RawStatus,
			Generation:        r.Generation,
			RetryRound:        r.RetryRound,
			Attempt:           r.Attempt,
			MaxAttempts:       r.MaxAttempts,
			BasePriority:      r.BasePriority,
			EffectivePriority: r.BasePriority,
			CreatedAt:         r.CreatedAt,
			UpdatedAt:         r.UpdatedAt,
			TerminalReason:    r.TerminalReason,
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
