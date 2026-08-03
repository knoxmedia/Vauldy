package scheduler

import (
	"context"
	"database/sql"
	"errors"
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

// ---------------------------------------------------------------------------
// Runtime override and control-plane tests (Task 9)
// ---------------------------------------------------------------------------

// newRuntimeOverrideService seeds an active policy revision and a service that
// treats base as the YAML-effective policy layer (defaults + YAML). It returns
// the service, database, and the active revision id used as expected_revision.
func newRuntimeOverrideService(t *testing.T, base Policy) (*Service, *sql.DB, int64) {
	t.Helper()
	db, _ := openAdmissionTestDB(t)
	revID := seedActivePolicy(t, db, base)
	svc := NewService(db)
	svc.SetBasePolicy(base)
	svc.SetPolicy(base)
	return svc, db, revID
}

func TestSchedulerServiceRuntimeOverrideActivatesNewRevision(t *testing.T) {
	base := PolicyDefaults()
	base.TypeConcurrency["poster"] = 5 // simulates a YAML override
	svc, db, revID := newRuntimeOverrideService(t, base)
	ctx := context.Background()

	res, err := svc.ApplyRuntimeOverride(ctx, RuntimeOverrideRequest{
		ExpectedRevision: revID,
		Concurrency:      map[string]int{"poster": 2},
		Author:           "admin",
		Reason:           "reduce poster load",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RevisionID == 0 {
		t.Fatal("no new revision id returned")
	}
	if res.Policy.TypeConcurrency["poster"] != 2 {
		t.Fatalf("effective poster=%d want 2", res.Policy.TypeConcurrency["poster"])
	}
	if res.Policy.Provenance["concurrency.poster"] != "override" {
		t.Fatalf("provenance=%q want override", res.Policy.Provenance["concurrency.poster"])
	}
	// In-memory effective policy is updated synchronously (durable activation,
	// not merely accepted work).
	if got := svc.CurrentPolicy().TypeConcurrency["poster"]; got != 2 {
		t.Fatalf("service effective poster=%d want 2", got)
	}
	// Durable activation: the active DB revision is the new one.
	st := NewStore(db)
	active, err := st.GetActivePolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != res.RevisionID {
		t.Fatalf("active revision=%d want %d", active.ID, res.RevisionID)
	}
	// Audit recorded with actor/reason.
	entries, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("audit entries=%d want 1", len(entries))
	}
	if entries[0].Actor != "admin" {
		t.Fatalf("audit actor=%q want admin", entries[0].Actor)
	}
}

func TestSchedulerServicePolicyRevisionConflict(t *testing.T) {
	base := PolicyDefaults()
	svc, db, revID := newRuntimeOverrideService(t, base)
	ctx := context.Background()

	res, err := svc.ApplyRuntimeOverride(ctx, RuntimeOverrideRequest{
		ExpectedRevision: revID,
		Concurrency:      map[string]int{"poster": 2},
		Author:           "admin",
		Reason:           "first",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.ApplyRuntimeOverride(ctx, RuntimeOverrideRequest{
		ExpectedRevision: revID, // stale
		Concurrency:      map[string]int{"poster": 3},
		Author:           "admin",
		Reason:           "stale",
	})
	var conflict RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err=%v want RevisionConflictError", err)
	}
	if conflict.Current != res.RevisionID {
		t.Fatalf("conflict current=%d want %d", conflict.Current, res.RevisionID)
	}
	// No new revision created and no audit written on conflict.
	st := NewStore(db)
	revs, err := st.ListPolicyRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 {
		t.Fatalf("policy revisions=%d want 2", len(revs))
	}
	entries, _ := st.ListAudit(ctx, 10)
	if len(entries) != 1 {
		t.Fatalf("audit entries=%d want 1 (no audit on conflict)", len(entries))
	}
}

func TestSchedulerServiceRuntimeOverrideInvalidUpdateNoActivation(t *testing.T) {
	base := PolicyDefaults()
	svc, db, revID := newRuntimeOverrideService(t, base)
	ctx := context.Background()
	st := NewStore(db)
	before, err := st.GetActivePolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.ApplyRuntimeOverride(ctx, RuntimeOverrideRequest{
		ExpectedRevision: revID,
		Concurrency:      map[string]int{"poster": -1},
		Author:           "admin",
		Reason:           "bad update",
	})
	var ve ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err=%v want ValidationError", err)
	}
	if len(ve.Errors) == 0 {
		t.Fatal("validation errors must be populated")
	}
	// Active revision is unchanged.
	after, err := st.GetActivePolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID {
		t.Fatalf("active revision changed from %d to %d", before.ID, after.ID)
	}
	// No audit on invalid update.
	entries, _ := st.ListAudit(ctx, 10)
	if len(entries) != 0 {
		t.Fatalf("audit entries=%d want 0 (no audit on invalid update)", len(entries))
	}
	// Effective policy still reflects the base.
	if got := svc.CurrentPolicy().TypeConcurrency["poster"]; got != 3 {
		t.Fatalf("effective poster=%d want 3", got)
	}
}

func TestSchedulerServiceRuntimeOverrideRestartPersistence(t *testing.T) {
	base := PolicyDefaults()
	base.TypeConcurrency["poster"] = 5
	svc, db, revID := newRuntimeOverrideService(t, base)
	ctx := context.Background()
	if _, err := svc.ApplyRuntimeOverride(ctx, RuntimeOverrideRequest{
		ExpectedRevision: revID,
		Concurrency:      map[string]int{"poster": 1},
		Author:           "admin",
		Reason:           "persist across restart",
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate restart: a fresh service on the same database reloads the
	// committed revision.
	fresh := NewService(db)
	fresh.SetBasePolicy(base)
	if err := fresh.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	p := fresh.CurrentPolicy()
	if p.TypeConcurrency["poster"] != 1 {
		t.Fatalf("poster=%d want 1 after restart", p.TypeConcurrency["poster"])
	}
	if p.Provenance["concurrency.poster"] != "override" {
		t.Fatalf("provenance=%q want override after restart", p.Provenance["concurrency.poster"])
	}
}

func TestSchedulerServiceRuntimeOverrideYAMLHiddenThenCleared(t *testing.T) {
	base := PolicyDefaults()
	base.MergeYAML(SchedulerYAMLConfig{TypeConcurrency: map[string]int{"poster": 5}})
	svc, db, revID := newRuntimeOverrideService(t, base)
	ctx := context.Background()

	res, err := svc.ApplyRuntimeOverride(ctx, RuntimeOverrideRequest{
		ExpectedRevision: revID,
		Concurrency:      map[string]int{"poster": 2},
		Author:           "admin",
		Reason:           "override",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Policy.TypeConcurrency["poster"] != 2 {
		t.Fatalf("poster=%d want 2", res.Policy.TypeConcurrency["poster"])
	}

	// A later YAML change (config.yml edited) must be hidden by the DB override
	// after restart.
	changedBase := PolicyDefaults()
	changedBase.MergeYAML(SchedulerYAMLConfig{TypeConcurrency: map[string]int{"poster": 9}})
	fresh := NewService(db)
	fresh.SetBasePolicy(changedBase)
	if err := fresh.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fresh.CurrentPolicy().TypeConcurrency["poster"]; got != 2 {
		t.Fatalf("poster=%d want 2 (DB override hides YAML change)", got)
	}

	// Clearing the override falls back to the current YAML value.
	cleared, err := fresh.ApplyRuntimeOverride(ctx, RuntimeOverrideRequest{
		ExpectedRevision: res.RevisionID,
		ClearConcurrency: []string{"poster"},
		Author:           "admin",
		Reason:           "clear",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Policy.TypeConcurrency["poster"] != 9 {
		t.Fatalf("poster=%d want 9 (cleared override falls back to YAML)", cleared.Policy.TypeConcurrency["poster"])
	}
	if cleared.Policy.Provenance["concurrency.poster"] != "yaml" {
		t.Fatalf("provenance=%q want yaml", cleared.Policy.Provenance["concurrency.poster"])
	}
}

func TestSchedulerServiceRuntimeOverrideLoweringPreservesRunningReservations(t *testing.T) {
	base := PolicyDefaults()
	svc, db, revID := newRuntimeOverrideService(t, base)
	ctx := context.Background()
	insertServiceReservation(t, db, "owner/run-1", "poster")
	insertServiceReservation(t, db, "owner/run-2", "poster")

	if _, err := svc.ApplyRuntimeOverride(ctx, RuntimeOverrideRequest{
		ExpectedRevision: revID,
		Concurrency:      map[string]int{"poster": 1},
		Author:           "admin",
		Reason:           "lower poster limit",
	}); err != nil {
		t.Fatal(err)
	}

	// Running reservations are preserved by the lowering.
	st := NewStore(db)
	active, err := st.ListActiveReservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("active reservations=%d want 2 preserved", len(active))
	}

	// New admission for poster is now blocked (2 active >= limit 1).
	blocker, err := CheckTypeConcurrency(ctx, db, "poster", svc.CurrentPolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if blocker == nil {
		t.Fatal("poster admission should be blocked after lowering limit")
	}
}

func TestSchedulerServiceControlPause(t *testing.T) {
	base := PolicyDefaults()
	svc, db, _ := newRuntimeOverrideService(t, base)
	ctx := context.Background()

	res, err := svc.Control(ctx, "poster", "pause", -1, "admin", "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != "paused" {
		t.Fatalf("state=%q want paused", res.State)
	}
	if res.Actor != "admin" || res.Reason != "maintenance" {
		t.Fatalf("result=%+v", res)
	}
	// Paused admits no new work.
	blockers, err := CheckAllBudgets(ctx, db, "poster", svc.CurrentPolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) == 0 {
		t.Fatal("paused type must block admission")
	}
	// Repeated pause is idempotent and does not bump the control revision.
	again, err := svc.Control(ctx, "poster", "pause", res.Revision, "admin", "again")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != "paused" || again.Revision != res.Revision {
		t.Fatalf("repeat=%+v want state=paused revision=%d", again, res.Revision)
	}
}

func TestSchedulerServiceControlResume(t *testing.T) {
	base := PolicyDefaults()
	svc, db, _ := newRuntimeOverrideService(t, base)
	ctx := context.Background()
	if _, err := svc.Control(ctx, "poster", "pause", -1, "admin", "pause"); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Control(ctx, "poster", "resume", -1, "admin", "resume")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != "running" {
		t.Fatalf("state=%q want running", res.State)
	}
	blockers, err := CheckAllBudgets(ctx, db, "poster", svc.CurrentPolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) != 0 {
		t.Fatalf("resumed type should admit, blockers=%v", blockers)
	}
	// Repeated resume is idempotent.
	again, err := svc.Control(ctx, "poster", "resume", res.Revision, "admin", "again")
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != res.Revision {
		t.Fatalf("idempotent resume changed revision %d -> %d", res.Revision, again.Revision)
	}
}

func TestSchedulerServiceControlDrainConverges(t *testing.T) {
	base := PolicyDefaults()
	svc, db, _ := newRuntimeOverrideService(t, base)
	ctx := context.Background()
	insertServiceReservation(t, db, "owner/run-1", "poster")

	res, err := svc.Control(ctx, "poster", "drain", -1, "admin", "drain")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != "draining" {
		t.Fatalf("state=%q want draining", res.State)
	}
	if res.LiveReservations != 1 {
		t.Fatalf("live_reservations=%d want 1", res.LiveReservations)
	}
	if res.Drained {
		t.Fatal("drained must be false while live reservations remain")
	}
	// Draining admits no new work.
	blockers, err := CheckAllBudgets(ctx, db, "poster", svc.CurrentPolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) == 0 {
		t.Fatal("draining type must block admission")
	}

	// Release the running reservation; repeated drain converges to paused.
	st := NewStore(db)
	if err := st.ReleaseReservation(ctx, "owner/run-1", "complete", "owner"); err != nil {
		t.Fatal(err)
	}
	conv, err := svc.Control(ctx, "poster", "drain", res.Revision, "admin", "converge")
	if err != nil {
		t.Fatal(err)
	}
	if conv.State != "paused" {
		t.Fatalf("state=%q want paused after convergence", conv.State)
	}
	if conv.LiveReservations != 0 {
		t.Fatalf("live_reservations=%d want 0", conv.LiveReservations)
	}
	if !conv.Drained {
		t.Fatal("drained must be true when converged")
	}
}

func TestSchedulerServiceControlDrainDurableAcrossRestart(t *testing.T) {
	base := PolicyDefaults()
	svc, db, _ := newRuntimeOverrideService(t, base)
	ctx := context.Background()
	insertServiceReservation(t, db, "owner/run-1", "poster")
	if _, err := svc.Control(ctx, "poster", "drain", -1, "admin", "drain"); err != nil {
		t.Fatal(err)
	}

	// Restart: admission still reads the durable control state from the DB.
	fresh := NewService(db)
	fresh.SetBasePolicy(base)
	fresh.SetPolicy(base)
	blockers, err := CheckAllBudgets(ctx, db, "poster", fresh.CurrentPolicy(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) == 0 {
		t.Fatal("drain must survive restart and keep blocking admission")
	}
	st := NewStore(db)
	cs, err := st.GetControlState(ctx, "poster")
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != "draining" {
		t.Fatalf("control state=%q want draining", cs.State)
	}
}

func TestSchedulerServiceControlRevisionConflict(t *testing.T) {
	base := PolicyDefaults()
	svc, _, _ := newRuntimeOverrideService(t, base)
	ctx := context.Background()
	res, err := svc.Control(ctx, "poster", "pause", -1, "admin", "pause")
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.Control(ctx, "poster", "resume", res.Revision+5, "admin", "stale")
	var cc ControlConflictError
	if !errors.As(err, &cc) {
		t.Fatalf("err=%v want ControlConflictError", err)
	}
	if cc.Current != res.Revision {
		t.Fatalf("conflict current=%d want %d", cc.Current, res.Revision)
	}
}
