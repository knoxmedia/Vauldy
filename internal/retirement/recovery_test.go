package retirement

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/publication"
)

func TestRecoveryInterruptedQuarantiningBeforeMove(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 1)
	qPath, err := QuarantinePath(fx.QuarantineRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET state='quarantining',attempts=1,quarantine_path=?,lease_owner='dead' WHERE id=?`, qPath, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateRetryableFailed) {
		t.Fatalf("state=%s", state)
	}
	if _, err = os.Stat(fx.SourcePath); err != nil {
		t.Fatal(err)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestRecoveryInterruptedQuarantiningAfterMove(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 1)
	qPath, qFP, err := MoveToQuarantine(fx.SourcePath, fx.QuarantineRoot, id, FileOps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET state='quarantining',attempts=1,quarantine_path=?,quarantine_fingerprint=?,lease_owner='dead' WHERE id=?`, qPath, qFP, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}
	if _, err = os.Stat(fx.SourcePath); !os.IsNotExist(err) {
		t.Fatal("source should be gone")
	}
	if _, err = os.Stat(qPath); !os.IsNotExist(err) {
		t.Fatal("quarantine should be gone")
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestRecoveryInterruptedQuarantinedAndDeleting(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 2)
	qPath, qFP, err := MoveToQuarantine(fx.SourcePath, fx.QuarantineRoot, id, FileOps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET state='quarantined',attempts=2,quarantine_path=?,quarantine_fingerprint=? WHERE id=?`, qPath, qFP, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}

	// Idempotent second pass.
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ = retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}
}

func TestRecoveryDeletingPartial(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 1)
	qPath, qFP, err := MoveToQuarantine(fx.SourcePath, fx.QuarantineRoot, id, FileOps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET state='deleting',attempts=1,quarantine_path=?,quarantine_fingerprint=? WHERE id=?`, qPath, qFP, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestRecoveryIdempotentStableStates(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if err := ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) {
		t.Fatalf("state=%s", state)
	}
}

func TestLifecycleFinalizeWiresRealBarrierHook(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = publication.FinalizeNodeTransitionTx(context.Background(), tx, fx.RunID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateReady) || blocker != "" {
		t.Fatalf("lifecycle finalize barrier state=%s blocker=%s", state, blocker)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestRecoveryUnsafeQuarantinePathNeverDeletesWeakMatch(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 1)
	// Craft a path outside the configured quarantine root that still contains identity substrings.
	evilDir := filepath.Join(fx.Root, "not-quarantine", fmt.Sprintf("%d", fx.MediaID), "1",
		fmt.Sprintf("r%d", fx.RetirementID), "rr0", "a1")
	evilPath := filepath.Join(evilDir, "source")
	writeFile(t, evilPath, []byte("do-not-delete"))
	if _, err := db.Exec(`UPDATE media_plaintext_retirement SET state='deleting',attempts=1,quarantine_path=?,quarantine_fingerprint='' WHERE id=?`, evilPath, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	removed := false
	ops := FileOps{
		Remove: func(path string) error {
			removed = true
			return os.Remove(path)
		},
		Rename: os.Rename,
	}
	err := ReconcileStartup(context.Background(), db, RecoveryOptions{
		QuarantineRoot: fx.QuarantineRoot,
		FileOps:        ops,
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("must not delete on ErrUnsafeQuarantinePath via weak substring match")
	}
	if _, err = os.Stat(evilPath); err != nil {
		t.Fatal("evil path must remain intact")
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateOperatorRequired) {
		t.Fatalf("unsafe path must fail closed to operator_required, got %s", state)
	}
	_ = id
}

func TestRecoveryAmbiguousBothPresentRequiresFingerprint(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 1)
	qPath, err := QuarantinePath(fx.QuarantineRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, qPath, []byte("different-quarantine-bytes"))
	// Partial FileOps with Rename but nil Remove must not panic.
	ops := FileOps{Rename: os.Rename}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET state='quarantining',attempts=1,quarantine_path=?,quarantine_fingerprint='' WHERE id=?`, qPath, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{
		QuarantineRoot: fx.QuarantineRoot,
		FileOps:        ops,
	}); err != nil {
		t.Fatal(err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateOperatorRequired) {
		t.Fatalf("ambiguous both-present without safe fingerprint compare must be operator_required, got %s", state)
	}
	if _, err = os.Stat(fx.SourcePath); err != nil {
		t.Fatal("source must remain")
	}
	if _, err = os.Stat(qPath); err != nil {
		t.Fatal("quarantine must remain when unsafe")
	}
}

func TestRecoveryPostDeletePreVerifyRetryableToVerified(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 1)
	qPath, err := QuarantinePath(fx.QuarantineRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(fx.SourcePath)
	qFP := "sha256:post-delete-evidence"
	if _, err = db.Exec(`UPDATE media_plaintext_retirement
SET state='retryable_failed', attempts=2, quarantine_path=?, quarantine_fingerprint=?,
    last_error='verify failed after delete', next_retry_at=datetime(CURRENT_TIMESTAMP,'+60 seconds')
WHERE id=?`, qPath, qFP, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("startup must finish post-delete/pre-verify retryable_failed to verified, got %s", state)
	}
	encryptStillDone(t, db, fx.TaskID)

	// Idempotent second pass.
	if err = ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ = retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}
}

func TestRecoveryPostDeletePreVerifyFailsClosedWithoutEvidence(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	_ = os.Remove(fx.SourcePath)
	if _, err := db.Exec(`UPDATE media_plaintext_retirement
SET state='retryable_failed', attempts=1, quarantine_path='', quarantine_fingerprint='',
    last_error='verify failed'
WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileStartup(context.Background(), db, RecoveryOptions{QuarantineRoot: fx.QuarantineRoot}); err != nil {
		t.Fatal(err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state == string(StateVerified) {
		t.Fatal("must not verify without quarantine identity evidence")
	}
	encryptStillDone(t, db, fx.TaskID)
}
