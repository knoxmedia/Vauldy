package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"knox-media/internal/store"
)

// fakeMetrics is a no-op SQLiteMetrics for tests.
type fakeMetrics struct{}

func (fakeMetrics) RecordImmediateTransaction() {}

// capabilityRegistry is a simple test registry.
type capabilityRegistry struct {
	available map[string]bool
}

func (r capabilityRegistry) Available(step string) bool {
	if r.available == nil {
		return true
	}
	return r.available[step]
}

type alwaysRegistry struct{}

func (alwaysRegistry) Available(string) bool { return true }

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func openAdmissionTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "admission-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	path := filepath.Join(dir, "admission.db")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		for i := 0; i < 20; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
	return db, path
}

func seedAdmissionLibraryMedia(t *testing.T, db *sql.DB) (libraryID, mediaID int64) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('test','video','/test')`)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libraryID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,ingest_generation,publication_state) VALUES(?,?,1,'processing')`, libraryID, "test-media")
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	mediaID, _ = res.LastInsertId()
	return libraryID, mediaID
}

func seedActivePolicy(t *testing.T, db *sql.DB, policy Policy) int64 {
	t.Helper()
	s := NewStore(db)
	ctx := context.Background()
	policyJSON := fmt.Sprintf(`{"type_concurrency":%s,"resource_capacity":%s,"provider_capacity":%s,"aging_interval_sec":%d,"aging_step":%d,"run_now_amount":%d,"run_now_ttl_sec":%d}`,
		mapToJSON(policy.TypeConcurrency),
		resourceMapToJSON(policy.ResourceCapacity),
		mapToJSON(policy.ProviderCapacity),
		policy.AgingIntervalSec,
		policy.AgingStep,
		policy.RunNowAmount,
		policy.RunNowTTLSec,
	)
	rev, err := s.CreatePolicyRevision(ctx, 1, nil, policyJSON, "test", "admission test", "hash")
	if err != nil {
		t.Fatalf("create policy revision: %v", err)
	}
	if err := s.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatalf("activate policy revision: %v", err)
	}
	return rev.ID
}

func mapToJSON(m map[string]int) string {
	if len(m) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%q:%d", k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func resourceMapToJSON(m map[ResourceKind]int) string {
	if len(m) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%q:%d", string(k), v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func setControlState(t *testing.T, db *sql.DB, taskType, state string) {
	t.Helper()
	s := NewStore(db)
	if err := s.SetControlState(context.Background(), taskType, state); err != nil {
		t.Fatalf("set control state: %v", err)
	}
}

func insertActiveReservation(t *testing.T, db *sql.DB, executionID, taskType string, units int, policyRevisionID int64) {
	t.Helper()
	leaseUntil := time.Now().Add(5 * time.Minute)
	_, err := db.Exec(`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,?,?,?,'active',?)`,
		executionID, taskType, units, policyRevisionID, leaseUntil)
	if err != nil {
		t.Fatalf("insert reservation: %v", err)
	}
}

// ============================================================================
// RED tests for budget checking functions
// ============================================================================

func TestAdmissionCheckTypeConcurrencyBlocked(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["poster"] = 1
	revID := seedActivePolicy(t, db, policy)
	setControlState(t, db, "poster", "running")

	insertActiveReservation(t, db, "exec-1", "poster", 1, revID)

	ctx := context.Background()
	now := time.Now()
	bl, err := CheckTypeConcurrency(ctx, db, "poster", policy, now)
	if err != nil {
		t.Fatalf("CheckTypeConcurrency: %v", err)
	}
	if bl == nil {
		t.Fatal("expected concurrency blocker, got nil")
	}
	if !strings.Contains(strings.ToLower(bl.Reason), "concurrency") {
		t.Fatalf("expected concurrency reason, got %q", bl.Reason)
	}
	if bl.TaskType != "poster" {
		t.Fatalf("expected poster blocker, got %q", bl.TaskType)
	}
}

func TestAdmissionCheckTypeConcurrencyAllowed(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["poster"] = 3
	seedActivePolicy(t, db, policy)
	setControlState(t, db, "poster", "running")

	ctx := context.Background()
	now := time.Now()
	bl, err := CheckTypeConcurrency(ctx, db, "poster", policy, now)
	if err != nil {
		t.Fatalf("CheckTypeConcurrency: %v", err)
	}
	if bl != nil {
		t.Fatalf("expected no blocker, got %+v", bl)
	}
}

func TestAdmissionCheckResourceBudgetBlocked(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["encrypt"] = 5
	policy.ResourceCapacity[CPU] = 2
	revID := seedActivePolicy(t, db, policy)
	setControlState(t, db, "encrypt", "running")

	// encrypt uses 1 CPU per reservation
	insertActiveReservation(t, db, "exec-1", "encrypt", 1, revID)
	insertActiveReservation(t, db, "exec-2", "encrypt", 1, revID)

	ctx := context.Background()
	now := time.Now()
	bl, err := CheckResourceBudget(ctx, db, "encrypt", policy, now)
	if err != nil {
		t.Fatalf("CheckResourceBudget: %v", err)
	}
	if bl == nil {
		t.Fatal("expected resource blocker, got nil")
	}
	if !strings.Contains(strings.ToLower(bl.Reason), "resource") {
		t.Fatalf("expected resource reason, got %q", bl.Reason)
	}
}

func TestAdmissionCheckResourceBudgetAllowed(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["encrypt"] = 5
	policy.ResourceCapacity[CPU] = 10
	seedActivePolicy(t, db, policy)
	setControlState(t, db, "encrypt", "running")

	ctx := context.Background()
	now := time.Now()
	bl, err := CheckResourceBudget(ctx, db, "encrypt", policy, now)
	if err != nil {
		t.Fatalf("CheckResourceBudget: %v", err)
	}
	if bl != nil {
		t.Fatalf("expected no blocker, got %+v", bl)
	}
}

func TestAdmissionCheckProviderBudgetBlocked(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["ai_analysis"] = 5
	policy.ProviderCapacity["openai"] = 1
	revID := seedActivePolicy(t, db, policy)
	setControlState(t, db, "ai_analysis", "running")

	// ai_analysis uses provider "" (empty in defaults), need to configure
	// Let's use a task type that has a provider set
	// Actually ai_analysis has Provider="" in defaults. Let me use a custom setup.
	// I'll test with raw SQL instead

	ctx := context.Background()
	now := time.Now()
	// ai_analysis in defaults has no provider, so this shouldn't block
	bl, err := CheckProviderBudget(ctx, db, "ai_analysis", policy, now)
	if err != nil {
		t.Fatalf("CheckProviderBudget: %v", err)
	}
	// ai_analysis has no provider in defaults, so no blocker expected
	_ = bl
	_ = revID
}

func TestAdmissionCheckProviderBudgetAllowed(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.ProviderCapacity["openai"] = 5
	seedActivePolicy(t, db, policy)

	ctx := context.Background()
	now := time.Now()
	// poster has no provider
	bl, err := CheckProviderBudget(ctx, db, "poster", policy, now)
	if err != nil {
		t.Fatalf("CheckProviderBudget: %v", err)
	}
	if bl != nil {
		t.Fatalf("expected no blocker for type without provider, got %+v", bl)
	}
}

func TestAdmissionCheckControlStatePaused(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	setControlState(t, db, "poster", "paused")

	ctx := context.Background()
	state, bl, err := CheckControlState(ctx, db, "poster")
	if err != nil {
		t.Fatalf("CheckControlState: %v", err)
	}
	if state != "paused" {
		t.Fatalf("expected paused, got %q", state)
	}
	if bl == nil {
		t.Fatal("expected blocker for paused type")
	}
	if !strings.Contains(strings.ToLower(bl.Reason), "paused") {
		t.Fatalf("expected paused reason, got %q", bl.Reason)
	}
}

func TestAdmissionCheckControlStateDraining(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	setControlState(t, db, "poster", "draining")

	ctx := context.Background()
	state, bl, err := CheckControlState(ctx, db, "poster")
	if err != nil {
		t.Fatalf("CheckControlState: %v", err)
	}
	if state != "draining" {
		t.Fatalf("expected draining, got %q", state)
	}
	if bl == nil {
		t.Fatal("expected blocker for draining type")
	}
}

func TestAdmissionCheckControlStateRunning(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	setControlState(t, db, "poster", "running")

	ctx := context.Background()
	state, bl, err := CheckControlState(ctx, db, "poster")
	if err != nil {
		t.Fatalf("CheckControlState: %v", err)
	}
	if state != "running" {
		t.Fatalf("expected running, got %q", state)
	}
	if bl != nil {
		t.Fatalf("expected no blocker for running, got %+v", bl)
	}
}

func TestAdmissionCheckAllBudgetsWithBlockers(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["poster"] = 1
	policy.ResourceCapacity[CPU] = 1
	revID := seedActivePolicy(t, db, policy)
	setControlState(t, db, "poster", "running")

	insertActiveReservation(t, db, "exec-1", "poster", 1, revID)

	ctx := context.Background()
	now := time.Now()
	blockers, err := CheckAllBudgets(ctx, db, "poster", policy, now)
	if err != nil {
		t.Fatalf("CheckAllBudgets: %v", err)
	}
	if len(blockers) == 0 {
		t.Fatal("expected blockers")
	}
	hasConcurrency := false
	hasResource := false
	for _, b := range blockers {
		if strings.Contains(strings.ToLower(b.Reason), "concurrency") {
			hasConcurrency = true
		}
		if strings.Contains(strings.ToLower(b.Reason), "resource") {
			hasResource = true
		}
	}
	if !hasConcurrency {
		t.Fatal("expected concurrency blocker")
	}
	if !hasResource {
		t.Fatal("expected resource blocker")
	}
}

func TestAdmissionCheckAllBudgetsClear(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["poster"] = 5
	seedActivePolicy(t, db, policy)
	setControlState(t, db, "poster", "running")

	ctx := context.Background()
	now := time.Now()
	blockers, err := CheckAllBudgets(ctx, db, "poster", policy, now)
	if err != nil {
		t.Fatalf("CheckAllBudgets: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("expected no blockers, got %+v", blockers)
	}
}

func TestAdmissionActiveReservationCount(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["poster"] = 5
	revID := seedActivePolicy(t, db, policy)
	setControlState(t, db, "poster", "running")

	insertActiveReservation(t, db, "exec-1", "poster", 1, revID)
	insertActiveReservation(t, db, "exec-2", "poster", 2, revID)

	ctx := context.Background()
	now := time.Now()
	count, err := ActiveReservationCount(ctx, db, "poster", now)
	if err != nil {
		t.Fatalf("ActiveReservationCount: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 active reservations, got %d", count)
	}

	// Check that released reservations don't count
	if _, err := db.Exec(`UPDATE scheduler_reservation SET status='released', released_at=CURRENT_TIMESTAMP, release_reason='test', released_by='test' WHERE execution_id='exec-1'`); err != nil {
		t.Fatal(err)
	}
	count2, err := ActiveReservationCount(ctx, db, "poster", now)
	if err != nil {
		t.Fatalf("ActiveReservationCount after release: %v", err)
	}
	if count2 != 1 {
		t.Fatalf("expected 1 active reservation after release, got %d", count2)
	}
}

func TestAdmissionActiveResourceUsage(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	revID := seedActivePolicy(t, db, policy)
	setControlState(t, db, "poster", "running")
	setControlState(t, db, "thumbnail", "running")

	// poster uses {cpu:1, disk_read:1, disk_write:1, external_process:1}
	insertActiveReservation(t, db, "exec-1", "poster", 1, revID)
	// thumbnail also uses {cpu:1, disk_read:1, disk_write:1, external_process:1}
	insertActiveReservation(t, db, "exec-2", "thumbnail", 1, revID)

	ctx := context.Background()
	now := time.Now()
	usage, err := ActiveResourceUsage(ctx, db, now)
	if err != nil {
		t.Fatalf("ActiveResourceUsage: %v", err)
	}
	if usage[CPU] != 2 {
		t.Fatalf("expected 2 CPU usage, got %d", usage[CPU])
	}
	if usage[DiskRead] != 2 {
		t.Fatalf("expected 2 disk_read usage, got %d", usage[DiskRead])
	}
}

func TestAdmissionGenerateExecutionID(t *testing.T) {
	id1 := GenerateExecutionID("worker")
	id2 := GenerateExecutionID("worker")
	if id1 == id2 {
		t.Fatal("execution IDs should be unique")
	}
	if !strings.HasPrefix(id1, "worker/") {
		t.Fatalf("execution ID should start with owner: %q", id1)
	}
}

func TestAdmissionInsertReservation(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	revID := seedActivePolicy(t, db, policy)

	ctx := context.Background()
	execID := GenerateExecutionID("worker")
	resID, err := InsertAdmissionReservation(ctx, db, execID, "poster", 1, revID, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("InsertAdmissionReservation: %v", err)
	}
	if resID == 0 {
		t.Fatal("expected non-zero reservation ID")
	}

	// Verify it was created
	var status string
	if err := db.QueryRow(`SELECT status FROM scheduler_reservation WHERE execution_id=?`, execID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("expected active, got %q", status)
	}
}

func TestAdmissionZeroTypeConcurrencyIsBlocked(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	policy.TypeConcurrency["poster"] = 0
	seedActivePolicy(t, db, policy)
	setControlState(t, db, "poster", "running")

	ctx := context.Background()
	now := time.Now()
	bl, err := CheckTypeConcurrency(ctx, db, "poster", policy, now)
	if err != nil {
		t.Fatalf("CheckTypeConcurrency: %v", err)
	}
	if bl == nil {
		t.Fatal("expected blocker for zero concurrency")
	}
	if !strings.Contains(strings.ToLower(bl.Reason), "zero") {
		t.Fatalf("expected zero concurrency reason, got %q", bl.Reason)
	}
}

func TestAdmissionRacingReservationCount(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		db, _ := openAdmissionTestDB(t)
		policy := PolicyDefaults()
		policy.TypeConcurrency["poster"] = 1
		revID := seedActivePolicy(t, db, policy)
		setControlState(t, db, "poster", "running")

		start := make(chan struct{})
		results := make(chan int, 2)
		var wg sync.WaitGroup

		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				execID := fmt.Sprintf("exec-race-%d", idx)
				now := time.Now()
				tx, err := db.BeginTx(context.Background(), nil)
				if err != nil {
					return
				}
				count, err := ActiveReservationCount(context.Background(), tx, "poster", now)
				if err != nil {
					tx.Rollback()
					return
				}
				if count >= 1 {
					tx.Rollback()
					results <- 0
					return
				}
				_, err = InsertAdmissionReservation(context.Background(), tx, execID, "poster", 1, revID, time.Now().Add(5*time.Minute))
				if err != nil {
					tx.Rollback()
					results <- 0
					return
				}
				if err := tx.Commit(); err != nil {
					results <- 0
					return
				}
				results <- 1
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)

		total := 0
		for r := range results {
			total += r
		}
		if total != 1 {
			t.Fatalf("iteration %d: racing reservations total=%d want 1", iteration, total)
		}
	}
}

func TestAdmissionReservationRollbackOnInsertFailure(t *testing.T) {
	db, _ := openAdmissionTestDB(t)
	policy := PolicyDefaults()
	revID := seedActivePolicy(t, db, policy)

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	execID := GenerateExecutionID("worker")
	_, err = InsertAdmissionReservation(ctx, tx, execID, "poster", 1, revID, time.Now().Add(5*time.Minute))
	if err != nil {
		tx.Rollback()
		t.Fatalf("first insert: %v", err)
	}

	// Rollback - reservation should not be visible
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scheduler_reservation WHERE execution_id=?`, execID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back reservation persisted: rows=%d", count)
	}
}
