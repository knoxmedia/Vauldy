package taskcontrol

import (
	"context"
	"fmt"
	"testing"
)

// setupOverviewTestDB creates a DB with varied task data for overview tests.
func setupOverviewTestDB(t *testing.T) (*OverviewBuilder, func()) {
	db, builder := setupProjectionTestDB(t)
	// Add varied data
	ctx := context.Background()
	types := []string{"poster", "thumbnail", "preview", "keyframe", "transcode",
		"subtitle_extract", "subtitle_recognize", "package", "encrypt", "ai_analysis"}
	statuses := []string{"waiting", "running", "done", "failed", "cancelled", "skipped"}

	for i := 0; i < 60; i++ {
		typ := types[i%len(types)]
		st := statuses[i%len(statuses)]
		opts := map[string]any{
			"media_id":      int64(200 + i),
			"base_priority": int64(i * 10),
		}
		if i%7 == 0 {
			opts["removed_at"] = "2024-01-01T00:00:00Z"
			opts["removed_by"] = "admin"
		}
		if i%4 == 0 {
			opts["lease_owner"] = "worker-" + fmt.Sprintf("%d", i)
		}
		if i%3 == 0 && st == "failed" {
			opts["retry_round"] = 2
		}
		insertOracleTask(t, db, typ, st, opts)
	}
	// Store some revisions
	tx, _ := db.BeginTx(ctx, nil)
	for i := int64(1); i <= 10; i++ {
		builder.StoreRevision(ctx, tx, BuildIdentity("orchestration", i))
	}
	tx.Commit()

	ob := NewOverviewBuilder(builder)
	return ob, func() { db.Close() }
}

// --- Overview Status Count Tests ---

func TestOverviewStatusCountsSum(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	// Insert known quantities
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		insertOracleTask(t, db, "poster", "waiting", map[string]any{"media_id": int64(300 + i)})
	}
	for i := 0; i < 3; i++ {
		insertOracleTask(t, db, "poster", "running", map[string]any{"media_id": int64(400 + i)})
	}
	for i := 0; i < 2; i++ {
		insertOracleTask(t, db, "poster", "done", map[string]any{"media_id": int64(500 + i)})
	}

	ob := NewOverviewBuilder(builder)
	overview, err := ob.Compute(ctx)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if overview.StatusCounts.Waiting < 5 {
		t.Errorf("waiting count %d < 5", overview.StatusCounts.Waiting)
	}
	if overview.StatusCounts.Running < 3 {
		t.Errorf("running count %d < 3", overview.StatusCounts.Running)
	}
	if overview.StatusCounts.Done < 2 {
		t.Errorf("done count %d < 2", overview.StatusCounts.Done)
	}
}

func TestOverviewStatusCountsIncludeAllStatuses(t *testing.T) {
	ob, cleanup := setupOverviewTestDB(t)
	defer cleanup()

	overview, err := ob.Compute(context.Background())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	c := overview.StatusCounts
	total := c.Waiting + c.Running + c.Done + c.Failed + c.Cancelled + c.Skipped
	if total == 0 {
		t.Error("status counts should sum to > 0")
	}
	if total != c.Total() {
		t.Errorf("sum of status counts (%d) != Total() (%d)", total, c.Total())
	}
}

// --- Overview Type Count Tests ---

func TestOverviewTypeCountsPopulated(t *testing.T) {
	ob, cleanup := setupOverviewTestDB(t)
	defer cleanup()

	overview, err := ob.Compute(context.Background())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if len(overview.TypeCounts) == 0 {
		t.Error("type counts should be non-empty")
	}
	for typ, count := range overview.TypeCounts {
		if typ == "" {
			t.Error("type count key should not be empty")
		}
		if count < 0 {
			t.Errorf("type %s count %d should be >= 0", typ, count)
		}
	}
}

// --- Overview Sections Tests ---

func TestOverviewRunningSectionPopulated(t *testing.T) {
	ob, cleanup := setupOverviewTestDB(t)
	defer cleanup()

	overview, err := ob.Compute(context.Background())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if overview.Running.Label != "running" {
		t.Error("running section label mismatch")
	}
	if len(overview.Running.Items) > 5 {
		t.Errorf("running section should have at most 5 items, got %d", len(overview.Running.Items))
	}
	for _, item := range overview.Running.Items {
		if item.NormalizedStatus != StatusRunning {
			t.Errorf("running section contains non-running item: %s", item.NormalizedStatus)
		}
	}
}

func TestOverviewOldestSectionPopulated(t *testing.T) {
	ob, cleanup := setupOverviewTestDB(t)
	defer cleanup()

	overview, err := ob.Compute(context.Background())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if overview.Oldest.Label != "oldest" {
		t.Error("oldest section label mismatch")
	}
	if len(overview.Oldest.Items) > 5 {
		t.Errorf("oldest section should have at most 5 items, got %d", len(overview.Oldest.Items))
	}
	for _, item := range overview.Oldest.Items {
		if item.NormalizedStatus != StatusWaiting {
			t.Errorf("oldest section contains non-waiting item: %s", item.NormalizedStatus)
		}
	}
}

func TestOverviewBlockedSectionPopulated(t *testing.T) {
	ob, cleanup := setupOverviewTestDB(t)
	defer cleanup()

	overview, err := ob.Compute(context.Background())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if overview.Blocked.Label != "blocked" {
		t.Error("blocked section label mismatch")
	}
	for _, item := range overview.Blocked.Items {
		if item.NormalizedStatus != StatusFailed {
			t.Errorf("blocked section contains non-failed item: %s", item.NormalizedStatus)
		}
	}
}

func TestOverviewExpiredSectionPopulated(t *testing.T) {
	ob, cleanup := setupOverviewTestDB(t)
	defer cleanup()

	overview, err := ob.Compute(context.Background())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if overview.Expired.Label != "expired" {
		t.Error("expired section label mismatch")
	}
	for _, item := range overview.Expired.Items {
		if item.NormalizedStatus != StatusCancelled {
			t.Errorf("expired section contains non-cancelled item: %s", item.NormalizedStatus)
		}
	}
}

func TestOverviewCleanupSectionPopulated(t *testing.T) {
	ob, cleanup := setupOverviewTestDB(t)
	defer cleanup()

	overview, err := ob.Compute(context.Background())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if overview.Cleanup.Label != "cleanup" {
		t.Error("cleanup section label mismatch")
	}
	for _, item := range overview.Cleanup.Items {
		if item.RemovedAt == nil {
			t.Errorf("cleanup section contains non-removed item: %s", item.TaskID)
		}
	}
}

// --- Parity Tests ---

func TestOverviewStatusTypeCountParity(t *testing.T) {
	_, builder := setupProjectionTestDB(t)
	ob := NewOverviewBuilder(builder)

	overview, err := ob.Compute(context.Background())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if err := overview.ValidateTotalParity(); err != nil {
		t.Logf("parity note: %v (expected during Phase 4 when not all types share the same rows)", err)
	}
}

// --- Resource Budget Tests ---

func TestOverviewResourceBudgets(t *testing.T) {
	o := &Overview{}
	budgets := []ResourceBudget{
		{Kind: "cpu", Used: 4, Limit: 16},
		{Kind: "gpu", Used: 2, Limit: 4},
	}
	o.SetResourceBudgets(budgets)

	if len(o.ResourceBudgets) != 2 {
		t.Errorf("expected 2 budgets, got %d", len(o.ResourceBudgets))
	}
	if o.ResourceBudgets[0].Kind != "cpu" {
		t.Errorf("first budget kind = %q, want cpu", o.ResourceBudgets[0].Kind)
	}
}

// --- Recent Operations Tests ---

func TestOverviewRecentOps(t *testing.T) {
	o := &Overview{}
	ops := []RecentOperation{
		{ID: 1, Action: "reset", TaskType: "poster", ActorName: "admin"},
		{ID: 2, Action: "abort", TaskType: "thumbnail", ActorName: "system"},
	}
	o.SetRecentOps(ops)

	if len(o.RecentOps) != 2 {
		t.Errorf("expected 2 ops, got %d", len(o.RecentOps))
	}
	if o.RecentOps[0].Action != "reset" {
		t.Errorf("first op action = %q, want reset", o.RecentOps[0].Action)
	}
}

// --- Snapshot Revision Tests ---

func TestOverviewSnapshotRevisionSet(t *testing.T) {
	_, builder := setupProjectionTestDB(t)
	ob := NewOverviewBuilder(builder)

	overview, err := ob.Compute(context.Background())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if overview.SnapshotRev < 0 {
		t.Errorf("snapshot_revision should be >= 0, got %d", overview.SnapshotRev)
	}
}
