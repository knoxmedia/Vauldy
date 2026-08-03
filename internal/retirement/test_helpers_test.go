package retirement

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

type fixture struct {
	DB             *sql.DB
	Root           string
	QuarantineRoot string
	SourcePath     string
	EncPath        string
	SourceFP       string
	MediaID        int64
	RunID          int64
	TaskID         int64
	StageID        string
	RetirementID   int64
}

func openRetirementDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func writeFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func seedEligibleEncryptionFixture(t *testing.T, db *sql.DB) *fixture {
	t.Helper()
	root := t.TempDir()
	fx := &fixture{
		DB:             db,
		Root:           root,
		QuarantineRoot: filepath.Join(root, "q"),
		SourcePath:     filepath.Join(root, "library", "movie.mp4"),
		EncPath:        filepath.Join(root, "library", "movie.mp4.enc"),
		StageID:        "00000000-0000-4000-8000-000000000001",
		MediaID:        1,
		RunID:          10,
		TaskID:         113,
	}
	plain := []byte("plaintext-body-for-retirement")
	writeFile(t, fx.SourcePath, plain)
	writeFile(t, fx.EncPath, []byte("ciphertext-body"))
	fp, err := storage.EncryptionSourceFingerprint(fx.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	fx.SourceFP = fp

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	exec(`INSERT INTO library(id,name,type,path,encrypted_assets_cleanup_plaintext,cleanup_local_source_after_package) VALUES(1,'lib','video',?,1,1)`, root)
	exec(`INSERT INTO media(id,library_id,file_id,file_type,file_path,ingest_generation,publication_state) VALUES(1,1,'f1','video',?,1,'published')`, fx.EncPath)
	exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'scan','published','{}',3)`)
	exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts) VALUES
 (11,10,1,1,'poster',1,'done',1,3),
 (12,10,1,1,'preview',0,'done',1,3),
 (13,10,1,1,'encrypt',1,'done',1,3)`)
	exec(`INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round) VALUES
 (112,1,10,12,1,'preview','done',1,3,0),
 (113,1,10,13,1,'encrypt','done',1,3,0)`)
	exec(`INSERT INTO media_plan_completion(run_id,media_id,generation,all_terminal,total_count,terminal_count,waiting_count,running_count,done_count,skipped_count,failed_count,cancelled_count,completed_at)
VALUES(10,1,1,1,3,3,0,0,3,0,0,0,CURRENT_TIMESTAMP)`)
	exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(1,?, 'aabb','ccdd',?,'encrypted')`, fx.EncPath, fx.SourcePath)
	exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state)
VALUES(?,113,0,1,1,10,13,1,'worker',?,?,?,'aabb','ccdd',?,?,1,'committed')`,
		fx.StageID, fx.SourcePath, fx.SourceFP, fx.EncPath, shaHex([]byte("ciphertext-body")), int64(len("ciphertext-body")))
	exec(`INSERT INTO media_ingest_evidence(media_id,run_id,step_id,generation,kind,stage_id,source_fingerprint,artifact_refs_json,verified_at)
VALUES(1,10,13,1,'encrypt',?,?,'{"path":"enc"}',CURRENT_TIMESTAMP)`, fx.StageID, fx.SourceFP)
	exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json,blocked_at)
VALUES(1,10,1,?,?,'encryption',113,?,NULL,0,'blocked','{}',CURRENT_TIMESTAMP)`, fx.SourcePath, fx.SourceFP, fx.StageID)

	if err = db.QueryRow(`SELECT id FROM media_plaintext_retirement WHERE media_id=1 AND generation=1`).Scan(&fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	return fx
}

func recompute(t *testing.T, db *sql.DB, runID int64, opts BarrierOptions) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = RecomputeRetirementBarrierTxWithOptions(context.Background(), tx, runID, opts); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func retirementState(t *testing.T, db *sql.DB, id int64) (state, blocker string) {
	t.Helper()
	if err := db.QueryRow(`SELECT state,blocker_code FROM media_plaintext_retirement WHERE id=?`, id).Scan(&state, &blocker); err != nil {
		t.Fatal(err)
	}
	return state, blocker
}

func encryptStillDone(t *testing.T, db *sql.DB, taskID int64) {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "done" {
		t.Fatalf("encrypt status=%s want done", status)
	}
}

func identityFor(fx *fixture, attempt int) Identity {
	return Identity{
		RetirementID: fx.RetirementID, MediaID: fx.MediaID, RunID: fx.RunID, Generation: 1,
		RetryRound: 0, Attempt: attempt, SourcePath: fx.SourcePath, SourceFingerprint: fx.SourceFP,
		BasisKind: BasisEncryption, BasisID: fx.TaskID, EncryptionStageID: fx.StageID,
	}
}

// Ensure publication fingerprint stays available for optional BarrierOptions.Fingerprint overrides.
var _ = publication.SourceFingerprint
