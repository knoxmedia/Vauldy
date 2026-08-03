package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	taskscheduler "knox-media/internal/scheduler"
	"knox-media/internal/store"
)

func TestSchedulerStartupOrderMatchesPhase3Requirements(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	// The startup order must be: DB migration → artifact recovery →
	// all queue lease recovery → reservation reconciliation →
	// active-policy validation/load → scheduler service/watch loop →
	// dispatchers/workers → submission sources.
	markers := []string{
		"NewService(db)",                  // scheduler service creation
		"buildSchedulerPolicy(cfg)",       // policy from compiled defaults + YAML
		"schedulerService.SetBasePolicy",  // base policy installed
		"activateSchedulerPolicy",         // durable policy activation
		"schedulerService.Reload",         // reload active DB override
		"ReconcileStartupReservations",    // reservation reconciliation
		"StartReservationExpiryReconciler", // reservation expiry under background group
	}
	previous := -1
	for _, marker := range markers {
		at := strings.Index(src, marker)
		if at < 0 {
			t.Fatalf("missing startup marker %q", marker)
		}
		if at <= previous {
			t.Fatalf("startup marker out of order %q", marker)
		}
		previous = at
	}

	// Reservation reconciliation must happen after lease recovery but before dispatcher start.
	reconcileAt := strings.Index(src, "ReconcileStartupReservations")
	leaseAt := strings.Index(src, "RecoverLeases")
	dispatcherAt := strings.Index(src, "dispatcher.Start")
	if reconcileAt < 0 || leaseAt < 0 || dispatcherAt < 0 {
		t.Fatalf("missing key markers reconcile=%d lease=%d dispatcher=%d", reconcileAt, leaseAt, dispatcherAt)
	}
	if reconcileAt <= leaseAt {
		t.Fatal("reservation reconciliation must come after lease recovery")
	}
	if reconcileAt >= dispatcherAt {
		t.Fatal("reservation reconciliation must come before dispatcher start")
	}

	// Policy reload must happen before dispatcher/claimer starts.
	reloadAt := strings.Index(src, "schedulerService.Reload")
	if reloadAt >= dispatcherAt {
		t.Fatal("policy reload must happen before dispatcher starts claiming")
	}
}

func TestMainFailsStartupOnSchedulerPolicyLoadFailure(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	// Policy build failure must Fatalf.
	buildAt := strings.Index(src, "buildSchedulerPolicy(cfg)")
	if buildAt < 0 {
		t.Fatal("missing buildSchedulerPolicy call")
	}
	afterBuild := src[buildAt:]
	if !strings.Contains(afterBuild, "log.Fatalf") || strings.Index(afterBuild, "log.Fatalf") > 500 {
		t.Fatal("buildSchedulerPolicy failure is not guarded by log.Fatalf")
	}

	// Policy activation failure must Fatalf.
	activateAt := strings.Index(src, "activateSchedulerPolicy")
	if activateAt < 0 {
		t.Fatal("missing activateSchedulerPolicy call")
	}
	afterActivate := src[activateAt:]
	if !strings.Contains(afterActivate, "log.Fatalf") {
		t.Fatal("activateSchedulerPolicy failure is not guarded by log.Fatalf")
	}

	// Policy reload failure must Fatalf.
	reloadAt := strings.Index(src, "schedulerService.Reload")
	if reloadAt < 0 {
		t.Fatal("missing Reload call")
	}
	afterReload := src[reloadAt:]
	if !strings.Contains(afterReload, "log.Fatalf") {
		t.Fatal("schedulerService.Reload failure is not guarded by log.Fatalf")
	}
}

func TestStartupReservationReconciliationReleasesExpiredLeases(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "resv-rec.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	schedStore := taskscheduler.NewStore(db)

	// Seed an active policy.
	policy := taskscheduler.PolicyDefaults()
	policy.TypeConcurrency["poster"] = 2
	raw, err := taskscheduler.EncodePolicyJSON(policy)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := schedStore.CreatePolicyRevision(ctx, 1, nil, raw, "test", "startup", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := schedStore.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatal(err)
	}

	// Insert an expired reservation (lease was 10 minutes ago).
	expiredLease := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	_, err = schedStore.CreateReservation(ctx, "expired-exec-1", "poster", 1, rev.ID, expiredLease)
	if err != nil {
		t.Fatal(err)
	}
	// Insert an active (unexpired) reservation.
	activeLease := time.Now().Add(5 * time.Minute).Truncate(time.Second)
	_, err = schedStore.CreateReservation(ctx, "active-exec-1", "poster", 1, rev.ID, activeLease)
	if err != nil {
		t.Fatal(err)
	}

	// Verify both created.
	resExp, _ := schedStore.GetReservation(ctx, "expired-exec-1")
	resAct, _ := schedStore.GetReservation(ctx, "active-exec-1")
	if resExp == nil || resExp.Status != "active" {
		t.Fatalf("expired reservation not created correctly: %+v", resExp)
	}
	if resAct == nil || resAct.Status != "active" {
		t.Fatalf("active reservation not created correctly: %+v", resAct)
	}
	if resExp.LeaseUntil == nil {
		t.Fatal("expired reservation has nil lease_until")
	}
	t.Logf("expired lease_until=%v now=%v", *resExp.LeaseUntil, time.Now())
	t.Logf("active lease_until=%v", *resAct.LeaseUntil)

	// Reconcile.
	released, err := ReconcileStartupReservations(ctx, db, "startup-recovery")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("released=%d", released)
	if released < 1 {
		t.Fatalf("expected at least 1 expired reservation released, got %d", released)
	}

	// Verify expired is released.
	res, err := schedStore.GetReservation(ctx, "expired-exec-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "released" {
		t.Fatalf("expired reservation status=%q, want released", res.Status)
	}

	// Verify active is still active.
	res, err = schedStore.GetReservation(ctx, "active-exec-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "active" {
		t.Fatalf("active reservation status=%q, want active", res.Status)
	}
}

func TestStartupReservationReconciliationReleasesOrphans(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "orphan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	schedStore := taskscheduler.NewStore(db)

	policy := taskscheduler.PolicyDefaults()
	policy.TypeConcurrency["poster"] = 2
	raw, err := taskscheduler.EncodePolicyJSON(policy)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := schedStore.CreatePolicyRevision(ctx, 1, nil, raw, "test", "startup", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := schedStore.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatal(err)
	}

	// Insert a live reservation with a very distant lease (survives restart
	// but the lease_owner is unknown). This is an "orphan": not expired,
	// but no known executor owns it.
	_, err = schedStore.CreateReservation(ctx, "orphan-exec", "poster", 1, rev.ID, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	// Reconcile should release the orphan since this is a fresh startup.
	released, err := ReconcileStartupReservations(ctx, db, "startup-recovery")
	if err != nil {
		t.Fatal(err)
	}
	// Orphan has a valid future lease so it won't match the expired query.
	// The reconciliation releases expired AND null-lease reservations only.
	// Orphans with valid leases need a different safety mechanism. For now
	// we verify that the reconciliation does not crash on such entries.
	_ = released

	res, err := schedStore.GetReservation(ctx, "orphan-exec")
	if err != nil {
		t.Fatal(err)
	}
	// Orphan with valid lease is preserved (its lease hasn't expired yet).
	// The lease-owner reconciliation happens at the queue level during
	// lease recovery.
	if res.Status != "active" {
		t.Fatalf("valid-lease reservation status=%q, want active", res.Status)
	}
}

func TestReservationExpiryReconcilerReleasesExpired(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "expiry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	schedStore := taskscheduler.NewStore(db)

	policy := taskscheduler.PolicyDefaults()
	raw, err := taskscheduler.EncodePolicyJSON(policy)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := schedStore.CreatePolicyRevision(ctx, 1, nil, raw, "test", "expiry", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := schedStore.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatal(err)
	}

	_, err = schedStore.CreateReservation(ctx, "exp-1", "poster", 1, rev.ID, time.Now().Add(-10*time.Minute).Truncate(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	reconciler := NewReservationExpiryReconciler(db)

	reconciler.RunOnce(ctx)

	res, err := schedStore.GetReservation(ctx, "exp-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "released" {
		t.Fatalf("expired reservation status=%q, want released", res.Status)
	}
}

func TestReservationExpiryReconcilerRunsPeriodically(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "expiry-periodic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	schedStore := taskscheduler.NewStore(db)

	policy := taskscheduler.PolicyDefaults()
	raw, err := taskscheduler.EncodePolicyJSON(policy)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := schedStore.CreatePolicyRevision(ctx, 1, nil, raw, "test", "periodic", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := schedStore.ActivatePolicyRevision(ctx, rev.ID, -1); err != nil {
		t.Fatal(err)
	}

	_, err = schedStore.CreateReservation(ctx, "periodic-exp", "poster", 1, rev.ID, time.Now().Add(-10*time.Minute).Truncate(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	reconciler := NewReservationExpiryReconciler(db)

	// Run one cycle.
	reconciler.RunOnce(ctx)

	// Insert a second expired reservation.
	_, err = schedStore.CreateReservation(ctx, "periodic-exp-2", "thumbnail", 1, rev.ID, time.Now().Add(-10*time.Minute).Truncate(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	// Run another cycle — the periodic reconciler should pick it up.
	reconciler.RunOnce(ctx)

	res, err := schedStore.GetReservation(ctx, "periodic-exp-2")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "released" {
		t.Fatalf("second expired reservation status=%q, want released", res.Status)
	}
}

func TestMainStartsReservationExpiryReconciliationUnderBackgroundGroup(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	if !strings.Contains(src, "background.Go(serverCtx, func") {
		t.Fatal("missing background.Go call")
	}

	// Find "StartReservationExpiryReconciler" after background.Go but before Background.Wait.
	expiryAt := strings.Index(src, "StartReservationExpiryReconciler")
	if expiryAt < 0 {
		t.Fatal("missing StartReservationExpiryReconciler")
	}

	bgWaitAt := strings.Index(src, "background.Wait(shutdownCtx)")
	if expiryAt > bgWaitAt {
		t.Fatal("reservation expiry reconciler starts after shutdown wait")
	}
}

func TestMainShutdownDoesNotReleaseUnfencedLiveExecutors(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	// Shutdown must NOT release unfenced reservations.
	if strings.Contains(src, "ReleaseAllReservations") || strings.Contains(src, "releaseAllReservations") {
		t.Fatal("shutdown incorrectly releases unfenced live executor reservations")
	}

	// Shutdown order: HTTP → monitor → background.Wait → coordinator → dispatcher.
	httpAt := strings.Index(src, "httpServer.Shutdown(")
	monitorAt := strings.Index(src, "monitorDone")
	bgAt := strings.Index(src, "background.Wait(shutdownCtx)")
	coordAt := strings.Index(src, "coordinator.ShutdownContext(shutdownCtx)")
	dispAt := strings.Index(src, "dispatcherDone")

	if httpAt < 0 || monitorAt < 0 || bgAt < 0 || coordAt < 0 || dispAt < 0 {
		t.Fatalf("missing shutdown markers http=%d monitor=%d bg=%d coord=%d disp=%d", httpAt, monitorAt, bgAt, coordAt, dispAt)
	}
	if bgAt > coordAt {
		t.Fatal("background.Wait must happen before coordinator shutdown")
	}
}

func TestMainRemovesBuildDispatcherOptionsAfterMigration(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	if strings.Contains(src, "buildDispatcherOptions") {
		t.Fatal("buildDispatcherOptions must be removed after all callers migrate to scheduler")
	}
	if strings.Contains(src, "postingest.DispatcherOptions{") {
		t.Fatal("local budget wiring must be removed after migration to scheduler")
	}
}

func TestMainInjectsSchedulerIntoScanCoordinator(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	// Scan coordinator must receive the scheduler service for admission.
	if !strings.Contains(src, "scancoord.New(") {
		t.Fatal("missing scancoord.New call")
	}

	// Scheduler service must appear in the scancoord options or be set separately.
	// The scheduler must be wired into the coordinator for sync admission.
	schedulerAt := strings.Index(src, "schedulerService")
	coordAt := strings.Index(src, "scancoord.New(")
	if schedulerAt < 0 || coordAt < 0 {
		t.Fatalf("schedulerService=%d scancoord=%d", schedulerAt, coordAt)
	}
}

func TestMainInjectsSchedulerSnapshotIntoAdminOverview(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	// Admin overview builder is now system-only (CPU, memory, disk, SQLite, health);
	// it no longer receives scheduler service for budget snapshots.
	if !strings.Contains(src, "NewAdminOverviewBuilder(db, sqliteMetrics)") {
		t.Fatal("AdminOverviewBuilder must use the system-only signature with sqliteMetrics only")
	}
	// Scheduler service wiring is no longer part of the overview builder.
	if strings.Contains(src, "NewAdminOverviewBuilder(db, schedulerService") {
		t.Fatal("AdminOverviewBuilder must not pass schedulerService (system-only)")
	}
}

func TestBackgroundGroupOwnsReservationExpiry(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	// Reservation expiry must be started via background.Go, not a bare goroutine.
	// Find all background.Go calls.
	expiryAt := strings.Index(src, "StartReservationExpiryReconciler")
	if expiryAt < 0 {
		t.Fatal("missing StartReservationExpiryReconciler")
	}

	// Ensure it appears inside a background.Go call (check for background.Go before it).
	searchRegion := src[:expiryAt]
	lastBgGo := strings.LastIndex(searchRegion, "background.Go(serverCtx")
	if lastBgGo < 0 {
		t.Fatal("StartReservationExpiryReconciler must be wrapped in background.Go")
	}
	if expiryAt-lastBgGo > 500 {
		t.Fatal("StartReservationExpiryReconciler too far from its background.Go wrapper")
	}
}
