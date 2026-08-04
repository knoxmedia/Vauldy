package retirement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"knox-media/internal/storage"
)

func newWorker(fx *fixture, seams CrashSeams) *Worker {
	seams.QuarantineRoot = fx.QuarantineRoot
	if seams.MaxAttempts == 0 {
		seams.MaxAttempts = 3
	}
	return &Worker{DB: fx.DB, Owner: "retire-worker", Seams: seams}
}

func TestWorkerClaimExecuteVerified(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	w := newWorker(fx, CrashSeams{})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(fx.SourcePath); !os.IsNotExist(err) {
		t.Fatal("source should be deleted")
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestWorkerCrashBeforeMoveLeavesSource(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	boom := errors.New("crash before move")
	w := newWorker(fx, CrashSeams{BeforeMove: func() error { return boom }})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); !errors.Is(err, boom) {
		t.Fatalf("execute=%v", err)
	}
	if _, err = os.Stat(fx.SourcePath); err != nil {
		t.Fatal("source must remain")
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateRetryableFailed) {
		t.Fatalf("state=%s", state)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestWorkerCrashAfterMoveBeforeStateCommitRestores(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	boom := errors.New("crash before state commit")
	w := newWorker(fx, CrashSeams{BeforeStateCommit: func() error { return boom }})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); !errors.Is(err, boom) {
		t.Fatalf("execute=%v", err)
	}
	if _, err = os.Stat(fx.SourcePath); err != nil {
		t.Fatalf("source should be restored: %v", err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateRetryableFailed) {
		t.Fatalf("state=%s", state)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestWorkerCrashAfterStateCommitKeepsQuarantined(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	boom := errors.New("crash after state commit")
	w := newWorker(fx, CrashSeams{AfterStateCommit: func() error { return boom }})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); !errors.Is(err, boom) {
		t.Fatalf("execute=%v", err)
	}
	if _, err = os.Stat(fx.SourcePath); !os.IsNotExist(err) {
		t.Fatal("source should be quarantined")
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateRetryableFailed) {
		t.Fatalf("state=%s want retryable_failed for post-quarantine crash", state)
	}
	var qPath, qFP string
	if err = db.QueryRow(`SELECT quarantine_path,quarantine_fingerprint FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID).Scan(&qPath, &qFP); err != nil {
		t.Fatal(err)
	}
	if qPath == "" || qFP == "" {
		t.Fatal("quarantine identity must be preserved for resume")
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestWorkerCrashBeforeDeleteAndRecoveryContinues(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	boom := errors.New("crash before delete")
	w := newWorker(fx, CrashSeams{BeforeDelete: func() error { return boom }})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateRetryableFailed) {
		t.Fatalf("state=%s", state)
	}
	encryptStillDone(t, db, fx.TaskID)

	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET next_retry_at=datetime(CURRENT_TIMESTAMP,'-1 seconds') WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	w2 := newWorker(fx, CrashSeams{})
	row2, err := w2.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w2.Execute(context.Background(), *row2); err != nil {
		t.Fatal(err)
	}
	state, _ = retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}
}

func TestWorkerExhaustedRetryOperatorRequired(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	if _, err := db.Exec(`UPDATE media_plaintext_retirement SET attempts=2 WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("always fail")
	w := newWorker(fx, CrashSeams{BeforeMove: func() error { return boom }, MaxAttempts: 3})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row.Attempts != 3 {
		t.Fatalf("attempts=%d", row.Attempts)
	}
	if err = w.Execute(context.Background(), *row); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateOperatorRequired) {
		t.Fatalf("state=%s", state)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestWorkerRenewLease(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	w := newWorker(fx, CrashSeams{BeforeMove: func() error { return errors.New("stop") }})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.RenewLease(context.Background(), *row); err != nil {
		t.Fatal(err)
	}
	row.LeaseOwner = "other"
	if err = w.RenewLease(context.Background(), *row); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err=%v", err)
	}
}

func TestWorkerGenerationFenceOnExecute(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	w := newWorker(fx, CrashSeams{})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media SET ingest_generation=9 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); err == nil {
		t.Fatal("expected generation fence")
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestWorkerPostDeleteVerifyFailureResumesToVerified(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	boom := errors.New("transient verify failure")
	w := newWorker(fx, CrashSeams{
		AfterDelete: func() error { return boom },
		MaxAttempts: 5,
	})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateRetryableFailed) {
		t.Fatalf("state=%s", state)
	}
	if _, err = os.Stat(fx.SourcePath); !os.IsNotExist(err) {
		t.Fatal("source must already be gone after successful delete")
	}
	var qPath, qFP string
	if err = db.QueryRow(`SELECT quarantine_path,quarantine_fingerprint FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID).Scan(&qPath, &qFP); err != nil {
		t.Fatal(err)
	}
	if qPath == "" || qFP == "" {
		t.Fatal("quarantine identity must remain for verify-only resume")
	}
	if _, err = os.Stat(qPath); !os.IsNotExist(err) {
		t.Fatal("quarantine must already be gone after successful delete")
	}

	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET next_retry_at=datetime(CURRENT_TIMESTAMP,'-1 seconds') WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	w2 := newWorker(fx, CrashSeams{MaxAttempts: 5})
	row2, err := w2.ClaimReady(context.Background())
	if err != nil {
		t.Fatalf("ClaimReady must pick up post-delete verify failure: %v", err)
	}
	if err = w2.Execute(context.Background(), *row2); err != nil {
		t.Fatalf("verify-only resume must reach verified without ReconcileStartup: %v", err)
	}
	state, _ = retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestWorkerPostDeleteVerifyResumeFailsClosedWithoutFingerprint(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	id := identityFor(fx, 1)
	qPath, err := QuarantinePath(fx.QuarantineRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(fx.SourcePath)
	if _, err = db.Exec(`UPDATE media_plaintext_retirement
SET state='retryable_failed', attempts=1, quarantine_path=?, quarantine_fingerprint='',
    next_retry_at=datetime(CURRENT_TIMESTAMP,'-1 seconds'), last_error='verify failed'
WHERE id=?`, qPath, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	w := newWorker(fx, CrashSeams{MaxAttempts: 5})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); err == nil {
		t.Fatal("expected fail closed without fingerprint evidence")
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state == string(StateVerified) {
		t.Fatal("must not verify without fingerprint evidence")
	}
	if state != string(StateOperatorRequired) && state != string(StateBlocked) && state != string(StateRetryableFailed) {
		t.Fatalf("state=%s", state)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestWorkerPostQuarantineDeleteFailureRetryableWithoutReconcile(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	boom := errors.New("transient delete failure")
	w := newWorker(fx, CrashSeams{
		BeforeDelete: func() error { return boom },
		MaxAttempts:  5,
	})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateRetryableFailed) {
		t.Fatalf("post-quarantine failure must be retryable_failed, got %s", state)
	}
	var nextRetry *time.Time
	if err = db.QueryRow(`SELECT next_retry_at FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID).Scan(&nextRetry); err != nil {
		t.Fatal(err)
	}
	if nextRetry == nil {
		t.Fatal("next_retry_at must be set for retryable post-quarantine failure")
	}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET next_retry_at=datetime(CURRENT_TIMESTAMP,'-1 seconds') WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	w2 := newWorker(fx, CrashSeams{MaxAttempts: 5})
	row2, err := w2.ClaimReady(context.Background())
	if err != nil {
		t.Fatalf("ClaimReady must pick up post-quarantine retryable_failed without ReconcileStartup: %v", err)
	}
	if err = w2.Execute(context.Background(), *row2); err != nil {
		t.Fatalf("resume execute: %v", err)
	}
	state, _ = retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}
	if _, err = os.Stat(fx.SourcePath); !os.IsNotExist(err) {
		t.Fatal("source should be gone after resume")
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestWorkerExecuteRenewsLeaseDuringLongWork(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	var renews atomic.Int32
	w := newWorker(fx, CrashSeams{
		OnRenew: func() { renews.Add(1) },
		BeforeDelete: func() error {
			if renews.Load() < 1 {
				return errors.New("expected RenewLease before delete work")
			}
			return nil
		},
	})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Execute(context.Background(), *row); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if renews.Load() < 3 {
		t.Fatalf("expected chunked RenewLease during quarantine/delete/verify, got %d", renews.Load())
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateVerified) {
		t.Fatalf("state=%s", state)
	}
}

func TestWorkerRestoreFailureFailsClosed(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	boom := errors.New("crash before state commit")
	restoreBoom := errors.New("restore refused")
	w := newWorker(fx, CrashSeams{
		BeforeStateCommit: func() error { return boom },
		FileOps: FileOps{
			Rename: func(oldpath, newpath string) error {
				// First rename is MoveToQuarantine; subsequent is RestoreQuarantine.
				if _, err := os.Stat(fx.SourcePath); os.IsNotExist(err) {
					return restoreBoom
				}
				return os.Rename(oldpath, newpath)
			},
		},
	})
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = w.Execute(context.Background(), *row)
	if err == nil {
		t.Fatal("expected restore failure to surface")
	}
	if !errors.Is(err, restoreBoom) && !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	state, _ := retirementState(t, db, fx.RetirementID)
	if state != string(StateOperatorRequired) {
		t.Fatalf("restore failure must fail closed to operator_required, got %s", state)
	}
}

func TestRetirementClaimReadyDoesNotStarveOperatorRequired(t *testing.T) {
	TestClaimReadyDoesNotStarveRetryableBehindOperatorRequired(t)
}

func TestRetirementRecoverySmokeInterruptedQuarantining(t *testing.T) {
	TestRecoveryInterruptedQuarantiningBeforeMove(t)
}

func TestClaimReadyDoesNotStarveRetryableBehindOperatorRequired(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})

	// Older operator_required row must not block claiming a due retryable_failed row.
	if _, err := db.Exec(`UPDATE media_plaintext_retirement SET state='operator_required', updated_at=datetime(CURRENT_TIMESTAMP,'-1 hour'), next_retry_at=NULL WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}

	source2 := filepath.Join(fx.Root, "library", "movie2.mp4")
	writeFile(t, source2, []byte("second-source-body-for-claim"))
	fp2, err := storage.EncryptionSourceFingerprint(source2)
	if err != nil {
		t.Fatal(err)
	}
	enc2 := filepath.Join(fx.Root, "library", "movie2.mp4.enc")
	writeFile(t, enc2, []byte("ciphertext-2"))
	stage2 := "00000000-0000-4000-8000-000000000002"
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,file_type,file_path,ingest_generation,publication_state) VALUES(2,1,'f2','video',?,1,'published')`, enc2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,2,1,'scan','published','{}',3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts) VALUES(23,20,2,1,'encrypt',1,'done',1,3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round) VALUES(213,2,20,23,1,'encrypt','done',1,3,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_plan_completion(run_id,media_id,generation,all_terminal,total_count,terminal_count,waiting_count,running_count,done_count,skipped_count,failed_count,cancelled_count,completed_at)
VALUES(20,2,1,1,1,1,0,0,1,0,0,0,CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(2,?,'aabb','ccdd',?,'encrypted')`, enc2, source2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state)
VALUES(?,213,0,1,2,20,23,1,'worker',?,?,?,'aabb','ccdd',?,?,1,'committed')`, stage2, source2, fp2, enc2, shaHex([]byte("ciphertext-2")), int64(len("ciphertext-2"))); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,attempts,next_retry_at,quarantine_evidence_json,updated_at)
VALUES(2,20,1,?,?,'encryption',213,?,NULL,0,'retryable_failed',1,CURRENT_TIMESTAMP,'{}',CURRENT_TIMESTAMP)`, source2, fp2, stage2)
	if err != nil {
		t.Fatal(err)
	}
	retryID, _ := res.LastInsertId()

	w := &Worker{DB: db, Owner: "retire-worker", Seams: CrashSeams{QuarantineRoot: fx.QuarantineRoot, MaxAttempts: 3}}
	row, err := w.ClaimReady(context.Background())
	if err != nil {
		t.Fatalf("operator_required must not starve due retryable: %v", err)
	}
	if row.RetirementID != retryID {
		t.Fatalf("claimed=%d want retryable id=%d", row.RetirementID, retryID)
	}
	var opState string
	if err := db.QueryRow(`SELECT state FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID).Scan(&opState); err != nil {
		t.Fatal(err)
	}
	if opState != string(StateOperatorRequired) {
		t.Fatalf("operator_required row mutated to %s", opState)
	}
}

func TestRunWorkerLoopClaimsDueRetryable(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	if _, err := db.Exec(`UPDATE media_plaintext_retirement SET state='retryable_failed', next_retry_at=CURRENT_TIMESTAMP, attempts=1 WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	w := newWorker(fx, CrashSeams{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunWorkerLoop(ctx, w, time.Hour, nil)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, _ := retirementState(t, db, fx.RetirementID)
		if state == string(StateVerified) {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("RunWorkerLoop did not advance retryable retirement")
}
