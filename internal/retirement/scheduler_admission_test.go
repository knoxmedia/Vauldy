package retirement

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

func openRetireAdmissionDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "retire-adm-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	path := filepath.Join(dir, "retire-admission.db")
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

func retireSeedPolicy(t *testing.T, db *sql.DB, policy scheduler.Policy) int64 {
	t.Helper()
	s := scheduler.NewStore(db)
	ctx := context.Background()
	policyJSON := fmt.Sprintf(`{"type_concurrency":%s,"resource_capacity":%s,"provider_capacity":%s,"aging_interval_sec":%d,"aging_step":%d,"run_now_amount":%d,"run_now_ttl_sec":%d}`,
		retireMapJSON(policy.TypeConcurrency),
		retireResMapJSON(policy.ResourceCapacity),
		retireMapJSON(policy.ProviderCapacity),
		policy.AgingIntervalSec, policy.AgingStep, policy.RunNowAmount, policy.RunNowTTLSec)
	rev, err := s.CreatePolicyRevision(ctx, 1, nil, policyJSON, "test", "retirement admission test", "hash")
	if err != nil {
		t.Fatalf("create policy revision: %v", err)
	}
	if err := s.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatalf("activate policy revision: %v", err)
	}
	return rev.ID
}

func retireMapJSON(m map[string]int) string {
	if len(m) == 0 {
		return "{}"
	}
	js := "{"
	first := true
	for k, v := range m {
		if !first {
			js += ","
		}
		first = false
		js += fmt.Sprintf("%q:%d", k, v)
	}
	return js + "}"
}

func retireResMapJSON(m map[scheduler.ResourceKind]int) string {
	if len(m) == 0 {
		return "{}"
	}
	js := "{"
	first := true
	for k, v := range m {
		if !first {
			js += ","
		}
		first = false
		js += fmt.Sprintf("%q:%d", string(k), v)
	}
	return js + "}"
}

func retireSetControl(t *testing.T, db *sql.DB, taskType, state string) {
	t.Helper()
	st := scheduler.NewStore(db)
	if err := st.SetControlState(context.Background(), taskType, state); err != nil {
		t.Fatalf("set control state: %v", err)
	}
}

func retireInsertReservation(t *testing.T, db *sql.DB, execID, taskType string, units int, revID int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,?,?,?,'active',?)`,
		execID, taskType, units, revID, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("insert reservation: %v", err)
	}
}

func TestSchedulerAdmissionRetirementDiskTokensConstrained(t *testing.T) {
	scheduler.ResetSyncCounters()
scheduler.ResetSyncCounters()
	db, _ := openRetireAdmissionDB(t)
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["retirement"] = 10
	policy.ResourceCapacity[scheduler.DiskRead] = 1
	policy.ResourceCapacity[scheduler.DiskWrite] = 1
	_ = retireSeedPolicy(t, db, policy)
	retireSetControl(t, db, "retirement", "running")

	ctx := context.Background()

	res1, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "retire-worker-1",
		TaskType: "retirement",
		Stage:    "retirement-cleanup",
	}, policy, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireSync retire-1: %v", err)
	}

	_, err = scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "retire-worker-2",
		TaskType: "retirement",
		Stage:    "retirement-cleanup",
	}, policy, 30*time.Second)
	if err == nil {
		t.Fatal("second retirement should be blocked by disk budget")
	}

	_ = scheduler.ReleaseSync(ctx, db, res1.ExecutionID, "done", "retire-worker-1")
}

func TestSchedulerAdmissionRetirementConcurrencyDefault(t *testing.T) {
	scheduler.ResetSyncCounters()
scheduler.ResetSyncCounters()
	db, _ := openRetireAdmissionDB(t)
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["retirement"] = 2
	_ = retireSeedPolicy(t, db, policy)
	retireSetControl(t, db, "retirement", "running")

	ctx := context.Background()

	var acquired []string
	for i := 0; i < 2; i++ {
		res, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
			Owner:    fmt.Sprintf("retire-%d", i),
			TaskType: "retirement",
			Stage:    "retirement-cleanup",
		}, policy, 30*time.Second)
		if err != nil {
			t.Fatalf("AcquireSync retirement #%d: %v", i+1, err)
		}
		acquired = append(acquired, res.ExecutionID)
	}

	_, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "retire-extra",
		TaskType: "retirement",
		Stage:    "retirement-cleanup",
	}, policy, 30*time.Second)
	if err == nil {
		t.Fatal("third retirement should be blocked by concurrency 2")
	}

	for _, id := range acquired {
		_ = scheduler.ReleaseSync(ctx, db, id, "done", "retire")
	}
}

func TestSchedulerAdmissionSharedResourceRetirementDiskContention(t *testing.T) {
	scheduler.ResetSyncCounters()
scheduler.ResetSyncCounters()
	for iteration := 0; iteration < 5; iteration++ {
		db, _ := openRetireAdmissionDB(t)
		scheduler.ResetSyncCounters()
		policy := scheduler.PolicyDefaults()
		policy.TypeConcurrency["retirement"] = 10
		policy.ResourceCapacity[scheduler.DiskWrite] = 1
		_ = retireSeedPolicy(t, db, policy)
		retireSetControl(t, db, "retirement", "running")

		start := make(chan struct{})
		var admitted atomic.Int32
		var wg sync.WaitGroup

		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				<-start
				ctx := context.Background()
				res, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
					Owner:    "retire-racer",
					TaskType: "retirement",
					Stage:    "retirement-cleanup",
				}, policy, 30*time.Second)
				if err == nil {
					admitted.Add(1)
				}
				if res != nil {
					_ = scheduler.ReleaseSync(ctx, db, res.ExecutionID, "done", "retire-racer")
				}
				wg.Done()
			}()
		}
		close(start)
		wg.Wait()

		if admitted.Load() > 1 {
			t.Fatalf("iteration %d: retirement admitted %d > disk budget 1", iteration, admitted.Load())
		}
	}
}
