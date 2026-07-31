package postingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"knox-media/internal/storage"
	"knox-media/internal/store"
)

func TestCommitEncryptionStageHashesOutsideImmediateTx(t *testing.T) {
	db, _ := openQueueTestDB(t)
	root := t.TempDir()
	encPath := filepath.Join(root, "out.enc")
	payload := []byte("encrypted-payload-for-commit-lock-test")
	if err := os.WriteFile(encPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	encHash := hex.EncodeToString(sum[:])
	stageID := "00000000-0000-0000-0000-00000000c001"
	plainPath := filepath.Join(root, "gone.mp4")

	libRes, err := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled) VALUES('v','video',?,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	mediaRes, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'f',?,'video','active','processing',1)`, libID, encPath)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaRes.LastInsertId()
	runRes, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,'1','scan','processing','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runRes.LastInsertId()
	stepRes, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,max_attempts,lease_owner) VALUES(?,?,1,'encrypt',1,'running',1,3,'worker')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := stepRes.LastInsertId()
	taskRes, err := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,lease_owner,retry_round) VALUES(?,?,?,1,'encrypt','running',1,3,'worker',0)`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := taskRes.LastInsertId()
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state) VALUES(?,?,1,?,?,?,1,'worker',?,?,?,?,?,?,?,'staged')`,
		stageID, taskID, mediaID, runID, stepID, plainPath, "sha256:dead", encPath, "wrapped", "iv", encHash, int64(len(payload))); err != nil {
		t.Fatal(err)
	}

	var inImmediate atomic.Bool
	var hashCallsInside atomic.Int64
	originalHash := encryptionCommitHashPath
	encryptionCommitHashPath = func(path string) (int64, string, error) {
		if inImmediate.Load() {
			hashCallsInside.Add(1)
		}
		return originalHash(path)
	}
	t.Cleanup(func() { encryptionCommitHashPath = originalHash })

	seams := EncryptionStateMachineSeams{
		ImmediateTx: func(ctx context.Context, db *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
			inImmediate.Store(true)
			defer inImmediate.Store(false)
			return store.WithImmediateConnTx(ctx, db, fn)
		},
	}
	run, step := runID, stepID
	task := Task{
		ID: taskID, MediaID: mediaID, Type: TaskEncrypt, Generation: 1, RetryRound: 0,
		RunID: &run, StepID: &step, LeaseOwner: "worker", Attempts: 1,
	}
	stage := storage.StagedMediaEncryption{
		StageID:           stageID,
		MediaID:           mediaID,
		OriginalPath:      plainPath,
		EncPath:           encPath,
		WrappedDEK:        "wrapped",
		IV:                "iv",
		SHA256:            encHash,
		Size:              int64(len(payload)),
		SourceFingerprint: "sha256:dead",
	}
	if err := commitEncryptionStage(context.Background(), db, task, stage, filepath.Join(root, "q"), seams); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := hashCallsInside.Load(); n != 0 {
		t.Fatalf("enc hash calls inside IMMEDIATE tx = %d, want 0 (hashing multi-GB files under write lock starves SQLite)", n)
	}
}
