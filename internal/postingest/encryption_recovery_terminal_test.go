package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixedStageRoot string

func (r fixedStageRoot) ResolveEncryptionStageRoot(context.Context, int64, string) (string, error) {
	return string(r), nil
}

func TestReconcileEncryptionStagesTerminalRowsDoNotStarveActionable(t *testing.T) {
	db, _ := openQueueTestDB(t)

	if _, e := db.Exec(`PRAGMA foreign_keys=OFF`); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	quarantine := t.TempDir()
	source := filepath.Join(root, "source.jpg")
	if e := os.WriteFile(source, []byte("plain"), 0600); e != nil {
		t.Fatal(e)
	}
	res, e := db.Exec(`INSERT INTO library(name,type,path) VALUES('recover','photo',?)`, root)
	if e != nil {
		t.Fatal(e)
	}
	libraryID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'recover',?,'image','active','processing',1)`, libraryID, source)
	if e != nil {
		t.Fatal(e)
	}
	mediaID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'repair','processing','{}',2)`, mediaID)
	if e != nil {
		t.Fatal(e)
	}
	runID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,status,required) VALUES(?,?,1,'encrypt','running',1)`, runID, mediaID)
	if e != nil {
		t.Fatal(e)
	}
	stepID, _ := res.LastInsertId()
	insert := func(i int, state, marker, updated string) string {
		t.Helper()
		res, e := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts) VALUES(?,?,?,?,'encrypt','failed',?,3)`, mediaID, runID, stepID, i+2, i+1)
		if e != nil {
			t.Fatal(e)
		}
		taskID, _ := res.LastInsertId()
		stage := fmt.Sprintf("10000000-0000-0000-0000-%012d", i)
		enc := filepath.Join(root, stage+".enc")
		if e = os.WriteFile(enc, []byte("enc"), 0600); e != nil {
			t.Fatal(e)
		}
		_, e = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,recovery_error,updated_at) VALUES(?,?,?,?,?,?,?,'owner',?,'fp',?,'wrapped','iv','hash',3,?,?,?)`, stage, taskID, i+1, mediaID, runID, stepID, i+2, source, enc, state, marker, updated)
		if e != nil {
			t.Fatal(e)
		}
		return stage
	}
	for i := 0; i < 101; i++ {
		insert(i, "restored", "stale_restored", "2000-01-01 00:00:00")
	}
	actionable := insert(101, "staged", "", "2099-01-01 00:00:00")
	roots := EncryptionRecoveryRoots{Quarantine: quarantine, Resolver: fixedStageRoot(root)}
	checked, cleaned, e := ReconcileEncryptionStages(context.Background(), db, roots, 100)
	if e != nil || checked != 1 || cleaned != 1 {
		t.Fatalf("checked=%d cleaned=%d err=%v", checked, cleaned, e)
	}
	var state, marker string
	if e = db.QueryRow(`SELECT state,recovery_error FROM media_encryption_stage_journal WHERE stage_id=?`, actionable).Scan(&state, &marker); e != nil {
		t.Fatal(e)
	}
	if state != "restored" || marker != "stale_restored" {
		t.Fatalf("state=%s marker=%s", state, marker)
	}
	checked, cleaned, e = ReconcileEncryptionStages(context.Background(), db, roots, 100)
	if e != nil || checked != 0 || cleaned != 0 {
		t.Fatalf("repeat checked=%d cleaned=%d err=%v", checked, cleaned, e)
	}
}

func TestCommittedPlaintextCleanupRetriesBeforeVerification(t *testing.T) {
	db, _ := openQueueTestDB(t)
	quarantineRoot := t.TempDir()
	path := filepath.Join(quarantineRoot, "1", "1", "00000000-0000-0000-0000-000000000099", "source")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte("plain"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`PRAGMA foreign_keys=OFF; INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,quarantine_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state) VALUES('00000000-0000-0000-0000-000000000099',1,1,1,1,1,1,'owner','source',?,'fp','enc','wrapped','iv','hash',1,'committed')`, path); e != nil {
		t.Fatal(e)
	}
	want := errors.New("transient remove")
	ops := defaultEncryptionFileOps()
	ops.remove = func(string) error { return want }
	outcome, e := cleanupCommittedEncryptionPlaintext(quarantineRoot, 1, 1, "00000000-0000-0000-0000-000000000099", path, ops)
	if outcome != committedCleanupRetry || !errors.Is(e, want) {
		t.Fatalf("outcome=%v err=%v", outcome, e)
	}
	ops = defaultEncryptionFileOps()
	outcome, e = cleanupCommittedEncryptionPlaintext(quarantineRoot, 1, 1, "00000000-0000-0000-0000-000000000099", path, ops)
	if outcome != committedCleanupVerified || e != nil {
		t.Fatalf("outcome=%v err=%v", outcome, e)
	}
}

func TestReconcileEncryptionStagesRetriesTransientRestoreWithoutStarving(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	root, quarantineRoot := t.TempDir(), t.TempDir()
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'retry','photo',?)`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(1,1,'retry',?,'image','active','processing',1)`, filepath.Join(root, "selected.enc")); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source.jpg")
	quarantine := filepath.Join(quarantineRoot, "1", "1", "00000000-0000-0000-0000-000000000102", "source")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	fingerprint := testEncryptionSourceFingerprint(t, source, quarantine)
	_, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,quarantine_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state) VALUES('00000000-0000-0000-0000-000000000102',1,1,1,1,1,1,'owner',?,?,?,?,'wrapped','iv','hash',1,'quarantined')`, source, quarantine, fingerprint, filepath.Join(root, "00000000-0000-0000-0000-000000000102.enc"))
	if err != nil {
		t.Fatal(err)
	}
	original := reconcileRestoreQuarantinedPlaintext
	calls := 0
	reconcileRestoreQuarantinedPlaintext = func(q, s, root string, mediaID, generation int64, stageID string, ops encryptionFileOps) error {
		calls++
		if calls == 1 {
			return os.ErrPermission
		}
		return restoreQuarantinedPlaintextWithOps(q, s, root, mediaID, generation, stageID, ops)
	}
	t.Cleanup(func() { reconcileRestoreQuarantinedPlaintext = original })
	roots := EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(root)}
	checked, cleaned, err := ReconcileEncryptionStages(context.Background(), db, roots, 100)
	if err != nil || checked != 1 || cleaned != 0 {
		t.Fatalf("first checked=%d cleaned=%d err=%v", checked, cleaned, err)
	}
	var state, marker string
	var attempts int
	if err = db.QueryRow(`SELECT state,recovery_error,recovery_attempts FROM media_encryption_stage_journal WHERE stage_id='00000000-0000-0000-0000-000000000102'`).Scan(&state, &marker, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "failed_closed" || !strings.HasPrefix(marker, "restore_pending:") || attempts != 1 {
		t.Fatalf("state=%s marker=%q attempts=%d", state, marker, attempts)
	}
	if _, err = db.Exec(`UPDATE media_encryption_stage_journal SET next_retry_at=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE stage_id='00000000-0000-0000-0000-000000000102'`); err != nil {
		t.Fatal(err)
	}
	checked, cleaned, err = ReconcileEncryptionStages(context.Background(), db, roots, 100)
	if err != nil || checked != 1 || cleaned != 1 {
		t.Fatalf("retry checked=%d cleaned=%d err=%v", checked, cleaned, err)
	}
	if err = db.QueryRow(`SELECT state,recovery_error FROM media_encryption_stage_journal WHERE stage_id='00000000-0000-0000-0000-000000000102'`).Scan(&state, &marker); err != nil {
		t.Fatal(err)
	}
	if state != "restored" || marker != "stale_restored" {
		t.Fatalf("state=%s marker=%q", state, marker)
	}
}

func TestReconcileEncryptionStagesExhaustedRestoreDoesNotStarveDueWork(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	root, quarantineRoot := t.TempDir(), t.TempDir()
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'retry','photo',?)`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(1,1,'retry',?,'image','active','processing',1)`, filepath.Join(root, "selected.enc")); err != nil {
		t.Fatal(err)
	}
	nextTask := int64(1)
	insert := func(stage string, attempts int, updated string) string {
		source := filepath.Join(root, stage+".jpg")
		q := filepath.Join(quarantineRoot, "1", "1", stage, "source")
		if err := os.MkdirAll(filepath.Dir(q), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(q, []byte(stage), 0600); err != nil {
			t.Fatal(err)
		}
		fingerprint := testEncryptionSourceFingerprint(t, source, q)
		_, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,quarantine_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,recovery_error,recovery_attempts,next_retry_at,updated_at) VALUES(?,?,1,1,1,1,1,'owner',?,?,?,?, 'wrapped','iv','hash',1,'failed_closed','restore_pending: permission',?,datetime(CURRENT_TIMESTAMP,'-1 second'),?)`, stage, nextTask, source, q, fingerprint, filepath.Join(root, stage+".enc"), attempts, updated)
		nextTask++
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	insert("00000000-0000-0000-0000-000000000103", encryptionRecoveryMaxAttempts, "2000-01-01")
	dueSource := insert("00000000-0000-0000-0000-000000000104", 1, "2001-01-01")
	roots := EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(root)}
	checked, cleaned, err := ReconcileEncryptionStages(context.Background(), db, roots, 1)
	if err != nil || checked != 1 || cleaned != 1 {
		t.Fatalf("checked=%d cleaned=%d err=%v", checked, cleaned, err)
	}
	if _, err = os.Stat(dueSource); err != nil {
		t.Fatalf("due restore missing: %v", err)
	}
}

func seedCommittedCleanupJournal(t *testing.T, db *sql.DB, libraryRoot, quarantineRoot, stage string, attempts int, marker, updated string) string {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(media_id),0)+1 FROM media_encryption_stage_journal`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(libraryRoot, stage+".jpg")
	enc := filepath.Join(libraryRoot, stage+".enc")
	quarantine := filepath.Join(quarantineRoot, fmt.Sprintf("%d", id), "1", stage, "source")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO library(id,name,type,path) VALUES(1,'cleanup','photo',?)`, libraryRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,1,?,?,'image','active','published',1)`, id, stage, enc); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,?,1,'repair','published','{}',2)`, id, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,status,required) VALUES(?,?,?,1,'encrypt','done',1)`, id, id, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts) VALUES(?,?,?,?,1,'encrypt','done',1,3)`, id, id, id, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,quarantine_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,recovery_error,recovery_attempts,next_retry_at,updated_at) VALUES(?,?,1,?,?,?,?, 'owner',?,?,'fp',?,'wrapped','iv','hash',3,'committed',?,?,CURRENT_TIMESTAMP,?)`, stage, id, id, id, id, int64(1), source, quarantine, enc, marker, attempts, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,1,'encrypt','fp','{}','test',CURRENT_TIMESTAMP,?)`, id, id, id, stage); err != nil {
		t.Fatal(err)
	}
	return quarantine
}

func TestReconcileCommittedCleanupCrashAfterRemoveMarksVerified(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	root, quarantineRoot := t.TempDir(), t.TempDir()
	stage := "00000000-0000-0000-0000-000000000201"
	path := seedCommittedCleanupJournal(t, db, root, quarantineRoot, stage, 0, "", "2000-01-01")
	// The prior process removed the leaf but crashed before updating the journal.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("leaf unexpectedly exists: %v", err)
	}
	checked, cleaned, err := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(root)}, 100)
	if err != nil || checked != 1 || cleaned != 0 {
		t.Fatalf("checked=%d cleaned=%d err=%v", checked, cleaned, err)
	}
	var state, marker string
	if err := db.QueryRow(`SELECT state,recovery_error FROM media_encryption_stage_journal WHERE stage_id=?`, stage).Scan(&state, &marker); err != nil {
		t.Fatal(err)
	}
	if state != "committed" || marker != "verified_committed" {
		t.Fatalf("state=%s marker=%q", state, marker)
	}
}

func TestReconcileCommittedCleanupUnsafeFirstContinuesToLaterJournal(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	root, quarantineRoot := t.TempDir(), t.TempDir()
	unsafeStage := "00000000-0000-0000-0000-000000000202"
	seedCommittedCleanupJournal(t, db, root, quarantineRoot, unsafeStage, 0, "", "2000-01-01")
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside")
	if err := os.WriteFile(outside, []byte("do not remove"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_encryption_stage_journal SET quarantine_path=? WHERE stage_id=?`, outside, unsafeStage); err != nil {
		t.Fatal(err)
	}
	actionableStage := "00000000-0000-0000-0000-000000000203"
	seedCommittedCleanupJournal(t, db, root, quarantineRoot, actionableStage, 0, "", "2001-01-01")
	checked, _, err := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(root)}, 100)
	if checked != 2 {
		t.Fatalf("checked=%d err=%v", checked, err)
	}
	if err == nil || !strings.Contains(err.Error(), "unsafe encryption recovery path") {
		t.Fatalf("want unsafe path aggregate err, got %v", err)
	}
	var state, marker string
	if err := db.QueryRow(`SELECT state,recovery_error FROM media_encryption_stage_journal WHERE stage_id=?`, unsafeStage).Scan(&state, &marker); err != nil {
		t.Fatal(err)
	}
	if state != "failed_closed" || marker != "unsafe_path" {
		t.Fatalf("unsafe state=%s marker=%q", state, marker)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "do not remove" {
		t.Fatalf("unsafe media altered: %q %v", got, err)
	}
	if err := db.QueryRow(`SELECT recovery_error FROM media_encryption_stage_journal WHERE stage_id=?`, actionableStage).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "verified_committed" {
		t.Fatalf("actionable marker=%q", marker)
	}
}

func TestReconcileCommittedCleanupTransientLstatRetriesThenSucceeds(t *testing.T) {
	// Committed recovery no longer deletes quarantine; handoff completes without
	// filesystem cleanup lstat retries.
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	root, quarantineRoot := t.TempDir(), t.TempDir()
	stage := "00000000-0000-0000-0000-000000000204"
	path := seedCommittedCleanupJournal(t, db, root, quarantineRoot, stage, 0, "", "2000-01-01")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_encryption_stage_journal SET cleanup_plaintext=1 WHERE stage_id=?`, stage); err != nil {
		t.Fatal(err)
	}
	roots := EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(root)}
	checked, _, err := ReconcileEncryptionStages(context.Background(), db, roots, 100)
	if err != nil || checked != 1 {
		t.Fatalf("checked=%d err=%v", checked, err)
	}
	var state, marker string
	if err := db.QueryRow(`SELECT state,recovery_error FROM media_encryption_stage_journal WHERE stage_id=?`, stage).Scan(&state, &marker); err != nil {
		t.Fatal(err)
	}
	if state != "committed" || marker != "retirement_handoff" {
		t.Fatalf("state=%s marker=%q", state, marker)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("quarantine must remain after handoff: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE encryption_stage_id=?`, stage).Scan(&n); err != nil || n != 1 {
		t.Fatalf("retirement rows=%d err=%v", n, err)
	}
}

func TestReconcileCommittedCleanupExhaustedDoesNotStarveDueJournal(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	root, quarantineRoot := t.TempDir(), t.TempDir()
	seedCommittedCleanupJournal(t, db, root, quarantineRoot, "00000000-0000-0000-0000-000000000205", encryptionRecoveryMaxAttempts, "plaintext_cleanup_pending: permission", "2000-01-01")
	actionable := "00000000-0000-0000-0000-000000000206"
	seedCommittedCleanupJournal(t, db, root, quarantineRoot, actionable, 0, "", "2001-01-01")
	checked, _, err := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(root)}, 1)
	if err != nil || checked != 1 {
		t.Fatalf("checked=%d err=%v", checked, err)
	}
	var marker string
	if err := db.QueryRow(`SELECT recovery_error FROM media_encryption_stage_journal WHERE stage_id=?`, actionable).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "verified_committed" {
		t.Fatalf("actionable marker=%q", marker)
	}
}

func TestReconcileEncryptionStagesContinuesThroughTombstone(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, e := db.Exec(`PRAGMA foreign_keys=OFF`); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	quarantine := t.TempDir()
	source := filepath.Join(root, "source.jpg")
	if e := os.WriteFile(source, []byte("plain"), 0600); e != nil {
		t.Fatal(e)
	}
	res, e := db.Exec(`INSERT INTO library(name,type,path) VALUES('tombstone-recover','photo',?)`, root)
	if e != nil {
		t.Fatal(e)
	}
	libraryID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'tombstone',?,'image','active','processing',1)`, libraryID, source)
	if e != nil {
		t.Fatal(e)
	}
	mediaID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'repair','processing','{}',2)`, mediaID)
	if e != nil {
		t.Fatal(e)
	}
	runID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,status,required) VALUES(?,?,1,'encrypt','failed',1)`, runID, mediaID)
	if e != nil {
		t.Fatal(e)
	}
	stepID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round,removed_at,remove_reason) VALUES(?,?,?,1,'encrypt','failed',1,3,0,CURRENT_TIMESTAMP,'admin_remove')`, mediaID, runID, stepID)
	if e != nil {
		t.Fatal(e)
	}
	taskID, _ := res.LastInsertId()
	stage := "10000000-0000-0000-0000-000000000070"
	enc := filepath.Join(root, stage+".enc")
	if e = os.WriteFile(enc, []byte("enc"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,updated_at) VALUES(?,?,0,1,?,?,?,1,'owner',?,'fp',?,'wrapped','iv','hash',3,'staged','2099-01-01 00:00:00')`, stage, taskID, mediaID, runID, stepID, source, enc); e != nil {
		t.Fatal(e)
	}
	checked, cleaned, e := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantine, Resolver: fixedStageRoot(root)}, 10)
	if e != nil || checked < 1 || cleaned < 1 {
		t.Fatalf("tombstone recovery checked=%d cleaned=%d err=%v", checked, cleaned, e)
	}
	var state string
	if e = db.QueryRow(`SELECT state FROM media_encryption_stage_journal WHERE stage_id=?`, stage).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state == "staged" {
		t.Fatalf("expected recovery through tombstone, state=%s", state)
	}
	var stillThere int
	if e = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE id=? AND removed_at IS NOT NULL`, taskID).Scan(&stillThere); e != nil || stillThere != 1 {
		t.Fatalf("tombstone row must remain count=%d err=%v", stillThere, e)
	}
}

func TestReconcileEncryptionStagesRetryRoundActiveOwnerFence(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, e := db.Exec(`PRAGMA foreign_keys=OFF`); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	quarantine := t.TempDir()
	source := filepath.Join(root, "source.jpg")
	if e := os.WriteFile(source, []byte("plain"), 0600); e != nil {
		t.Fatal(e)
	}
	res, e := db.Exec(`INSERT INTO library(name,type,path) VALUES('round-fence','photo',?)`, root)
	if e != nil {
		t.Fatal(e)
	}
	libraryID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'round',?,'image','active','processing',1)`, libraryID, source)
	if e != nil {
		t.Fatal(e)
	}
	mediaID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'repair','processing','{}',2)`, mediaID)
	if e != nil {
		t.Fatal(e)
	}
	runID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,status,required) VALUES(?,?,1,'encrypt','running',1)`, runID, mediaID)
	if e != nil {
		t.Fatal(e)
	}
	stepID, _ := res.LastInsertId()
	// Current round is running; stale journal from prior round must still be recoverable.
	res, e = db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round,lease_owner) VALUES(?,?,?,1,'encrypt','running',2,3,2,'owner')`, mediaID, runID, stepID)
	if e != nil {
		t.Fatal(e)
	}
	taskID, _ := res.LastInsertId()
	staleStage := "10000000-0000-0000-0000-000000000080"
	currentStage := "10000000-0000-0000-0000-000000000081"
	staleEnc := filepath.Join(root, staleStage+".enc")
	currentEnc := filepath.Join(root, currentStage+".enc")
	for _, p := range []string{staleEnc, currentEnc} {
		if e = os.WriteFile(p, []byte("enc"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,updated_at)
VALUES(?,?,0,1,?,?,?,1,'owner',?,'fp',?,'wrapped','iv','hash',3,'staged','2099-01-01 00:00:00')`, staleStage, taskID, mediaID, runID, stepID, source, staleEnc); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,updated_at)
VALUES(?,?,2,2,?,?,?,1,'owner',?,'fp',?,'wrapped','iv','hash',3,'staged','2099-01-01 00:00:01')`, currentStage, taskID, mediaID, runID, stepID, source, currentEnc); e != nil {
		t.Fatal(e)
	}
	checked, cleaned, e := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantine, Resolver: fixedStageRoot(root)}, 10)
	if e != nil {
		t.Fatal(e)
	}
	if checked < 1 || cleaned < 1 {
		t.Fatalf("expected stale-round recovery checked=%d cleaned=%d", checked, cleaned)
	}
	var staleState, currentState string
	if e = db.QueryRow(`SELECT state FROM media_encryption_stage_journal WHERE stage_id=?`, staleStage).Scan(&staleState); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(`SELECT state FROM media_encryption_stage_journal WHERE stage_id=?`, currentStage).Scan(&currentState); e != nil {
		t.Fatal(e)
	}
	if staleState == "staged" {
		t.Fatalf("stale retry_round journal should recover, state=%s", staleState)
	}
	if currentState != "staged" {
		t.Fatalf("active retry_round journal must remain owner-fenced, state=%s", currentState)
	}
}
