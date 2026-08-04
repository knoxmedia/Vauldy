package postingest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/storage"
	"knox-media/internal/store"
)

func TestEncryptAdminResetRetryRoundMonotonicAndJournalIdentity(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, runID, stepID := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "encrypt-admin", nil)
	ctx := context.Background()

	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed',attempts=2,retry_round=0,last_error='boom' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	stage0 := "10000000-0000-0000-0000-000000000010"
	if _, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state)
VALUES(?,?,0,2,?,?,?,1,'owner','src','fp','enc','dek','iv','sha',1,'staged')`, stage0, taskID, mediaID, runID, stepID); err != nil {
		t.Fatal(err)
	}

	if err := q.AdminResetEncrypt(ctx, taskID, 42); err != nil {
		t.Fatal(err)
	}
	var round, attempts int
	var status string
	if err := db.QueryRow(`SELECT retry_round,attempts,status FROM post_ingest_task WHERE id=?`, taskID).Scan(&round, &attempts, &status); err != nil {
		t.Fatal(err)
	}
	if round != 1 || attempts != 0 || status != string(StatusWaiting) {
		t.Fatalf("after first reset round=%d attempts=%d status=%s", round, attempts, status)
	}
	var stepRound int
	if err := db.QueryRow(`SELECT retry_round FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepRound); err != nil || stepRound != 1 {
		t.Fatalf("step retry_round=%d err=%v", stepRound, err)
	}
	var auditN int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_encrypt_admin_audit WHERE task_id=? AND action='reset' AND previous_retry_round=0 AND new_retry_round=1`, taskID).Scan(&auditN); err != nil || auditN != 1 {
		t.Fatalf("reset audit count=%d err=%v", auditN, err)
	}

	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed',attempts=1 WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := q.AdminResetEncrypt(ctx, taskID, 42); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT retry_round FROM post_ingest_task WHERE id=?`, taskID).Scan(&round); err != nil || round != 2 {
		t.Fatalf("second reset round=%d err=%v", round, err)
	}

	var oldJournal int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_encryption_stage_journal WHERE stage_id=? AND retry_round=0 AND attempt=2`, stage0).Scan(&oldJournal); err != nil || oldJournal != 1 {
		t.Fatalf("old journal survived=%d err=%v", oldJournal, err)
	}
	if _, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state)
VALUES('10000000-0000-0000-0000-000000000011',?,0,2,?,?,?,1,'owner','src','fp','enc2','dek','iv','sha',1,'staged')`, taskID, mediaID, runID, stepID); err == nil {
		t.Fatal("expected uniqueness collision on (task_id,retry_round,attempt)")
	}
	if _, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state)
VALUES('10000000-0000-0000-0000-000000000012',?,2,1,?,?,?,1,'owner','src','fp','enc3','dek','iv','sha',1,'staged')`, taskID, mediaID, runID, stepID); err != nil {
		t.Fatalf("new round journal insert: %v", err)
	}
}

func TestEncryptAdminResetStaleRoundCannotCommit(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, runID, stepID := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "stale-round", nil)
	ctx := context.Background()

	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed',attempts=1,retry_round=0 WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := q.AdminResetEncrypt(ctx, taskID, 42); err != nil {
		t.Fatal(err)
	}
	task, err := q.Claim(ctx, TaskEncrypt)
	if err != nil || task == nil || task.RetryRound != 1 {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	stale := *task
	stale.RetryRound = 0
	if err := q.Complete(ctx, stale); err == nil {
		t.Fatal("stale retry_round completed")
	}
	if err := finishEncryptionLifecycleTx(ctx, db, stale); err == nil {
		t.Fatal("stale retry_round finished lifecycle")
	}
	_ = runID
	_ = stepID
	_ = mediaID
}

func TestEncryptAdminTombstoneRemoveListAndRecovery(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, runID, stepID := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "tombstone", nil)
	ctx := context.Background()

	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed',attempts=1,retry_round=0 WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "plain.bin")
	enc := filepath.Join(root, "stage.enc")
	if err := os.WriteFile(source, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("enc"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET file_path=? WHERE id=?`, source, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE library SET path=? WHERE id=(SELECT library_id FROM media WHERE id=?)`, root, mediaID); err != nil {
		t.Fatal(err)
	}
	stageID := "10000000-0000-0000-0000-000000000020"
	if _, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state)
VALUES(?,?,0,1,?,?,?,1,'owner',?,'fp',?,'dek','iv','hash',3,'staged')`, stageID, taskID, mediaID, runID, stepID, source, enc); err != nil {
		t.Fatal(err)
	}

	if err := q.AdminRemoveEncrypt(ctx, taskID, 42); err != nil {
		t.Fatal(err)
	}
	var removedAt sql.NullString
	var removedBy, reason string
	var n int
	if err := db.QueryRow(`SELECT removed_at,removed_by,remove_reason,COUNT(*) FROM post_ingest_task WHERE id=?`, taskID).Scan(&removedAt, &removedBy, &reason, &n); err != nil {
		t.Fatal(err)
	}
	if !removedAt.Valid || strings.TrimSpace(removedAt.String) == "" || n != 1 {
		t.Fatalf("tombstone missing: removed_at=%v count=%d", removedAt, n)
	}
	if reason == "" {
		t.Fatal("expected remove_reason")
	}
	var journalN int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_encryption_stage_journal WHERE stage_id=?`, stageID).Scan(&journalN); err != nil || journalN != 1 {
		t.Fatalf("journal after tombstone=%d err=%v", journalN, err)
	}

	hidden, err := q.ListEncrypt(ctx, "all", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range hidden {
		if row.ID == taskID {
			t.Fatal("default list exposed tombstoned encrypt task")
		}
	}
	shown, err := q.ListEncrypt(ctx, "all", 50, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range shown {
		if row.ID == taskID {
			found = true
		}
	}
	if !found {
		t.Fatal("include-removed list missing tombstoned encrypt task")
	}

	if err := q.AdminPurgeEncrypt(ctx, taskID, 42); err == nil || !strings.Contains(err.Error(), "journal") {
		t.Fatalf("purge with journal err=%v", err)
	}

	quarantine := t.TempDir()
	checked, cleaned, err := ReconcileEncryptionStages(ctx, db, EncryptionRecoveryRoots{Quarantine: quarantine, Resolver: fixedStageRoot(root)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if checked < 1 {
		t.Fatalf("recovery did not continue through tombstone checked=%d cleaned=%d", checked, cleaned)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM media_encryption_stage_journal WHERE stage_id=?`, stageID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "staged" {
		t.Fatalf("expected recovery to progress journal state, got %s", state)
	}
}

func TestEncryptAdminPurgeRejectedWhileReferencesRemain(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, runID, stepID := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "purge", nil)
	ctx := context.Background()
	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='cancelled' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := q.AdminRemoveEncrypt(ctx, taskID, 42); err != nil {
		t.Fatal(err)
	}

	t.Run("journal_blocks_and_persists_purge_rejected_audit", func(t *testing.T) {
		stageID := "10000000-0000-0000-0000-000000000040"
		if _, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state)
VALUES(?,?,0,1,?,?,?,1,'owner','src','fp','enc','dek','iv','sha',1,'staged')`, stageID, taskID, mediaID, runID, stepID); err != nil {
			t.Fatal(err)
		}
		if err := q.AdminPurgeEncrypt(ctx, taskID, 42); err == nil || !strings.Contains(strings.ToLower(err.Error()), "journal") {
			t.Fatalf("purge with journal err=%v", err)
		}
		var rejected int
		if err := db.QueryRow(`SELECT COUNT(*) FROM media_encrypt_admin_audit WHERE task_id=? AND action='purge_rejected' AND reason='journal_refs'`, taskID).Scan(&rejected); err != nil || rejected != 1 {
			t.Fatalf("durable purge_rejected audit count=%d err=%v", rejected, err)
		}
		if _, err := db.Exec(`DELETE FROM media_encryption_stage_journal WHERE stage_id=?`, stageID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("retirement", func(t *testing.T) {
		res, err := db.Exec(`INSERT INTO package_task(media_id,pipeline_type,status) VALUES(?,'cmaf_drm','done')`, mediaID)
		if err != nil {
			t.Fatal(err)
		}
		pkgID, _ := res.LastInsertId()
		if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json)
VALUES(?,?,1,'src','fp','package',?,NULL,?,0,'blocked','{}')`, mediaID, runID, pkgID, pkgID); err != nil {
			t.Fatal(err)
		}
		if err := q.AdminPurgeEncrypt(ctx, taskID, 42); err == nil || !strings.Contains(strings.ToLower(err.Error()), "retirement") {
			t.Fatalf("purge with retirement err=%v", err)
		}
		if _, err := db.Exec(`DELETE FROM media_plaintext_retirement WHERE media_id=? AND generation=1`, mediaID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dependency", func(t *testing.T) {
		res, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'thumbnail',0,'waiting')`, runID, mediaID)
		if err != nil {
			t.Fatal(err)
		}
		depStep, _ := res.LastInsertId()
		if _, err := db.Exec(`INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(?,?, 'success')`, depStep, stepID); err != nil {
			t.Fatal(err)
		}
		if err := q.AdminPurgeEncrypt(ctx, taskID, 42); err == nil || !strings.Contains(strings.ToLower(err.Error()), "dependency") {
			t.Fatalf("purge with dependency err=%v", err)
		}
		if _, err := db.Exec(`DELETE FROM media_ingest_step_dependency WHERE step_id=?`, depStep); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEncryptAdminPurgeSucceedsWhenOnlyAuditsRemain(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "purge-ok", nil)
	ctx := context.Background()
	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='cancelled' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := q.AdminRemoveEncrypt(ctx, taskID, 7); err != nil {
		t.Fatal(err)
	}
	var beforeAudits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_encrypt_admin_audit WHERE task_id=?`, taskID).Scan(&beforeAudits); err != nil || beforeAudits < 1 {
		t.Fatalf("expected live audits before purge count=%d err=%v", beforeAudits, err)
	}
	if err := q.AdminPurgeEncrypt(ctx, taskID, 7); err != nil {
		t.Fatal(err)
	}
	var taskN, liveAudits, archived, purgeArchived int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE id=?`, taskID).Scan(&taskN); err != nil || taskN != 0 {
		t.Fatalf("task remained count=%d err=%v", taskN, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_encrypt_admin_audit WHERE task_id=?`, taskID).Scan(&liveAudits); err != nil || liveAudits != 0 {
		t.Fatalf("live audits after purge=%d err=%v", liveAudits, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_encrypt_admin_audit_archive WHERE task_id=?`, taskID).Scan(&archived); err != nil || archived < beforeAudits {
		t.Fatalf("archived audits=%d want>=%d err=%v", archived, beforeAudits, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_encrypt_admin_audit_archive WHERE task_id=? AND action='purge'`, taskID).Scan(&purgeArchived); err != nil || purgeArchived != 1 {
		t.Fatalf("purge archive row=%d err=%v", purgeArchived, err)
	}
}

func TestEncryptAdminRemoveRejectsStaleGeneration(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "stale-gen-remove", nil)
	ctx := context.Background()
	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	err := q.AdminRemoveEncrypt(ctx, taskID, 42)
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale generation remove err=%v", err)
	}
	var removed sql.NullString
	if err := db.QueryRow(`SELECT removed_at FROM post_ingest_task WHERE id=?`, taskID).Scan(&removed); err != nil || removed.Valid {
		t.Fatalf("tombstone written on stale generation: %v err=%v", removed, err)
	}
}

func TestEncryptAdminResetRejectsStaleGeneration(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "stale-gen-reset", nil)
	ctx := context.Background()
	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET ingest_generation=9 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	err := q.AdminResetEncrypt(ctx, taskID, 42)
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale generation reset err=%v", err)
	}
}

func TestEncryptAdminResetAmbiguousCommitReconcilesOnNextRound(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "ambiguous-reset", nil)
	ctx := context.Background()
	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed',attempts=1,retry_round=0 WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	q.immediateTx = func(ctx context.Context, dbArg *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		outcome, err := store.WithImmediateConnTx(ctx, dbArg, fn)
		if err != nil {
			return outcome, err
		}
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("injected ambiguous commit")}
	}
	if err := q.AdminResetEncrypt(ctx, taskID, 9); err != nil {
		t.Fatalf("reconcile after ambiguous commit: %v", err)
	}
	var round int
	var status string
	if err := db.QueryRow(`SELECT retry_round,status FROM post_ingest_task WHERE id=?`, taskID).Scan(&round, &status); err != nil {
		t.Fatal(err)
	}
	if round != 1 || status != string(StatusWaiting) {
		t.Fatalf("round=%d status=%s", round, status)
	}
}

func TestEncryptAdminResetAmbiguousCommitDoesNotReconcileOnStaleRound(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "ambiguous-stale", nil)
	ctx := context.Background()
	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed',attempts=1,retry_round=0 WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	q.immediateTx = func(ctx context.Context, dbArg *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		conn, err := dbArg.Conn(ctx)
		if err != nil {
			return store.ImmediateOutcome{}, err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			return store.ImmediateOutcome{}, err
		}
		if err := fn(conn); err != nil {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
			return store.ImmediateOutcome{}, err
		}
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("commit unknown; rolled back")}
	}
	err := q.AdminResetEncrypt(ctx, taskID, 9)
	if err == nil {
		t.Fatal("expected ambiguous commit without nextRound advance to fail")
	}
	var round int
	if e := db.QueryRow(`SELECT retry_round FROM post_ingest_task WHERE id=?`, taskID).Scan(&round); e != nil || round != 0 {
		t.Fatalf("round=%d err=%v", round, e)
	}
}

func TestEncryptTombstoneClaimFailsClosedAndReviveIsClaimable(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "tombstone-claim", nil)
	ctx := context.Background()
	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	// Waiting while tombstoned (not produced by AdminRemove, but possible via reopen bugs/manual SQL) must fail closed.
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='waiting',removed_at=CURRENT_TIMESTAMP,removed_by='x',remove_reason='manual',available_at=CURRENT_TIMESTAMP,attempts=0 WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	claimed, err := q.Claim(ctx, TaskEncrypt)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("claimed tombstoned waiting task: %+v", claimed)
	}

	if _, already, err := q.EnqueueEncryptManual(ctx, mediaID); err != nil || already {
		t.Fatalf("enqueue revive already=%v err=%v", already, err)
	}
	var removed sql.NullString
	var action string
	if err := db.QueryRow(`SELECT removed_at FROM post_ingest_task WHERE id=?`, taskID).Scan(&removed); err != nil || removed.Valid {
		t.Fatalf("tombstone not cleared: %v err=%v", removed, err)
	}
	if err := db.QueryRow(`SELECT action FROM media_encrypt_admin_audit WHERE task_id=? ORDER BY id DESC LIMIT 1`, taskID).Scan(&action); err != nil || action != "reset_from_removed" {
		t.Fatalf("revive audit action=%q err=%v", action, err)
	}
	claimed, err = q.Claim(ctx, TaskEncrypt)
	if err != nil || claimed == nil || claimed.ID != taskID {
		t.Fatalf("claim after enqueue revive=%+v err=%v", claimed, err)
	}
}

func TestEncryptAdminResetFromRemovedAuditsRevive(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "reset-revive", nil)
	ctx := context.Background()
	taskID, _, err := q.EnqueueEncryptManual(ctx, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := q.AdminRemoveEncrypt(ctx, taskID, 5); err != nil {
		t.Fatal(err)
	}
	if err := q.AdminResetEncrypt(ctx, taskID, 5); err != nil {
		t.Fatal(err)
	}
	var action string
	var removed sql.NullString
	if err := db.QueryRow(`SELECT action FROM media_encrypt_admin_audit WHERE task_id=? ORDER BY id DESC LIMIT 1`, taskID).Scan(&action); err != nil || action != "reset_from_removed" {
		t.Fatalf("action=%q err=%v", action, err)
	}
	if err := db.QueryRow(`SELECT removed_at FROM post_ingest_task WHERE id=?`, taskID).Scan(&removed); err != nil || removed.Valid {
		t.Fatalf("tombstone remained: %v", removed)
	}
	claimed, err := q.Claim(ctx, TaskEncrypt)
	if err != nil || claimed == nil || claimed.ID != taskID {
		t.Fatalf("claim after reset revive=%+v err=%v", claimed, err)
	}
}

func TestEnqueueEncryptManualReviveClearsTombstoneAndIsClaimable(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "enqueue-revive", nil)
	ctx := context.Background()
	id, _, err := q.EnqueueEncryptManual(ctx, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed',removed_at=CURRENT_TIMESTAMP,removed_by='1',remove_reason='admin_remove' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	reopenID, already, err := q.EnqueueEncryptManual(ctx, mediaID)
	if err != nil || already || reopenID != id {
		t.Fatalf("enqueue revive id=%d already=%v err=%v", reopenID, already, err)
	}
	var action string
	var removed sql.NullString
	if err := db.QueryRow(`SELECT action FROM media_encrypt_admin_audit WHERE task_id=? AND action='reset_from_removed'`, id).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT removed_at FROM post_ingest_task WHERE id=?`, id).Scan(&removed); err != nil || removed.Valid {
		t.Fatalf("tombstone after enqueue: %v err=%v", removed, err)
	}
	claimed, err := q.Claim(ctx, TaskEncrypt)
	if err != nil || claimed == nil || claimed.ID != id {
		t.Fatalf("claim after enqueue revive=%+v err=%v", claimed, err)
	}
}

func TestEnqueueEncryptManualCurrentGenerationFence(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "enqueue-gen", nil)
	ctx := context.Background()
	oldID, _, err := q.EnqueueEncryptManual(ctx, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed' WHERE id=?`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	newID, already, err := q.EnqueueEncryptManual(ctx, mediaID)
	if err != nil || already || newID == oldID {
		t.Fatalf("current-gen enqueue id=%d old=%d already=%v err=%v", newID, oldID, already, err)
	}
	var oldStatus string
	var oldRound int
	if err := db.QueryRow(`SELECT status,retry_round FROM post_ingest_task WHERE id=?`, oldID).Scan(&oldStatus, &oldRound); err != nil {
		t.Fatal(err)
	}
	if oldStatus != string(StatusFailed) || oldRound != 0 {
		t.Fatalf("stale-gen row mutated status=%s round=%d", oldStatus, oldRound)
	}
}

func TestEnqueueEncryptManualRetryRoundCAS(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "enqueue-cas", nil)
	ctx := context.Background()
	id, _, err := q.EnqueueEncryptManual(ctx, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed',retry_round=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	q.immediateTx = func(ctx context.Context, dbArg *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.WithImmediateConnTx(ctx, dbArg, func(tx store.ImmediateConnTx) error {
			return fn(&casBumpTx{ImmediateConnTx: tx, taskID: id})
		})
	}
	_, _, err = q.EnqueueEncryptManual(ctx, mediaID)
	if err == nil || !strings.Contains(err.Error(), "raced") {
		t.Fatalf("expected CAS race err=%v", err)
	}
}

type casBumpTx struct {
	store.ImmediateConnTx
	taskID int64
	bumped bool
}

func (c *casBumpTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	row := c.ImmediateConnTx.QueryRowContext(ctx, query, args...)
	if !c.bumped && strings.Contains(query, "retry_round") && strings.Contains(query, "removed_at") {
		c.bumped = true
		_, _ = c.ImmediateConnTx.ExecContext(ctx, `UPDATE post_ingest_task SET retry_round=retry_round+1 WHERE id=?`, c.taskID)
	}
	return row
}

func TestEncryptAdminRemovePersistsActor(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedLinkedEncryptAdminFixture(t, db)
	q := NewQueue(db, "actor-remove", nil)
	ctx := context.Background()
	var taskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE media_id=? AND task_type='encrypt'`, mediaID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if err := q.AdminRemoveEncrypt(ctx, taskID, 99); err != nil {
		t.Fatal(err)
	}
	var removedBy string
	var actorID int64
	if err := db.QueryRow(`SELECT removed_by FROM post_ingest_task WHERE id=?`, taskID).Scan(&removedBy); err != nil || removedBy != "99" {
		t.Fatalf("removed_by=%q err=%v", removedBy, err)
	}
	if err := db.QueryRow(`SELECT actor_id FROM media_encrypt_admin_audit WHERE task_id=? AND action='remove'`, taskID).Scan(&actorID); err != nil || actorID != 99 {
		t.Fatalf("actor_id=%d err=%v", actorID, err)
	}
}

func TestEncryptJournalInsertUsesRetryRoundIdentity(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	runID, stepID := int64(11), int64(12)
	task := Task{ID: 7, MediaID: 41, Type: TaskEncrypt, Generation: 2, RetryRound: 3, RunID: &runID, StepID: &stepID, LeaseOwner: "worker", Attempts: 1}
	source := filepath.Join(t.TempDir(), "a.bin")
	enc := filepath.Join(t.TempDir(), "a.enc")
	_ = os.WriteFile(source, []byte("x"), 0600)
	_ = os.WriteFile(enc, []byte("y"), 0600)
	stage := storage.StagedMediaEncryption{
		StageID: stageIDForAdminTest("10000000-0000-0000-0000-000000000030"), OriginalPath: source, EncPath: enc,
		SourceFingerprint: "fp", WrappedDEK: "dek", IV: "iv", SHA256: "hash", Size: 1, CleanupPlaintext: false,
	}
	if err := insertEncryptionStageJournal(context.Background(), db, task, stage); err != nil {
		t.Fatal(err)
	}
	var round int
	if err := db.QueryRow(`SELECT retry_round FROM media_encryption_stage_journal WHERE stage_id=?`, stage.StageID).Scan(&round); err != nil || round != 3 {
		t.Fatalf("retry_round=%d err=%v", round, err)
	}
	stale := task
	stale.RetryRound = 0
	if _, err := loadJournalEncryptionStage(context.Background(), db, stale); err == nil {
		t.Fatal("stale retry_round loaded journal")
	}
	loaded, err := loadJournalEncryptionStage(context.Background(), db, task)
	if err != nil || loaded.StageID != stage.StageID {
		t.Fatalf("load=%+v err=%v", loaded, err)
	}
}

func seedLinkedEncryptAdminFixture(t *testing.T, db *sql.DB) (mediaID, runID, stepID int64) {
	t.Helper()
	mediaID, _, _ = seedQueueTest(t, db)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=1,publication_state='processing' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','processing','{}',3)`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,max_attempts,retry_round) VALUES(?,?,1,'encrypt',1,'failed',1,3,0)`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ = res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round) VALUES(?,?,?,1,'encrypt','failed',1,3,0)`, mediaID, runID, stepID); err != nil {
		t.Fatal(err)
	}
	return mediaID, runID, stepID
}

func stageIDForAdminTest(id string) string { return id }
