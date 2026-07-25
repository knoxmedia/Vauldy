package postingest

import (
	"context"
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
	if e := cleanupCommittedEncryptionPlaintext(context.Background(), db, quarantineRoot, 1, 1, "00000000-0000-0000-0000-000000000099", path, ops); !errors.Is(e, want) {
		t.Fatalf("err=%v", e)
	}
	var marker string
	if e := db.QueryRow(`SELECT recovery_error FROM media_encryption_stage_journal WHERE stage_id='00000000-0000-0000-0000-000000000099'`).Scan(&marker); e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(marker, "plaintext_cleanup_pending:") || len(marker) > 512 {
		t.Fatalf("marker=%q", marker)
	}
	ops = defaultEncryptionFileOps()
	if e := cleanupCommittedEncryptionPlaintext(context.Background(), db, quarantineRoot, 1, 1, "00000000-0000-0000-0000-000000000099", path, ops); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(`SELECT recovery_error FROM media_encryption_stage_journal WHERE stage_id='00000000-0000-0000-0000-000000000099'`).Scan(&marker); e != nil {
		t.Fatal(e)
	}
	if marker != "verified_committed" {
		t.Fatalf("marker=%q", marker)
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
	_, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,quarantine_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state) VALUES('00000000-0000-0000-0000-000000000102',1,1,1,1,1,1,'owner',?,?,'fp',?,'wrapped','iv','hash',1,'quarantined')`, source, quarantine, filepath.Join(root, "00000000-0000-0000-0000-000000000102.enc"))
	if err != nil {
		t.Fatal(err)
	}
	original := reconcileRestoreQuarantinedPlaintext
	calls := 0
	reconcileRestoreQuarantinedPlaintext = func(q, s, root string, mediaID, generation int64, stageID string) error {
		calls++
		if calls == 1 {
			return os.ErrPermission
		}
		return restoreQuarantinedPlaintext(q, s, root, mediaID, generation, stageID)
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
		_, err := db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,quarantine_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,recovery_error,recovery_attempts,next_retry_at,updated_at) VALUES(?,?,1,1,1,1,1,'owner',?,?,'fp',?,'wrapped','iv','hash',1,'failed_closed','restore_pending: permission',?,datetime(CURRENT_TIMESTAMP,'-1 second'),?)`, stage, nextTask, source, q, filepath.Join(root, stage+".enc"), attempts, updated)
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
