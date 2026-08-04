package postingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/storage"

	_ "modernc.org/sqlite"
)

func TestSelectedEncryptionStageMatchingQuickIdentitySkipsFullFingerprint(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`
CREATE TABLE media(id INTEGER PRIMARY KEY, file_path TEXT NOT NULL, ingest_generation INTEGER NOT NULL);
CREATE TABLE media_ingest_run(id INTEGER PRIMARY KEY, status TEXT NOT NULL, superseded_at TEXT, superseded_by_generation INTEGER);
CREATE TABLE media_ingest_step(id INTEGER PRIMARY KEY, status TEXT NOT NULL, lease_owner TEXT, attempts INTEGER NOT NULL);
CREATE TABLE post_ingest_task(id INTEGER PRIMARY KEY, media_id INTEGER NOT NULL, generation INTEGER NOT NULL, retry_round INTEGER NOT NULL, ingest_run_id INTEGER, ingest_step_id INTEGER, status TEXT NOT NULL, lease_owner TEXT, attempts INTEGER NOT NULL);
INSERT INTO media_ingest_run VALUES(11,'processing',NULL,0);
INSERT INTO media_ingest_step VALUES(12,'running','worker',3);
`); err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(t.TempDir(), "plain.bin")
	if err = os.WriteFile(source, []byte("plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media VALUES(41,?,2)`, source); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO post_ingest_task VALUES(7,41,2,4,11,12,'running','worker',3)`); err != nil {
		t.Fatal(err)
	}
	identity, err := storage.QuickSourceIdentity(source)
	if err != nil {
		t.Fatal(err)
	}

	originalFingerprint := encryptionSourceFingerprint
	fullHashCalls := 0
	encryptionSourceFingerprint = func(string) (string, error) {
		fullHashCalls++
		return "unexpected", nil
	}
	t.Cleanup(func() { encryptionSourceFingerprint = originalFingerprint })

	runID, stepID := int64(11), int64(12)
	task := Task{
		ID: 7, MediaID: 41, Type: TaskEncrypt, Generation: 2, RetryRound: 4,
		RunID: &runID, StepID: &stepID, LeaseOwner: "worker", Attempts: 3,
	}
	stage := storage.StagedMediaEncryption{
		OriginalPath:      source,
		EncPath:           filepath.Join(t.TempDir(), "stage.enc"),
		SourceIdentity:    identity,
		SourceFingerprint: "captured-at-stage",
	}
	selected, err := selectedEncryptionStage(context.Background(), db, task, stage)
	if err != nil {
		t.Fatal(err)
	}
	if selected {
		t.Fatal("plaintext source was reported as already selected encrypted output")
	}
	if fullHashCalls != 0 {
		t.Fatalf("full fingerprint calls=%d want 0", fullHashCalls)
	}
}

func TestSelectedEncryptionStageIdentityMismatchReturnsError(t *testing.T) {
	db, source, task := selectedEncryptionIdentityFixture(t)

	originalFingerprint := encryptionSourceFingerprint
	encryptionSourceFingerprint = func(string) (string, error) {
		return "captured-at-stage", nil
	}
	t.Cleanup(func() { encryptionSourceFingerprint = originalFingerprint })

	stage := storage.StagedMediaEncryption{
		OriginalPath:      source,
		EncPath:           filepath.Join(t.TempDir(), "stage.enc"),
		SourceIdentity:    "different-identity",
		SourceFingerprint: "captured-at-stage",
	}
	if _, err := selectedEncryptionStage(context.Background(), db, task, stage); err == nil {
		t.Fatal("expected changed source identity error")
	}
}

func TestSelectedEncryptionStageEmptyIdentityFallsBackToFullFingerprint(t *testing.T) {
	db, source, task := selectedEncryptionIdentityFixture(t)

	originalFingerprint := encryptionSourceFingerprint
	fullHashCalls := 0
	encryptionSourceFingerprint = func(string) (string, error) {
		fullHashCalls++
		return "captured-at-stage", nil
	}
	t.Cleanup(func() { encryptionSourceFingerprint = originalFingerprint })

	stage := storage.StagedMediaEncryption{
		OriginalPath:      source,
		EncPath:           filepath.Join(t.TempDir(), "stage.enc"),
		SourceFingerprint: "captured-at-stage",
	}
	selected, err := selectedEncryptionStage(context.Background(), db, task, stage)
	if err != nil {
		t.Fatal(err)
	}
	if selected {
		t.Fatal("plaintext source was reported as already selected encrypted output")
	}
	if fullHashCalls != 1 {
		t.Fatalf("full fingerprint calls=%d want 1", fullHashCalls)
	}
}

func TestLoadJournalEncryptionStageReusesResumeIdentityAndInsertIsIdempotent(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if err := storage.EnsureEncryptResumeSchema(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	source := filepath.Join(root, "plain.bin")
	encPath := filepath.Join(root, "stage.enc")
	if err := os.WriteFile(source, []byte("plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(encPath, []byte("encrypted"), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := storage.QuickSourceIdentity(source)
	if err != nil {
		t.Fatal(err)
	}

	runID, stepID := int64(11), int64(12)
	task := Task{
		ID: 7, MediaID: 41, Type: TaskEncrypt, Generation: 2,
		RunID: &runID, StepID: &stepID, LeaseOwner: "worker", Attempts: 3,
	}
	stageID := "10000000-0000-0000-0000-000000000001"
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'staged')`,
		stageID, task.ID, task.Attempts, task.MediaID, runID, stepID, task.Generation, task.LeaseOwner,
		source, "captured-at-stage", encPath, "wrapped", "iv", "hash", 9, 1); err != nil {
		t.Fatal(err)
	}
	if err = storage.UpsertEncryptResume(context.Background(), db, storage.EncryptResumeRow{
		MediaID: task.MediaID, Generation: task.Generation, StageID: stageID,
		EncPath: encPath, SourcePath: source, SourceIdentity: identity,
		WrappedDEK: "wrapped", IV: "iv", PlainOffset: 9, EncBytesWritten: 9, State: "staged",
	}); err != nil {
		t.Fatal(err)
	}

	stage, err := loadJournalEncryptionStage(context.Background(), db, task)
	if err != nil {
		t.Fatal(err)
	}
	if stage.StageID != stageID || stage.SourceIdentity != identity || stage.SourceFingerprint != "captured-at-stage" {
		t.Fatalf("loaded stage=%+v", stage)
	}
	if err = insertEncryptionStageJournal(context.Background(), db, task, stage); err != nil {
		t.Fatalf("reinsert existing journal stage: %v", err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM media_encryption_stage_journal WHERE stage_id=?`, stageID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("journal rows=%d want 1", count)
	}
}

func selectedEncryptionIdentityFixture(t *testing.T) (*sql.DB, string, Task) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`
CREATE TABLE media(id INTEGER PRIMARY KEY, file_path TEXT NOT NULL, ingest_generation INTEGER NOT NULL);
CREATE TABLE media_ingest_run(id INTEGER PRIMARY KEY, status TEXT NOT NULL, superseded_at TEXT, superseded_by_generation INTEGER);
CREATE TABLE media_ingest_step(id INTEGER PRIMARY KEY, status TEXT NOT NULL, lease_owner TEXT, attempts INTEGER NOT NULL);
CREATE TABLE post_ingest_task(id INTEGER PRIMARY KEY, media_id INTEGER NOT NULL, generation INTEGER NOT NULL, retry_round INTEGER NOT NULL, ingest_run_id INTEGER, ingest_step_id INTEGER, status TEXT NOT NULL, lease_owner TEXT, attempts INTEGER NOT NULL);
INSERT INTO media_ingest_run VALUES(11,'processing',NULL,0);
INSERT INTO media_ingest_step VALUES(12,'running','worker',3);
`); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "plain.bin")
	if err = os.WriteFile(source, []byte("plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media VALUES(41,?,2)`, source); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO post_ingest_task VALUES(7,41,2,4,11,12,'running','worker',3)`); err != nil {
		t.Fatal(err)
	}
	runID, stepID := int64(11), int64(12)
	return db, source, Task{
		ID: 7, MediaID: 41, Type: TaskEncrypt, Generation: 2, RetryRound: 4,
		RunID: &runID, StepID: &stepID, LeaseOwner: "worker", Attempts: 3,
	}
}
