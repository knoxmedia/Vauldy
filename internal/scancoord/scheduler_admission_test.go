package scancoord

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"knox-media/internal/scheduler"
	"knox-media/internal/store"
)

func openScanAdmissionDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "scan-adm-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	path := filepath.Join(dir, "scan-admission.db")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	cleanup := func() {
		_ = db.Close()
		_ = os.RemoveAll(dir)
	}
	t.Cleanup(cleanup)
	return db, cleanup
}

func scanSeedPolicy(t *testing.T, db *sql.DB, policy scheduler.Policy) int64 {
	t.Helper()
	s := scheduler.NewStore(db)
	ctx := context.Background()
	tcJSON := scanMapToJSON(policy.TypeConcurrency)
	rcJSON := scanResourceMapToJSON(policy.ResourceCapacity)
	pcJSON := scanMapToJSON(policy.ProviderCapacity)
	policyJSON := fmt.Sprintf(`{"type_concurrency":%s,"resource_capacity":%s,"provider_capacity":%s,"aging_interval_sec":%d,"aging_step":%d,"run_now_amount":%d,"run_now_ttl_sec":%d}`,
		tcJSON, rcJSON, pcJSON, policy.AgingIntervalSec, policy.AgingStep, policy.RunNowAmount, policy.RunNowTTLSec)
	rev, err := s.CreatePolicyRevision(ctx, 1, nil, policyJSON, "test", "scan admission test", "hash")
	if err != nil {
		t.Fatalf("create policy revision: %v", err)
	}
	if err := s.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatalf("activate policy revision: %v", err)
	}
	return rev.ID
}

func scanMapToJSON(m map[string]int) string {
	if len(m) == 0 {
		return "{}"
	}
	s := "{"
	first := true
	for k, v := range m {
		if !first {
			s += ","
		}
		first = false
		s += fmt.Sprintf("%q:%d", k, v)
	}
	return s + "}"
}

func scanResourceMapToJSON(m map[scheduler.ResourceKind]int) string {
	if len(m) == 0 {
		return "{}"
	}
	s := "{"
	first := true
	for k, v := range m {
		if !first {
			s += ","
		}
		first = false
		s += fmt.Sprintf("%q:%d", string(k), v)
	}
	return s + "}"
}

func scanSetControl(t *testing.T, db *sql.DB, taskType, state string) {
	t.Helper()
	s := scheduler.NewStore(db)
	if err := s.SetControlState(context.Background(), taskType, state); err != nil {
		t.Fatalf("set control state: %v", err)
	}
}

func scanInsertReservation(t *testing.T, db *sql.DB, execID, taskType string, units int, revID int64) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,?,?,?,'active',?)`,
		execID, taskType, units, revID, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("insert reservation: %v", err)
	}
}

func TestSchedulerAdmissionScanGlobalLimitOne(t *testing.T) {	scheduler.ResetSyncCounters()
	db, _ := openScanAdmissionDB(t)
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["scan"] = 1
	_ = scanSeedPolicy(t, db, policy)
	scanSetControl(t, db, "scan", "running")

	ctx := context.Background()

	res, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "scanner",
		TaskType: "scan",
		Stage:    "scan-library",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync first scan: %v", err)
	}

	_, err = scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "scanner-other",
		TaskType: "scan",
		Stage:    "scan-library",
	}, policy, 30*time.Second)
	if err == nil {
		t.Fatal("second scan should be blocked by global limit of 1")
	}

	_ = scheduler.ReleaseSync(ctx, db, res.ExecutionID, "done", "scanner")
}

func TestSchedulerAdmissionScanConcurrentDifferentLibraries(t *testing.T) {	scheduler.ResetSyncCounters()
	db, _ := openScanAdmissionDB(t)
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["scan"] = 2
	_ = scanSeedPolicy(t, db, policy)
	scanSetControl(t, db, "scan", "running")

	ctx := context.Background()

	resLib1, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:       "scanner-lib1",
		TaskType:    "scan",
		Stage:       "scan-library",
		LibraryID:   1,
		SourceClass: "video",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync lib1: %v", err)
	}

	resLib2, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:       "scanner-lib2",
		TaskType:    "scan",
		Stage:       "scan-library",
		LibraryID:   2,
		SourceClass: "audio",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync lib2: %v", err)
	}

	_, err = scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:       "scanner-lib3",
		TaskType:    "scan",
		Stage:       "scan-library",
		LibraryID:   3,
		SourceClass: "video",
	}, policy, 30*time.Second)
	if err == nil {
		t.Fatal("third concurrent scan should be blocked with limit 2")
	}

	_ = scheduler.ReleaseSync(ctx, db, resLib1.ExecutionID, "done", "scanner-lib1")
	_ = scheduler.ReleaseSync(ctx, db, resLib2.ExecutionID, "done", "scanner-lib2")
}

func TestSchedulerAdmissionSequentialScanStages(t *testing.T) {	scheduler.ResetSyncCounters()
	db, _ := openScanAdmissionDB(t)
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["metadata"] = 1
	_ = scanSeedPolicy(t, db, policy)
	scanSetControl(t, db, "metadata", "running")

	ctx := context.Background()

	// Sequential stages within a scan must not all hold concurrent tokens.
	stage1, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "scanner",
		TaskType: "metadata",
		Stage:    "probe-ffprobe",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync stage1: %v", err)
	}

	// Release stage1 before stage2.
	_ = scheduler.ReleaseSync(ctx, db, stage1.ExecutionID, "probe-done", "scanner")

	stage2, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "scanner",
		TaskType: "metadata",
		Stage:    "extract-poster",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync stage2: %v", err)
	}

	_ = scheduler.ReleaseSync(ctx, db, stage2.ExecutionID, "poster-done", "scanner")
}

func TestSchedulerAdmissionScanRaceOneLibraryLease(t *testing.T) {	scheduler.ResetSyncCounters()
	for iteration := 0; iteration < 5; iteration++ {
		db, _ := openScanAdmissionDB(t)
		scheduler.ResetSyncCounters()
		policy := scheduler.PolicyDefaults()
		policy.TypeConcurrency["scan"] = 1
		_ = scanSeedPolicy(t, db, policy)
		scanSetControl(t, db, "scan", "running")

		start := make(chan struct{})
		var leased atomic.Int32
		var wg sync.WaitGroup

		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				<-start
				ctx := context.Background()
				res, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
					Owner:    "scan-racer",
					TaskType: "scan",
					Stage:    "scan-library",
				}, policy, 30*time.Second)
				if err == nil {
					leased.Add(1)
				}
				if res != nil {
					_ = scheduler.ReleaseSync(ctx, db, res.ExecutionID, "done", "scan-racer")
				}
				wg.Done()
			}()
		}
		close(start)
		wg.Wait()

		if leased.Load() > 1 {
			t.Fatalf("iteration %d: scan leases %d > global limit 1", iteration, leased.Load())
		}
	}
}
