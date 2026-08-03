package postingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/publication"
	"knox-media/internal/storage"
)

func TestEncryptionCommitUpsertsRetirementAndKeepsSource(t *testing.T) {
	db, _ := openQueueTestDB(t)
	root := t.TempDir()
	plain := []byte("plaintext-for-retirement-handoff")
	source := filepath.Join(root, "plain.bin")
	encPath := filepath.Join(root, "out.enc")
	encPayload := []byte("encrypted-payload-retirement")
	if err := os.WriteFile(source, plain, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(encPath, encPayload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encPayload)
	encHash := hex.EncodeToString(sum[:])
	fp, err := storage.EncryptionSourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	stageID := "00000000-0000-0000-0000-00000000a001"

	libRes, err := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES('v','video',?,1,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	mediaRes, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'f',?,'video','active','processing',1)`, libID, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaRes.LastInsertId()
	runRes, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','processing','{}',?)`, mediaID, publication.CurrentPolicyVersion)
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
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state) VALUES(?,?,0,1,?,?,?,1,'worker',?,?,?,?,?,?,?,1,'staged')`,
		stageID, taskID, mediaID, runID, stepID, source, fp, encPath, "wrapped", "iv", encHash, int64(len(encPayload))); err != nil {
		t.Fatal(err)
	}

	run, step := runID, stepID
	task := Task{
		ID: taskID, MediaID: mediaID, Type: TaskEncrypt, Generation: 1, RetryRound: 0,
		RunID: &run, StepID: &step, LeaseOwner: "worker", Attempts: 1,
	}
	stage := storage.StagedMediaEncryption{
		StageID: stageID, MediaID: mediaID, OriginalPath: source, EncPath: encPath,
		WrappedDEK: "wrapped", IV: "iv", SHA256: encHash, Size: int64(len(encPayload)),
		SourceFingerprint: fp, CleanupPlaintext: true,
	}
	if err := commitEncryptionStage(context.Background(), db, task, stage, filepath.Join(root, "q"), EncryptionStateMachineSeams{}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var taskStatus, journalState, selected string
	if err := db.QueryRow(`SELECT p.status,j.state,m.file_path FROM post_ingest_task p JOIN media_encryption_stage_journal j ON j.task_id=p.id JOIN media m ON m.id=p.media_id WHERE p.id=?`, taskID).Scan(&taskStatus, &journalState, &selected); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "done" || journalState != "committed" || !samePathForEvidence(selected, encPath) {
		t.Fatalf("status=%s journal=%s selected=%s", taskStatus, journalState, selected)
	}
	var evidence int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_evidence WHERE media_id=? AND generation=1 AND kind='encrypt' AND stage_id=?`, mediaID, stageID).Scan(&evidence); err != nil || evidence != 1 {
		t.Fatalf("evidence=%d err=%v", evidence, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source must remain present for retirement handoff: %v", err)
	}
	var basisKind, stageRef, sourcePath, sourceFP, state string
	var basisID, generation int64
	if err := db.QueryRow(`SELECT basis_kind,basis_id,encryption_stage_id,generation,source_path,source_fingerprint,state FROM media_plaintext_retirement WHERE media_id=?`, mediaID).
		Scan(&basisKind, &basisID, &stageRef, &generation, &sourcePath, &sourceFP, &state); err != nil {
		t.Fatalf("retirement intent missing: %v", err)
	}
	if basisKind != "encryption" || basisID != taskID || stageRef != stageID || generation != 1 || state != "blocked" {
		t.Fatalf("retirement row basis_kind=%s basis_id=%d stage=%s gen=%d state=%s", basisKind, basisID, stageRef, generation, state)
	}
	if !samePathForEvidence(sourcePath, source) || sourceFP != fp {
		t.Fatalf("retirement identity path=%s fp=%s", sourcePath, sourceFP)
	}
}

func TestEncryptionCommitWithoutCleanupCreatesNoRetirement(t *testing.T) {
	db, _ := openQueueTestDB(t)
	root := t.TempDir()
	source := filepath.Join(root, "plain.bin")
	encPath := filepath.Join(root, "out.enc")
	encPayload := []byte("encrypted-no-cleanup")
	if err := os.WriteFile(source, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(encPath, encPayload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encPayload)
	encHash := hex.EncodeToString(sum[:])
	fp, err := storage.EncryptionSourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	stageID := "00000000-0000-0000-0000-00000000a002"
	libRes, err := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES('v','video',?,1,0)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	mediaRes, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'f',?,'video','active','processing',1)`, libID, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaRes.LastInsertId()
	runRes, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','processing','{}',?)`, mediaID, publication.CurrentPolicyVersion)
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
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state) VALUES(?,?,0,1,?,?,?,1,'worker',?,?,?,?,?,?,?,0,'staged')`,
		stageID, taskID, mediaID, runID, stepID, source, fp, encPath, "wrapped", "iv", encHash, int64(len(encPayload))); err != nil {
		t.Fatal(err)
	}
	run, step := runID, stepID
	task := Task{
		ID: taskID, MediaID: mediaID, Type: TaskEncrypt, Generation: 1, RetryRound: 0,
		RunID: &run, StepID: &step, LeaseOwner: "worker", Attempts: 1,
	}
	stage := storage.StagedMediaEncryption{
		StageID: stageID, MediaID: mediaID, OriginalPath: source, EncPath: encPath,
		WrappedDEK: "wrapped", IV: "iv", SHA256: encHash, Size: int64(len(encPayload)),
		SourceFingerprint: fp, CleanupPlaintext: false,
	}
	if err := commitEncryptionStage(context.Background(), db, task, stage, filepath.Join(root, "q"), EncryptionStateMachineSeams{}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source must remain when cleanup not requested: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=?`, mediaID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("retirement rows=%d err=%v", n, err)
	}
}

func TestEncryptionCommitRetirementUpsertIsIdempotent(t *testing.T) {
	db, _ := openQueueTestDB(t)
	root := t.TempDir()
	source := filepath.Join(root, "plain.bin")
	encPath := filepath.Join(root, "out.enc")
	encPayload := []byte("encrypted-idempotent")
	if err := os.WriteFile(source, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(encPath, encPayload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encPayload)
	encHash := hex.EncodeToString(sum[:])
	fp, err := storage.EncryptionSourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	stageID := "00000000-0000-0000-0000-00000000a003"
	libRes, err := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES('v','video',?,1,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	mediaRes, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'f',?,'video','active','processing',1)`, libID, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaRes.LastInsertId()
	runRes, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','processing','{}',?)`, mediaID, publication.CurrentPolicyVersion)
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
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state) VALUES(?,?,0,1,?,?,?,1,'worker',?,?,?,?,?,?,?,1,'staged')`,
		stageID, taskID, mediaID, runID, stepID, source, fp, encPath, "wrapped", "iv", encHash, int64(len(encPayload))); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json) VALUES(?,?,1,?,?,'encryption',?,?,NULL,0,'blocked','{}')`,
		mediaID, runID, source, fp, taskID, stageID); err != nil {
		t.Fatal(err)
	}
	run, step := runID, stepID
	task := Task{
		ID: taskID, MediaID: mediaID, Type: TaskEncrypt, Generation: 1, RetryRound: 0,
		RunID: &run, StepID: &step, LeaseOwner: "worker", Attempts: 1,
	}
	stage := storage.StagedMediaEncryption{
		StageID: stageID, MediaID: mediaID, OriginalPath: source, EncPath: encPath,
		WrappedDEK: "wrapped", IV: "iv", SHA256: encHash, Size: int64(len(encPayload)),
		SourceFingerprint: fp, CleanupPlaintext: true,
	}
	if err := commitEncryptionStage(context.Background(), db, task, stage, filepath.Join(root, "q"), EncryptionStateMachineSeams{}); err != nil {
		t.Fatalf("commit with existing retirement: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=? AND generation=1`, mediaID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("retirement rows=%d err=%v", n, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, taskID).Scan(&status); err != nil || status != "done" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}

func TestCommittedEncryptionRecoveryHandsOffRetirementInsteadOfDeleting(t *testing.T) {
	db, _ := openQueueTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	root, quarantineRoot := t.TempDir(), t.TempDir()
	stage := "00000000-0000-0000-0000-000000000301"
	path := seedCommittedCleanupJournal(t, db, root, quarantineRoot, stage, 0, "", "2000-01-01")
	if err := os.WriteFile(path, []byte("quarantined-plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_encryption_stage_journal SET cleanup_plaintext=1,source_fingerprint='fp-hand-off' WHERE stage_id=?`, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_run SET policy_version=? WHERE id=(SELECT run_id FROM media_encryption_stage_journal WHERE stage_id=?)`, publication.CurrentPolicyVersion, stage); err != nil {
		t.Fatal(err)
	}
	checked, cleaned, err := ReconcileEncryptionStages(context.Background(), db, EncryptionRecoveryRoots{Quarantine: quarantineRoot, Resolver: fixedStageRoot(root)}, 100)
	if err != nil || checked != 1 || cleaned != 0 {
		t.Fatalf("checked=%d cleaned=%d err=%v", checked, cleaned, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("quarantine must not be deleted by encryption recovery: %v", err)
	}
	var marker string
	if err := db.QueryRow(`SELECT recovery_error FROM media_encryption_stage_journal WHERE stage_id=?`, stage).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "retirement_handoff" {
		t.Fatalf("marker=%q want retirement_handoff", marker)
	}
	var n int
	var basisKind, stageRef, sourcePath, qPath, evidence string
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(basis_kind),''),COALESCE(MAX(encryption_stage_id),''),COALESCE(MAX(source_path),''),COALESCE(MAX(quarantine_path),''),COALESCE(MAX(quarantine_evidence_json),'') FROM media_plaintext_retirement WHERE encryption_stage_id=?`, stage).Scan(&n, &basisKind, &stageRef, &sourcePath, &qPath, &evidence); err != nil {
		t.Fatal(err)
	}
	if n != 1 || basisKind != "encryption" || stageRef != stage {
		t.Fatalf("retirement n=%d kind=%s stage=%s", n, basisKind, stageRef)
	}
	var journalSource string
	if err := db.QueryRow(`SELECT source_path FROM media_encryption_stage_journal WHERE stage_id=?`, stage).Scan(&journalSource); err != nil {
		t.Fatal(err)
	}
	if !samePathForEvidence(sourcePath, journalSource) {
		t.Fatalf("source_path=%s want journal original %s", sourcePath, journalSource)
	}
	if !samePathForEvidence(qPath, path) {
		t.Fatalf("quarantine_path=%s want %s", qPath, path)
	}
	if !strings.Contains(evidence, "encryption_quarantine_path") {
		t.Fatalf("quarantine_evidence_json=%s missing encryption quarantine ref", evidence)
	}
}

func TestUpsertEncryptionRetirementFailsClosedOnMismatchedActiveState(t *testing.T) {
	db, task, stage := seedEncryptionRetirementUpsertFixture(t)
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json)
VALUES(?,?,1,'other-source','other-fp','encryption',?,?,NULL,0,'quarantining','{}')`, task.MediaID, *task.RunID, task.ID+99, "00000000-0000-0000-0000-00000000b099"); err != nil {
		// FK may reject unknown stage — insert a matching journal first under FK off.
		t.Fatal(err)
	}
	err := upsertEncryptionRetirementIntentTx(context.Background(), db, task, stage, "")
	if err == nil {
		t.Fatal("expected fail-closed on mismatched active retirement")
	}
	var state, stageRef string
	if e := db.QueryRow(`SELECT state,encryption_stage_id FROM media_plaintext_retirement WHERE media_id=? AND generation=?`, task.MediaID, task.Generation).Scan(&state, &stageRef); e != nil {
		t.Fatal(e)
	}
	if state != "quarantining" || stageRef == stage.StageID {
		t.Fatalf("active row mutated: state=%s stage=%s", state, stageRef)
	}
}

func TestUpsertEncryptionRetirementIdempotentOnMatchingActiveState(t *testing.T) {
	db, task, stage := seedEncryptionRetirementUpsertFixture(t)
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_path,quarantine_evidence_json)
VALUES(?,?,1,?,?,'encryption',?,?,NULL,0,'deleting','prior-q','{"prior":true}')`, task.MediaID, *task.RunID, stage.OriginalPath, stage.SourceFingerprint, task.ID, stage.StageID); err != nil {
		t.Fatal(err)
	}
	if err := upsertEncryptionRetirementIntentTx(context.Background(), db, task, stage, filepath.Join(t.TempDir(), "q")); err != nil {
		t.Fatalf("matching active retirement should be idempotent success: %v", err)
	}
	var state, qPath string
	if err := db.QueryRow(`SELECT state,quarantine_path FROM media_plaintext_retirement WHERE media_id=? AND generation=?`, task.MediaID, task.Generation).Scan(&state, &qPath); err != nil {
		t.Fatal(err)
	}
	if state != "deleting" {
		t.Fatalf("state regressed to %s", state)
	}
	if qPath != "prior-q" {
		t.Fatalf("in-flight quarantine overwritten: %s", qPath)
	}
}

func TestUpsertEncryptionRetirementConflictWhereZeroRowsFailsWithoutMatch(t *testing.T) {
	db, task, stage := seedEncryptionRetirementUpsertFixture(t)
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json)
VALUES(?,?,1,?,?,'encryption',?,?,NULL,0,'verified','{}')`, task.MediaID, *task.RunID, "different", "different-fp", task.ID, stage.StageID); err != nil {
		t.Fatal(err)
	}
	if err := upsertEncryptionRetirementIntentTx(context.Background(), db, task, stage, ""); err == nil {
		t.Fatal("verified row with mismatched fingerprint must fail closed")
	}
}

func TestPreV3CommitCleanupErrorDoesNotFailEncrypt(t *testing.T) {
	db, _ := openQueueTestDB(t)
	root := t.TempDir()
	source := filepath.Join(root, "plain.bin")
	encPath := filepath.Join(root, "out.enc")
	encPayload := []byte("encrypted-prev3")
	if err := os.WriteFile(source, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(encPath, encPayload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encPayload)
	encHash := hex.EncodeToString(sum[:])
	fp, err := storage.EncryptionSourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	stageID := "00000000-0000-0000-0000-00000000a010"
	libRes, err := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES('v','video',?,1,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	mediaRes, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'f',?,'video','active','processing',1)`, libID, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaRes.LastInsertId()
	runRes, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','processing','{}',2)`, mediaID)
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
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state) VALUES(?,?,0,1,?,?,?,1,'worker',?,?,?,?,?,?,?,1,'staged')`,
		stageID, taskID, mediaID, runID, stepID, source, fp, encPath, "wrapped", "iv", encHash, int64(len(encPayload))); err != nil {
		t.Fatal(err)
	}
	run, step := runID, stepID
	task := Task{
		ID: taskID, MediaID: mediaID, Type: TaskEncrypt, Generation: 1, RetryRound: 0,
		RunID: &run, StepID: &step, LeaseOwner: "worker", Attempts: 1,
	}
	stage := storage.StagedMediaEncryption{
		StageID: stageID, MediaID: mediaID, OriginalPath: source, EncPath: encPath,
		WrappedDEK: "wrapped", IV: "iv", SHA256: encHash, Size: int64(len(encPayload)),
		SourceFingerprint: fp, CleanupPlaintext: true,
	}
	original := encryptionFileOpsForCleanup
	encryptionFileOpsForCleanup = func() encryptionFileOps {
		ops := defaultEncryptionFileOps()
		ops.syncDir = func(string) error { return errors.New("injected cleanup sync failure") }
		return ops
	}
	t.Cleanup(func() { encryptionFileOpsForCleanup = original })

	if err := commitEncryptionStage(context.Background(), db, task, stage, filepath.Join(root, "q"), EncryptionStateMachineSeams{}); err != nil {
		t.Fatalf("cleanup error must not fail encrypt commit: %v", err)
	}
	var status, marker string
	if err := db.QueryRow(`SELECT p.status,j.recovery_error FROM post_ingest_task p JOIN media_encryption_stage_journal j ON j.task_id=p.id WHERE p.id=?`, taskID).Scan(&status, &marker); err != nil {
		t.Fatal(err)
	}
	if status != "done" {
		t.Fatalf("status=%s want done", status)
	}
	if !strings.HasPrefix(marker, "plaintext_cleanup_pending:") {
		t.Fatalf("marker=%q want plaintext_cleanup_pending", marker)
	}
}

func seedEncryptionRetirementUpsertFixture(t *testing.T) (*sql.DB, Task, storage.StagedMediaEncryption) {
	t.Helper()
	db, _ := openQueueTestDB(t)
	root := t.TempDir()
	source := filepath.Join(root, "plain.bin")
	if err := os.WriteFile(source, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	stageID := "00000000-0000-0000-0000-00000000b001"
	libRes, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('v','video',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	mediaRes, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'f',?,'video','active','processing',1)`, libID, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaRes.LastInsertId()
	runRes, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'scan','processing','{}',?)`, mediaID, publication.CurrentPolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runRes.LastInsertId()
	stepRes, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'encrypt',1,'done')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := stepRes.LastInsertId()
	taskRes, err := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts) VALUES(?,?,?,1,'encrypt','done',1)`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := taskRes.LastInsertId()
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state) VALUES(?,?,0,1,?,?,?,1,'worker',?,'fp',?,'dek','iv','hash',1,1,'committed')`,
		stageID, taskID, mediaID, runID, stepID, source, filepath.Join(root, "out.enc")); err != nil {
		t.Fatal(err)
	}
	// Extra journal for mismatch cases that need a different stage FK target.
	otherStage := "00000000-0000-0000-0000-00000000b099"
	if _, err = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state) VALUES(?,?,0,2,?,?,?,1,'worker',?,'fp2',?,'dek','iv','hash',1,'committed')`,
		otherStage, taskID, mediaID, runID, stepID, source, filepath.Join(root, "out2.enc")); err != nil {
		t.Fatal(err)
	}
	run, step := runID, stepID
	task := Task{ID: taskID, MediaID: mediaID, Type: TaskEncrypt, Generation: 1, RetryRound: 0, RunID: &run, StepID: &step, Attempts: 1}
	stage := storage.StagedMediaEncryption{
		StageID: stageID, MediaID: mediaID, OriginalPath: source, SourceFingerprint: "fp", CleanupPlaintext: true,
	}
	return db, task, stage
}
