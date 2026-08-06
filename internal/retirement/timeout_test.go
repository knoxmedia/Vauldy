package retirement

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestWorkerExecuteTimeoutRegressesToRetryable verifies a slow fingerprint does
// not stall the whole worker: the execution window expires and the row regresses
// to retryable_failed (not blocked) so other due rows can proceed.
func TestWorkerExecuteTimeoutRegressesToRetryable(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	w := newWorker(fx, CrashSeams{
		ExecuteTimeout: 100 * time.Millisecond,
		Fingerprint: func(ctx context.Context, path string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = w.Execute(context.Background(), *row)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateRetryableFailed) {
		t.Fatalf("state=%s want retryable_failed", state)
	}
	if blocker != "" {
		t.Fatalf("blocker=%s want empty after timeout", blocker)
	}
	if _, err = os.Stat(fx.SourcePath); err != nil {
		t.Fatal("source must remain intact after barrier timeout")
	}
	encryptStillDone(t, db, fx.TaskID)
}

// TestRecoverySkipsActiveLease verifies the reconciler never touches a row owned
// by a live worker lease, and does reconcile once that lease expires.
func TestRecoverySkipsActiveLease(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 1)
	qPath, qFP, err := MoveToQuarantine(context.Background(), fx.SourcePath, fx.QuarantineRoot, id, FileOps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET state='quarantined',attempts=1,quarantine_path=?,quarantine_fingerprint=?,lease_owner='live-worker',lease_until=datetime(CURRENT_TIMESTAMP,'+300 seconds') WHERE id=?`, qPath, qFP, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateQuarantined) {
		t.Fatalf("reconciler must skip active lease, state=%s", state)
	}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET lease_until=datetime(CURRENT_TIMESTAMP,'-5 seconds') WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ = retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("expired-lease row must reconcile, state=%s", state)
	}
}

// TestRecoveryTimeoutLeavesRowForLaterPass verifies an interrupted quarantining
// row whose fingerprint hash exceeds the pass window is left untouched (not
// fail-closed to operator_required) and surfaces the deadline error.
func TestRecoveryTimeoutLeavesRowForLaterPass(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 1)
	qPath, _, err := MoveToQuarantine(context.Background(), fx.SourcePath, fx.QuarantineRoot, id, FileOps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET state='quarantining',attempts=1,quarantine_path=?,quarantine_fingerprint='' WHERE id=?`, qPath, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	orig := loadFingerprint
	loadFingerprint = func(ctx context.Context, path string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	t.Cleanup(func() { loadFingerprint = orig })

	rctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = ReconcileStartup(rctx, db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline exceeded", err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateQuarantining) {
		t.Fatalf("row must remain quarantining after timeout, state=%s", state)
	}
}
