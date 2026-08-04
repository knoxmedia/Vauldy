package pretranscode

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

func openPrepareAdmissionDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "prepare-adm-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	path := filepath.Join(dir, "prepare-admission.db")
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

func prepareSeedPolicy(t *testing.T, db *sql.DB, policy scheduler.Policy) int64 {
	t.Helper()
	s := scheduler.NewStore(db)
	ctx := context.Background()
	policyJSON := fmt.Sprintf(`{"type_concurrency":%s,"resource_capacity":%s,"provider_capacity":%s,"aging_interval_sec":%d,"aging_step":%d,"run_now_amount":%d,"run_now_ttl_sec":%d}`,
		strMapJSON(policy.TypeConcurrency),
		resMapJSON(policy.ResourceCapacity),
		strMapJSON(policy.ProviderCapacity),
		policy.AgingIntervalSec, policy.AgingStep, policy.RunNowAmount, policy.RunNowTTLSec)
	rev, err := s.CreatePolicyRevision(ctx, 1, nil, policyJSON, "test", "prepare admission test", "hash")
	if err != nil {
		t.Fatalf("create policy revision: %v", err)
	}
	if err := s.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatalf("activate policy revision: %v", err)
	}
	return rev.ID
}

func strMapJSON(m map[string]int) string {
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

func resMapJSON(m map[scheduler.ResourceKind]int) string {
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

func prepareSetControl(t *testing.T, db *sql.DB, taskType, state string) {
	t.Helper()
	st := scheduler.NewStore(db)
	if err := st.SetControlState(context.Background(), taskType, state); err != nil {
		t.Fatalf("set control state: %v", err)
	}
}

func prepareInsertReservation(t *testing.T, db *sql.DB, execID, taskType string, units int, revID int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,?,?,?,'active',?)`,
		execID, taskType, units, revID, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("insert reservation: %v", err)
	}
}

func TestSchedulerAdmissionPrepareExternalProcessConstrained(t *testing.T) {
	scheduler.ResetSyncCounters()
scheduler.ResetSyncCounters()
	db, _ := openPrepareAdmissionDB(t)
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["prepare"] = 10
	policy.ResourceCapacity[scheduler.ExternalProcess] = 1
	revID := prepareSeedPolicy(t, db, policy)
	prepareSetControl(t, db, "prepare", "running")

	ctx := context.Background()

	res1, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "pretranscode-1",
		TaskType: "prepare",
		Stage:    "prepare-transcode",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync prepare-1: %v", err)
	}

	_, err = scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "pretranscode-2",
		TaskType: "prepare",
		Stage:    "prepare-transcode",
	}, policy, 30*time.Second)
	if err == nil {
		t.Fatal("second prepare should be blocked by external_process budget")
	}

	_ = scheduler.ReleaseSync(ctx, db, res1.ExecutionID, "done", "pretranscode-1")
	_ = revID
}

func TestSchedulerAdmissionPrepareDefaultConcurrencyUsedViaAdmission(t *testing.T) {
	scheduler.ResetSyncCounters()
scheduler.ResetSyncCounters()
	db, _ := openPrepareAdmissionDB(t)
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["prepare"] = 2
	_ = prepareSeedPolicy(t, db, policy)
	prepareSetControl(t, db, "prepare", "running")

	ctx := context.Background()

	var acquired []string
	for i := 0; i < 2; i++ {
		res, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
			Owner:    fmt.Sprintf("pretranscode-%d", i),
			TaskType: "prepare",
			Stage:    "prepare-transcode",
		}, policy, 30*time.Second)
		if err != nil {
			t.Fatalf("AcquireSync prepare #%d: %v", i+1, err)
		}
		acquired = append(acquired, res.ExecutionID)
	}

	_, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "pretranscode-extra",
		TaskType: "prepare",
		Stage:    "prepare-transcode",
	}, policy, 30*time.Second)
	if err == nil {
		t.Fatal("third prepare should be blocked by concurrency 2")
	}

	for _, id := range acquired {
		_ = scheduler.ReleaseSync(ctx, db, id, "done", "pretranscode")
	}
}

func TestSchedulerAdmissionPrepareConcurrencySharedResource(t *testing.T) {
	scheduler.ResetSyncCounters()
scheduler.ResetSyncCounters()
	for iteration := 0; iteration < 5; iteration++ {
		db, _ := openPrepareAdmissionDB(t)
		scheduler.ResetSyncCounters()
		policy := scheduler.PolicyDefaults()
		policy.TypeConcurrency["prepare"] = 2
		_ = prepareSeedPolicy(t, db, policy)
		prepareSetControl(t, db, "prepare", "running")

		start := make(chan struct{})
		var admitted atomic.Int32
		var wg sync.WaitGroup

		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				<-start
				ctx := context.Background()
				res, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
					Owner:    "prepare-racer",
					TaskType: "prepare",
					Stage:    "prepare-transcode",
				}, policy, 30*time.Second)
				if err == nil {
					admitted.Add(1)
				}
				if res != nil {
					_ = scheduler.ReleaseSync(ctx, db, res.ExecutionID, "done", "prepare-racer")
				}
				wg.Done()
			}()
		}
		close(start)
		wg.Wait()

		if admitted.Load() > 2 {
			t.Fatalf("iteration %d: prepared %d > concurrency 2", iteration, admitted.Load())
		}
	}
}
