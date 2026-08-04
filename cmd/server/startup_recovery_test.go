package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"knox-media/internal/postingest"
	"knox-media/internal/retirement"
	"knox-media/internal/storage"
	"knox-media/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type startupRecoveryStageRoot string

func (r startupRecoveryStageRoot) ResolveEncryptionStageRoot(context.Context, int64, string) (string, error) {
	return string(r), nil
}

func startupRecoveryRoots(t *testing.T, scrapeArtwork string) StartupRecoveryRoots {
	t.Helper()
	root := t.TempDir()
	return StartupRecoveryRoots{
		Encryption: postingest.EncryptionRecoveryRoots{
			Quarantine: filepath.Join(root, "encryption-quarantine"),
			Resolver:   startupRecoveryStageRoot(filepath.Join(root, "encryption-stages")),
		},
		Thumbnail: postingest.ThumbnailRecoveryRoots{
			Preview: filepath.Join(root, "previews"),
			Derived: filepath.Join(root, "derived"),
		},
		Poster: postingest.PosterRecoveryRoots{
			Upload:  filepath.Join(root, "uploads"),
			Derived: filepath.Join(root, "derived"),
		},
		ScrapeArtwork: scrapeArtwork,
		Retirement: retirement.RecoveryOptions{
			QuarantineRoot: filepath.Join(root, "retirement-quarantine"),
		},
	}
}

func TestStartupRecoveryEncryptionBeforeRetirementReconcile(t *testing.T) {
	data, err := os.ReadFile("startup_recovery.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	encAt := strings.Index(src, "postingest.ReconcileEncryptionStages(")
	retAt := strings.Index(src, "retirement.ReconcileStartup(")
	if encAt < 0 || retAt < 0 || encAt > retAt {
		t.Fatalf("encryption quarantine reconcile must precede retirement.ReconcileStartup (enc=%d ret=%d)", encAt, retAt)
	}
}

func TestRecoverStartupArtifactsReconcilesRetirementBeforeClaims(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "retire-startup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	roots := startupRecoveryRoots(t, "")
	libRoot := t.TempDir()
	sourcePath := filepath.Join(libRoot, "movie.mp4")
	encPath := filepath.Join(libRoot, "movie.mp4.enc")
	if err := os.MkdirAll(libRoot, 0755); err != nil {
		t.Fatal(err)
	}
	plain := []byte("plaintext-body-for-startup-retirement")
	if err := os.WriteFile(sourcePath, plain, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(encPath, []byte("ciphertext-body"), 0600); err != nil {
		t.Fatal(err)
	}
	fp, err := storage.EncryptionSourceFingerprint(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("ciphertext-body"))
	encSHA := hex.EncodeToString(sum[:])
	stageID := "00000000-0000-4000-8000-000000000099"
	qReserved := filepath.Join(roots.Retirement.QuarantineRoot, "reserved")

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	exec(`INSERT INTO library(id,name,type,path,encrypted_assets_cleanup_plaintext) VALUES(1,'lib','video',?,1)`, libRoot)
	exec(`INSERT INTO media(id,library_id,file_id,file_type,file_path,ingest_generation,publication_state) VALUES(1,1,'f1','video',?,1,'published')`, encPath)
	exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'scan','published','{}',3)`)
	exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts) VALUES
 (11,10,1,1,'poster',1,'done',1,3),(13,10,1,1,'encrypt',1,'done',1,3)`)
	exec(`INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round) VALUES
 (113,1,10,13,1,'encrypt','done',1,3,0)`)
	exec(`INSERT INTO media_plan_completion(run_id,media_id,generation,all_terminal,total_count,terminal_count,waiting_count,running_count,done_count,skipped_count,failed_count,cancelled_count,completed_at)
VALUES(10,1,1,1,2,2,0,0,2,0,0,0,CURRENT_TIMESTAMP)`)
	exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(1,?,'aabb','ccdd',?,'encrypted')`, encPath, sourcePath)
	exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state,recovery_error,recovery_attempts)
VALUES(?,113,0,1,1,10,13,1,'worker',?,?,?,'aabb','ccdd',?,?,1,'committed','retirement_handoff',0)`, stageID, sourcePath, fp, encPath, encSHA, int64(len("ciphertext-body")))
	exec(`INSERT INTO media_ingest_evidence(media_id,run_id,step_id,generation,kind,stage_id,source_fingerprint,artifact_refs_json,verified_at)
VALUES(1,10,13,1,'encrypt',?,?,'{}',CURRENT_TIMESTAMP)`, stageID, fp)
	exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,attempts,quarantine_path,quarantine_evidence_json,lease_owner)
VALUES(1,10,1,?,?,'encryption',113,?,NULL,0,'quarantining',1,?,'{}','dead')`, sourcePath, fp, stageID, qReserved)

	var retirementID int64
	if err := db.QueryRow(`SELECT id FROM media_plaintext_retirement WHERE media_id=1`).Scan(&retirementID); err != nil {
		t.Fatal(err)
	}

	claimed := false
	ok := func(context.Context) error { return nil }
	hooks := publicationV2StartupHooks{
		RecoverArtifacts: func(ctx context.Context) error {
			return recoverStartupArtifacts(ctx, db, roots)
		},
		RecoverLeases: ok, RecoverReservations: ok, ReplaceActiveV1: ok, ValidateAggregateV2: ok,
		Preflight:     func(context.Context) ([]string, error) { return nil, nil },
		StartClaimers: func() { claimed = true }, StartSubmissionSources: func() {},
	}
	if _, err := PreparePublicationV2Startup(context.Background(), hooks); err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("claimers should start after successful retirement filesystem reconcile")
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM media_plaintext_retirement WHERE id=?`, retirementID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "quarantining" {
		t.Fatal("retirement filesystem reconcile must run before claims; state still quarantining")
	}
}

func TestStartupRecoveryRetirementFailureBlocksClaims(t *testing.T) {
	claimed, sourced := false, false
	ok := func(context.Context) error { return nil }
	hooks := publicationV2StartupHooks{
		RecoverArtifacts: func(context.Context) error {
			return errors.New("startup recovery: retirement: reconcile failed")
		},
		RecoverLeases: ok, RecoverReservations: ok, ReplaceActiveV1: ok, ValidateAggregateV2: ok,
		Preflight:     func(context.Context) ([]string, error) { return nil, nil },
		StartClaimers: func() { claimed = true }, StartSubmissionSources: func() { sourced = true },
	}
	if _, err := PreparePublicationV2Startup(context.Background(), hooks); err == nil {
		t.Fatal("expected retirement startup failure")
	}
	if claimed || sourced {
		t.Fatalf("claimed=%v sourced=%v", claimed, sourced)
	}
}

func TestRecoverStartupTasksCleansScrapeStagesBeforeQueueReset(t *testing.T) {
	db, e := store.OpenSQLite(filepath.Join(t.TempDir(), "r.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	root := t.TempDir()
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');INSERT INTO media(id,library_id,file_id,file_type,ingest_generation) VALUES(1,1,'f','video',1);INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json) VALUES(1,1,1,'scan','processing','{}');INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(1,1,1,1,'scrape',0,'running')`)
	stale := filepath.Join(root, "stale")
	_ = os.MkdirAll(stale, 0755)
	_ = os.WriteFile(filepath.Join(stale, "poster.jpg"), []byte("x"), 0644)
	_, _ = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('stale',1,1,1,1,'dead','fp','scrape_artwork','staged',?,'{}')`, stale)
	_, _ = db.Exec(`UPDATE media_asset_stage_journal SET updated_at=datetime(CURRENT_TIMESTAMP,'-11 minutes') WHERE stage_id='stale'`)
	if e = recoverStartupTasks(context.Background(), db, postingest.NewQueue(db, "r", nil), startupRecoveryRoots(t, root)); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(stale); !os.IsNotExist(e) {
		t.Fatalf("stale retained: %v", e)
	}
}

func TestRecoverStartupTasksBackfillsMissingDomainTasks(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "domain-backfill.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
		INSERT INTO media(id,library_id,file_id,file_type,ingest_generation) VALUES(1,1,'f','video',1);
		INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts) VALUES(1,1,'subtitle','waiting',3)`); err != nil {
		t.Fatal(err)
	}
	if err := recoverStartupTasks(context.Background(), db, postingest.NewQueue(db, "r", nil), startupRecoveryRoots(t, "")); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM subtitle_task WHERE media_id=1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("status=%q, want pending", status)
	}
}
