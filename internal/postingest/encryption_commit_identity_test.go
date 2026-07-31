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
