package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncAdmissionAcquireReleaseIdempotent(t *testing.T) {
	ResetSyncCounters()
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["scan"] = 1
	seedActivePolicy(t, db, policy)
	setControlState(t, db, "scan", "running")

	ctx := context.Background()
	res, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:    "scanner",
		TaskType: "scan",
		Stage:    "scan-library",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync: %v", err)
	}
	if res == nil || res.ExecutionID == "" {
		t.Fatal("expected non-empty execution id")
	}
	if !strings.HasPrefix(res.ExecutionID, "scanner/") {
		t.Fatalf("execution id prefix: %q", res.ExecutionID)
	}

	// Second acquire on same type with limit=1 should block.
	_, err = AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:    "scanner-2",
		TaskType: "scan",
		Stage:    "scan-library",
	}, policy, 30*time.Second)
	var blocked ErrSyncBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("expected ErrSyncBlocked, got %v", err)
	}
	if !strings.Contains(strings.ToLower(blocked.Blocker), "concurrency") {
		t.Fatalf("blocker reason should mention concurrency: %q", blocked.Blocker)
	}

	// Release the first reservation.
	if err := ReleaseSync(ctx, db, res.ExecutionID, "done", "scanner"); err != nil {
		t.Fatalf("ReleaseSync: %v", err)
	}

	// Release is idempotent.
	if err := ReleaseSync(ctx, db, res.ExecutionID, "done", "scanner"); err != nil {
		t.Fatalf("ReleaseSync idempotent: %v", err)
	}

	// After release, a new acquire should succeed.
	res2, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:    "scanner-3",
		TaskType: "scan",
		Stage:    "scan-library",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync after release: %v", err)
	}
	if res2 == nil {
		t.Fatal("expected reservation after release")
	}
	_ = ReleaseSync(ctx, db, res2.ExecutionID, "done", "scanner-3")
}

func TestSyncAdmissionHeartbeatExtendsLease(t *testing.T) {
	ResetSyncCounters()
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["prepare"] = 1
	seedActivePolicy(t, db, policy)
	setControlState(t, db, "prepare", "running")

	ctx := context.Background()
	res, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:    "pretranscode",
		TaskType: "prepare",
		Stage:    "prepare-transcode",
	}, policy, 5*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync: %v", err)
	}

	// Heartbeat extends the lease.
	if err := HeartbeatSync(ctx, db, res.ExecutionID, 60*time.Second); err != nil {
		t.Fatalf("HeartbeatSync: %v", err)
	}

	// Heartbeat on unknown id returns ErrSyncLeaseLost.
	if err := HeartbeatSync(ctx, db, "nonexistent/id", 30*time.Second); !errors.Is(err, ErrSyncLeaseLost) {
		t.Fatalf("expected ErrSyncLeaseLost, got %v", err)
	}

	_ = ReleaseSync(ctx, db, res.ExecutionID, "done", "pretranscode")

	// Heartbeat after release returns ErrSyncLeaseLost.
	if err := HeartbeatSync(ctx, db, res.ExecutionID, 30*time.Second); !errors.Is(err, ErrSyncLeaseLost) {
		t.Fatalf("expected ErrSyncLeaseLost after release, got %v", err)
	}
}

func TestSyncAdmissionResourceBudgetBlocksExternalProcess(t *testing.T) {
	ResetSyncCounters()
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["prepare"] = 10
	policy.ResourceCapacity[ExternalProcess] = 2
	revID := seedActivePolicy(t, db, policy)
	setControlState(t, db, "prepare", "running")

	ctx := context.Background()

	// Hold 2 external_process tokens.
	insertActiveReservation(t, db, "exec-ep-1", "prepare", 1, revID)
	insertActiveReservation(t, db, "exec-ep-2", "prepare", 1, revID)

	// prepare uses 1 external_process per reservation, so third should block.
	_, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:    "pretranscode-3",
		TaskType: "prepare",
		Stage:    "prepare-transcode",
	}, policy, 30*time.Second)
	var blocked ErrSyncBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("expected ErrSyncBlocked for external_process budget, got %v", err)
	}
	if !strings.Contains(strings.ToLower(blocked.Blocker), "external") {
		t.Fatalf("blocker should mention external_process: %q", blocked.Blocker)
	}
}

func TestSyncAdmissionConcurrentDifferentLibraryScans(t *testing.T) {
	ResetSyncCounters()
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["scan"] = 2
	seedActivePolicy(t, db, policy)
	setControlState(t, db, "scan", "running")

	ctx := context.Background()

	res1, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:       "scanner-lib1",
		TaskType:    "scan",
		Stage:       "scan-library",
		LibraryID:   1,
		SourceClass: "video",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync lib1: %v", err)
	}

	res2, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:       "scanner-lib2",
		TaskType:    "scan",
		Stage:       "scan-library",
		LibraryID:   2,
		SourceClass: "video",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync lib2: %v", err)
	}

	// With concurrency 2, a third scan should block.
	_, err = AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:       "scanner-lib3",
		TaskType:    "scan",
		Stage:       "scan-library",
		LibraryID:   3,
		SourceClass: "video",
	}, policy, 30*time.Second)
	if err == nil {
		t.Fatal("expected third scan to block with concurrency 2")
	}

	// Release one and the third should proceed.
	_ = ReleaseSync(ctx, db, res1.ExecutionID, "done", "scanner-lib1")
	res3, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:       "scanner-lib3",
		TaskType:    "scan",
		Stage:       "scan-library",
		LibraryID:   3,
		SourceClass: "video",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync lib3 after release: %v", err)
	}

	_ = ReleaseSync(ctx, db, res2.ExecutionID, "done", "scanner-lib2")
	_ = ReleaseSync(ctx, db, res3.ExecutionID, "done", "scanner-lib3")
}

func TestSyncAdmissionCapabilityAbsenceStillBlocks(t *testing.T) {
	ResetSyncCounters()
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["scrape"] = 5
	seedActivePolicy(t, db, policy)
	// No control state set for scrape means it defaults to no row, which CheckControlState treats as running.

	ctx := context.Background()
	// Scrape should still be admitted when capacity is available.
	res, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:    "scraper",
		TaskType: "scrape",
		Stage:    "scrape-metadata",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync scrape: %v", err)
	}
	_ = ReleaseSync(ctx, db, res.ExecutionID, "done", "scraper")
}

func TestSyncAdmissionRetirementDiskTokensConstrained(t *testing.T) {
	ResetSyncCounters()
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["retirement"] = 10
	policy.ResourceCapacity[DiskRead] = 1
	policy.ResourceCapacity[DiskWrite] = 1
	revID := seedActivePolicy(t, db, policy)
	setControlState(t, db, "retirement", "running")

	ctx := context.Background()

	// retirement uses 1 disk_read, 1 disk_write per reservation.
	// With capacity 1/1, one reservation exhausts both.
	res1, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:    "retire-1",
		TaskType: "retirement",
		Stage:    "retirement-delete",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync retire-1: %v", err)
	}

	_, err = AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:    "retire-2",
		TaskType: "retirement",
		Stage:    "retirement-delete",
	}, policy, 30*time.Second)
	var blocked ErrSyncBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("expected ErrSyncBlocked for disk budget, got %v", err)
	}
	if !strings.Contains(strings.ToLower(blocked.Blocker), "disk") {
		t.Fatalf("blocker should mention disk: %q", blocked.Blocker)
	}

	_ = ReleaseSync(ctx, db, res1.ExecutionID, "done", "retire-1")
	_ = revID
}

func TestSyncAdmissionIngestDefaultConcurrency(t *testing.T) {
	ResetSyncCounters()
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	// ingest default concurrency is 3 from compiled defaults.
	seedActivePolicy(t, db, policy)
	setControlState(t, db, "ingest", "running")

	if got := policy.TypeConcurrency["ingest"]; got != 3 {
		t.Fatalf("ingest default concurrency = %d, want 3", got)
	}

	ctx := context.Background()
	// Acquire 3 ingest tokens.
	var released []string
	for i := 0; i < 3; i++ {
		res, err := AcquireSync(ctx, db, SyncAdmissionRequest{
			Owner:    "ingester",
			TaskType: "ingest",
			Stage:    "ingest-file",
		}, policy, 30*time.Second)
		if err != nil {
			t.Fatalf("AcquireSync ingest #%d: %v", i+1, err)
		}
		released = append(released, res.ExecutionID)
	}

	// 4th should block (limit 3).
	_, err := AcquireSync(ctx, db, SyncAdmissionRequest{
		Owner:    "ingester-extra",
		TaskType: "ingest",
		Stage:    "ingest-file",
	}, policy, 30*time.Second)
	var blocked ErrSyncBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("expected ErrSyncBlocked for 4th ingest, got %v", err)
	}

	for _, id := range released {
		_ = ReleaseSync(ctx, db, id, "done", "ingester")
	}
}

func TestSyncAdmissionRacePreventsOverAdmission(t *testing.T) {
	ResetSyncCounters()
	for iteration := 0; iteration < 10; iteration++ {
		db, _ := openAdmissionTestDB(t)
		ResetSyncCounters()
		policy := PolicyDefaults()
		policy.TypeConcurrency["metadata"] = 1
		seedActivePolicy(t, db, policy)
		setControlState(t, db, "metadata", "running")

		start := make(chan struct{})
		var success atomic.Int32
		var wg sync.WaitGroup

		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(idx int) {
				<-start
				ctx := context.Background()
				res, err := AcquireSync(ctx, db, SyncAdmissionRequest{
					Owner:    "racer",
					TaskType: "metadata",
					Stage:    "metadata-probe",
				}, policy, 30*time.Second)
				if err == nil {
					success.Add(1)
				}
				if res != nil {
					_ = ReleaseSync(ctx, db, res.ExecutionID, "done", "racer")
				}
				wg.Done()
			}(i)
		}
		close(start)
		wg.Wait()

		if success.Load() != 1 {
			t.Fatalf("iteration %d: expected exactly 1 success, got %d", iteration, success.Load())
		}
	}
}

func TestSyncAdmissionSequentialStagesWithinScan(t *testing.T) {
	ResetSyncCounters()
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["metadata"] = 1
	seedActivePolicy(t, db, policy)
	setControlState(t, db, "metadata", "running")

	ctx := context.Background()
	stages := []string{"metadata-probe", "poster-generate", "scrape-online"}

	for _, stage := range stages {
		res, err := AcquireSync(ctx, db, SyncAdmissionRequest{
			Owner:       "scanner",
			TaskType:    "metadata",
			Stage:       stage,
			LibraryID:   1,
			SourceClass: "video",
		}, policy, 30*time.Second)
		if err != nil {
			t.Fatalf("AcquireSync stage %q failed: %v", stage, err)
		}
		// Sequential stages within a scan: release before next stage.
		if err := ReleaseSync(ctx, db, res.ExecutionID, stage+"-done", "scanner"); err != nil {
			t.Fatalf("ReleaseSync stage %q: %v", stage, err)
		}
	}
}
