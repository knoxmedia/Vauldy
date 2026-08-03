package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

func newTestService(t *testing.T, policy Policy) (*Service, *sql.DB) {
	t.Helper()
	db, _ := openAdmissionTestDB(t)
	seedActivePolicy(t, db, policy)
	svc := NewService(db)
	svc.SetPolicy(policy)
	return svc, db
}

func insertServiceReservation(t *testing.T, db *sql.DB, executionID, taskType string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,?,1,(SELECT id FROM scheduler_policy_revision WHERE is_active=1),'active',?)`,
		executionID, taskType, time.Now().Add(90*time.Second))
	if err != nil {
		t.Fatalf("insert reservation: %v", err)
	}
}

// activateNextRevision activates a new policy revision on top of the current
// active revision (lowering or raising limits) and returns its id.
func activateNextRevision(t *testing.T, db *sql.DB, policy Policy) int64 {
	t.Helper()
	ctx := context.Background()
	s := NewStore(db)
	current, err := s.GetActivePolicyRevision(ctx)
	var expected int64 = -1
	var parent *int64
	if err == nil && current != nil {
		expected = current.ID
		parent = &current.ID
	}
	policyJSON := fmt.Sprintf(`{"type_concurrency":%s,"resource_capacity":%s,"provider_capacity":%s,"aging_interval_sec":%d,"aging_step":%d,"run_now_amount":%d,"run_now_ttl_sec":%d}`,
		mapToJSON(policy.TypeConcurrency), resourceMapToJSON(policy.ResourceCapacity), mapToJSON(policy.ProviderCapacity),
		policy.AgingIntervalSec, policy.AgingStep, policy.RunNowAmount, policy.RunNowTTLSec)
	rev, err := s.CreatePolicyRevision(ctx, 1, parent, policyJSON, "test", "service revision", "hash")
	if err != nil {
		t.Fatalf("create policy revision: %v", err)
	}
	if err := s.ActivatePolicyRevision(ctx, rev.ID, expected); err != nil {
		t.Fatalf("activate policy revision: %v", err)
	}
	return rev.ID
}

func waitForServiceCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestDispatcherScheduler_ClaimReturnsAdmittedReservation(t *testing.T) {
	policy := PolicyDefaults()
	svc, db := newTestService(t, policy)
	_ = db

	invoked := make(chan struct{})
	var once sync.Once
	svc.SetClaimer(func(ctx context.Context, owner string, taskTypes []string) (*ClaimResult, error) {
		once.Do(func() { close(invoked) })
		return &ClaimResult{
			ExecutionID: "owner/exec-1",
			TaskType:    "poster",
			Owner:       owner,
			QueueID:     42,
			MediaID:     7,
			LeaseUntil:  time.Now().Add(time.Minute),
		}, nil
	})
	res, err := svc.Claim(context.Background(), "owner", []string{"poster"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.ExecutionID != "owner/exec-1" || res.TaskType != "poster" || res.QueueID != 42 || res.MediaID != 7 {
		t.Fatalf("claim result=%+v", res)
	}
	select {
	case <-invoked:
	default:
		t.Fatal("claimer was not invoked")
	}

	svc.SetClaimer(func(context.Context, string, []string) (*ClaimResult, error) { return nil, nil })
	res, err = svc.Claim(context.Background(), "owner", nil)
	if err != nil || res != nil {
		t.Fatalf("declined claim res=%v err=%v", res, err)
	}

	svc.SetClaimer(nil)
	if _, err := svc.Claim(context.Background(), "owner", nil); err == nil {
		t.Fatal("Claim without claimer should error")
	}
}

func TestDispatcherScheduler_BudgetSnapshotEqualsDurableReservations(t *testing.T) {
	policy := PolicyDefaults()
	svc, db := newTestService(t, policy)
	ctx := context.Background()
	s := NewStore(db)

	insertServiceReservation(t, db, "owner/poster-1", "poster")
	insertServiceReservation(t, db, "owner/poster-2", "poster")
	insertServiceReservation(t, db, "owner/preview-1", "preview")
	insertServiceReservation(t, db, "owner/subtitle-1", "subtitle")

	active, err := s.ListActiveReservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := svc.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.GlobalUsed != len(active) {
		t.Fatalf("GlobalUsed=%d want %d durable reservations", snap.GlobalUsed, len(active))
	}
	if snap.PosterUsed != 2 || snap.PreviewUsed != 1 || snap.SubtitleUsed != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}
	if snap.GlobalLimit <= 0 || snap.PosterLimit <= 0 || snap.PreviewLimit <= 0 || snap.SubtitleLimit <= 0 {
		t.Fatalf("limits must be positive from policy: %+v", snap)
	}

	if err := s.ReleaseReservation(ctx, "owner/poster-1", "complete", "owner"); err != nil {
		t.Fatal(err)
	}
	snap, err = svc.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.PosterUsed != 1 || snap.GlobalUsed != 3 {
		t.Fatalf("after release snapshot=%+v", snap)
	}
}

func TestDispatcherScheduler_PolicyLoweringDoesNotCancelRunningTasks(t *testing.T) {
	policy := PolicyDefaults()
	policy.TypeConcurrency["poster"] = 2
	svc, db := newTestService(t, policy)
	ctx := context.Background()
	s := NewStore(db)

	insertServiceReservation(t, db, "owner/run-1", "poster")
	insertServiceReservation(t, db, "owner/run-2", "poster")

	lowered := PolicyDefaults()
	lowered.TypeConcurrency["poster"] = 1
	activateNextRevision(t, db, lowered)
	if err := svc.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.policy.TypeConcurrency["poster"] != 1 {
		t.Fatalf("reloaded poster limit=%d want 1", svc.policy.TypeConcurrency["poster"])
	}

	// Running reservations are untouched by the policy lowering.
	active, err := s.ListActiveReservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("active reservations=%d want 2 preserved", len(active))
	}

	// New admission for poster is now blocked (2 active >= limit 1).
	count, err := ActiveReservationCount(ctx, db, "poster", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("poster active count=%d want 2", count)
	}
	blocker, err := CheckTypeConcurrency(ctx, db, "poster", svc.policy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if blocker == nil {
		t.Fatal("poster admission should be blocked after lowering")
	}
}

func TestDispatcherScheduler_FallbackReadmissionReleasesGPUAndWaitsForCPU(t *testing.T) {
	policy := PolicyDefaults()
	policy.TypeConcurrency["subtitle_recognize"] = 2
	policy.ResourceCapacity[CPU] = 1
	svc, db := newTestService(t, policy)
	ctx := context.Background()
	s := NewStore(db)

	gpuExec := "owner/gpu-attempt"
	insertServiceReservation(t, db, gpuExec, "subtitle_recognize") // holds the only CPU token per registry
	insertServiceReservation(t, db, "owner/cpu-blocker", "atrack") // keeps CPU full after the GPU attempt is released

	done := make(chan *Reservation, 1)
	fail := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		res, err := svc.AcquireFallback(ctx, FallbackRequest{
			ExecutionID: gpuExec,
			TaskType:    "subtitle_recognize",
			Owner:       "owner",
			Resources:   ResourceRequest{CPU: 1},
		})
		if err != nil {
			fail <- err
			return
		}
		done <- res
	}()
	<-started

	// The GPU attempt is fenced (released) immediately.
	waitForServiceCondition(t, time.Second, func() bool {
		gpuRes, err := s.GetReservation(ctx, gpuExec)
		return err == nil && gpuRes != nil && gpuRes.Status == "released"
	})

	// The fallback is not admitted yet: the blocker still holds the CPU token.
	select {
	case res := <-done:
		t.Fatalf("fallback admitted early: %+v", res)
	case err := <-fail:
		t.Fatalf("fallback failed early: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	// Free the CPU token; the fresh CPU-heavy fallback is admitted.
	if err := s.ReleaseReservation(ctx, "owner/cpu-blocker", "complete", "owner"); err != nil {
		t.Fatal(err)
	}
	var fresh *Reservation
	select {
	case fresh = <-done:
	case err := <-fail:
		t.Fatalf("fallback error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("fallback not admitted after CPU freed")
	}
	if fresh == nil {
		t.Fatal("fallback returned nil reservation")
	}
	if fresh.ExecutionID == gpuExec || fresh.Status != "active" || fresh.TaskType != "subtitle_recognize" {
		t.Fatalf("fresh reservation=%+v", fresh)
	}
}

func TestDispatcherScheduler_FallbackRejectsInvalidRequest(t *testing.T) {
	policy := PolicyDefaults()
	svc, _ := newTestService(t, policy)
	ctx := context.Background()

	if _, err := svc.AcquireFallback(ctx, FallbackRequest{}); err == nil {
		t.Fatal("empty fallback request should error")
	}
	if _, err := svc.AcquireFallback(ctx, FallbackRequest{
		ExecutionID: "owner/x",
		TaskType:    "subtitle_recognize",
		Owner:       "owner",
		Resources:   ResourceRequest{ResourceKind("bogus"): 1},
	}); err == nil {
		t.Fatal("invalid resource kind should error")
	}
}
