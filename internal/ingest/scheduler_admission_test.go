package ingest

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

func openIngestAdmissionDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ingest-adm-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	path := filepath.Join(dir, "ingest-admission.db")
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

func TestSchedulerAdmissionIngestDefaultConcurrency3(t *testing.T) {
	scheduler.ResetSyncCounters()
	db, _ := openIngestAdmissionDB(t)
	policy := scheduler.PolicyDefaults()
	seedSchedulerPolicy(t, db, policy)
	_ = setSchedulerControl(t, db, "ingest", "running")

	ctx := context.Background()
	var released []string

	for i := 0; i < 3; i++ {
		res, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
			Owner:    "ingest-worker",
			TaskType: "ingest",
			Stage:    "ingest-file",
		}, policy, 30*time.Second)
		if err != nil {
			t.Fatalf("AcquireSync ingest #%d should succeed with concurrency 3: %v", i+1, err)
		}
		released = append(released, res.ExecutionID)
	}

	_, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "ingest-worker-extra",
		TaskType: "ingest",
		Stage:    "ingest-file",
	}, policy, 30*time.Second)
	if err == nil {
		t.Fatal("4th ingest acquisition should be blocked by concurrency limit of 3")
	}

	for _, id := range released {
		_ = scheduler.ReleaseSync(ctx, db, id, "done", "ingest-worker")
	}
}

func TestSchedulerAdmissionIngestSharedResourceDiskContention(t *testing.T) {
	scheduler.ResetSyncCounters()
	db, _ := openIngestAdmissionDB(t)
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["ingest"] = 10
	policy.ResourceCapacity[scheduler.DiskWrite] = 1
	revID := seedSchedulerPolicy(t, db, policy)
	_ = setSchedulerControl(t, db, "ingest", "running")

	ctx := context.Background()

	insertActiveReservation(t, db, "ingest-disk-1", "ingest", 1, revID)

	_, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
		Owner:    "ingest-worker-2",
		TaskType: "ingest",
		Stage:    "ingest-file",
	}, policy, 30*time.Second)
	if err == nil {
		t.Fatal("ingest should be blocked by disk write budget when token is held")
	}
}

func TestSchedulerAdmissionConcurrencyIngestRespectedUnderRace(t *testing.T) {
	for iteration := 0; iteration < 5; iteration++ {
		db, _ := openIngestAdmissionDB(t)
		scheduler.ResetSyncCounters()
		policy := scheduler.PolicyDefaults()
		policy.TypeConcurrency["ingest"] = 2
		seedSchedulerPolicy(t, db, policy)
		_ = setSchedulerControl(t, db, "ingest", "running")

		start := make(chan struct{})
		var admitted atomic.Int32
		var wg sync.WaitGroup

		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				<-start
				ctx := context.Background()
				res, err := scheduler.AcquireSync(ctx, db, scheduler.SyncAdmissionRequest{
					Owner:    "ingest-racer",
					TaskType: "ingest",
					Stage:    "ingest-file",
				}, policy, 30*time.Second)
				if err == nil {
					admitted.Add(1)
				}
				if res != nil {
					_ = scheduler.ReleaseSync(ctx, db, res.ExecutionID, "done", "ingest-racer")
				}
				wg.Done()
			}()
		}
		close(start)
		wg.Wait()

		if admitted.Load() > 2 {
			t.Fatalf("iteration %d: admitted %d > concurrency limit 2", iteration, admitted.Load())
		}
	}
}

// --------- test helpers ---------

func seedSchedulerPolicy(t *testing.T, db *sql.DB, policy scheduler.Policy) int64 {
	t.Helper()
	s := scheduler.NewStore(db)
	ctx := context.Background()
	policyJSON := schedulerPolicyJSON(policy)
	rev, err := s.CreatePolicyRevision(ctx, 1, nil, policyJSON, "test", "ingest admission test", "hash")
	if err != nil {
		t.Fatalf("create policy revision: %v", err)
	}
	if err := s.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatalf("activate policy revision: %v", err)
	}
	return rev.ID
}

func setSchedulerControl(t *testing.T, db *sql.DB, taskType, state string) int {
	t.Helper()
	s := scheduler.NewStore(db)
	if err := s.SetControlState(context.Background(), taskType, state); err != nil {
		t.Fatalf("set control state: %v", err)
	}
	return 0
}

func schedulerPolicyJSON(p scheduler.Policy) string {
	return fmt.Sprintf(
		`{"type_concurrency":%s,"resource_capacity":%s,"provider_capacity":%s,"aging_interval_sec":%d,"aging_step":%d,"run_now_amount":%d,"run_now_ttl_sec":%d}`,
		mapToJSONInt(p.TypeConcurrency),
		resourceMapToJSONInt(p.ResourceCapacity),
		mapToJSONStr(p.ProviderCapacity),
		p.AgingIntervalSec,
		p.AgingStep,
		p.RunNowAmount,
		p.RunNowTTLSec,
	)
}

func mapToJSONInt(m map[string]int) string {
	if len(m) == 0 {
		return "{}"
	}
	parts := ""
	first := true
	for k, v := range m {
		if !first {
			parts += ","
		}
		first = false
		parts += fmt.Sprintf("%q:%d", k, v)
	}
	return "{" + parts + "}"
}

func resourceMapToJSONInt(m map[scheduler.ResourceKind]int) string {
	if len(m) == 0 {
		return "{}"
	}
	parts := ""
	first := true
	for k, v := range m {
		if !first {
			parts += ","
		}
		first = false
		parts += fmt.Sprintf("%q:%d", string(k), v)
	}
	return "{" + parts + "}"
}

func mapToJSONStr(m map[string]int) string {
	if len(m) == 0 {
		return "{}"
	}
	parts := ""
	first := true
	for k, v := range m {
		if !first {
			parts += ","
		}
		first = false
		parts += fmt.Sprintf("%q:%d", k, v)
	}
	return "{" + parts + "}"
}

func insertActiveReservation(t *testing.T, db *sql.DB, execID, taskType string, units int, revID int64) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,?,?,?,'active',?)`,
		execID, taskType, units, revID, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("insert active reservation: %v", err)
	}
}
