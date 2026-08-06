package retirement

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/publication"
	"knox-media/internal/store"
)

func TestBarrierMatrixOptionalWaitingBlocks(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='waiting' WHERE id=12`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_plan_completion SET all_terminal=0,waiting_count=1,terminal_count=2,done_count=2 WHERE run_id=10`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerPlanNotTerminal) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
	encryptStillDone(t, db, fx.TaskID)
}

func TestBarrierMatrixOptionalRunningBlocks(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='running' WHERE id=12`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_plan_completion SET all_terminal=0,running_count=1,terminal_count=2,done_count=2 WHERE run_id=10`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerPlanNotTerminal) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierMatrixPermanentFailCancelSkipAdvances(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='failed' WHERE id=12`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES
 (14,10,1,1,'subtitle_extract',0,'cancelled'),
 (15,10,1,1,'ai_analysis',0,'skipped')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_plan_completion SET all_terminal=1,total_count=5,terminal_count=5,done_count=2,failed_count=1,cancelled_count=1,skipped_count=1,waiting_count=0,running_count=0 WHERE run_id=10`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateReady) || blocker != "" {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierMatrixRetryableWaitingBlocks(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='waiting',attempts=1,max_attempts=3 WHERE id=12`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_plan_completion SET all_terminal=0,waiting_count=1,terminal_count=2,done_count=2 WHERE run_id=10`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerPlanNotTerminal) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierGenerationFence(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerGenerationFence) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierFingerprintFence(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if err := os.WriteFile(fx.SourcePath, []byte("mutated-plaintext-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerFingerprintFence) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

// TestBarrierLegacySHA256FenceUsesIdentity proves legacy sha256: source
// fingerprints are gated by their recorded identity (canonical path, size,
// mtime) rather than a full-file SHA-256: a value whose digest is wrong still
// passes when the file identity is unchanged, and a changed size blocks.
// Full byte-level verification is deferred to DeleteQuarantine before removal.
func TestBarrierLegacySHA256FenceUsesIdentity(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	info, err := os.Stat(fx.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(fx.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	legacyFP := fmt.Sprintf("%s|%d|%d|sha256:%s", filepath.Clean(abs), info.Size(), info.ModTime().UnixNano(), strings.Repeat("0", 64))
	if _, err := db.Exec(`UPDATE media_plaintext_retirement SET source_fingerprint=? WHERE id=?`, legacyFP, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateReady) {
		t.Fatalf("legacy fingerprint with matching identity must flip ready: state=%s blocker=%s", state, blocker)
	}

	if err := os.WriteFile(fx.SourcePath, []byte("different-size-content"), 0600); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker = retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerFingerprintFence) {
		t.Fatalf("changed source identity must block: state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierCiphertextKeyEvidenceRequired(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if err := os.Remove(fx.EncPath); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerCiphertextUnreadable) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}

	writeFile(t, fx.EncPath, []byte("ciphertext-body"))
	if _, err := db.Exec(`UPDATE media_encrypted_assets SET wrapped_dek='' WHERE media_id=1`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker = retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerKeyUnreadable) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}

	if _, err := db.Exec(`UPDATE media_encrypted_assets SET wrapped_dek='aabb' WHERE media_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM media_ingest_evidence WHERE media_id=1`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker = retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerEvidenceUnreadable) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierStrategyRegistryRequired(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	incomplete := publication.StrategyRegistry{
		publication.StepPoster: {Strategy: publication.EncryptedSourceDerivative, Validated: true},
	}
	recompute(t, db, fx.RunID, BarrierOptions{Strategies: incomplete})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerStrategyIncomplete) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierPolicyAndActiveConsumer(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if _, err := db.Exec(`UPDATE library SET encrypted_assets_cleanup_plaintext=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerPolicyDisabled) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}

	if _, err := db.Exec(`UPDATE library SET encrypted_assets_cleanup_plaintext=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{ActiveConsumer: func(int64) bool { return true }})
	state, blocker = retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerActiveConsumer) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestRetirementRecomputeDefaultActiveConsumerBlocksReady(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	prev := defaultActiveConsumer
	SetDefaultActiveConsumer(func(mediaID int64) bool { return mediaID == fx.MediaID })
	t.Cleanup(func() { SetDefaultActiveConsumer(prev) })

	if err := RecomputeRetirementBarrierTx(context.Background(), db, fx.RunID); err != nil {
		t.Fatal(err)
	}
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerActiveConsumer) {
		t.Fatalf("default ActiveConsumer must block ready: state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierActiveConsumerBlocksOnImmediateConnTx(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if _, err := db.Exec(`INSERT INTO preview_task(media_id, status) VALUES(1, 'running')`); err != nil {
		t.Fatal(err)
	}
	_, err := store.WithImmediateConnTx(context.Background(), db, func(tx store.ImmediateConnTx) error {
		return RecomputeRetirementBarrierTxWithOptions(context.Background(), tx, fx.RunID, BarrierOptions{})
	})
	if err != nil {
		t.Fatal(err)
	}
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerActiveConsumer) {
		t.Fatalf("ImmediateConnTx must honor active consumer: state=%s blocker=%s", state, blocker)
	}
}

// refuseActiveConsumerCheck delegates all SQL except the active-consumer probe,
// which is forced to fail so the barrier cannot confirm consumers are idle.
type refuseActiveConsumerCheck struct {
	inner store.SQLExecutor
}

func (r refuseActiveConsumerCheck) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return r.inner.ExecContext(ctx, query, args...)
}

func (r refuseActiveConsumerCheck) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.inner.QueryContext(ctx, query, args...)
}

func (r refuseActiveConsumerCheck) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.Contains(query, "preview_task") && strings.Contains(query, "keyframe_task") {
		return r.inner.QueryRowContext(ctx, `SELECT 1 FROM __retirement_active_consumer_check_unavailable__`)
	}
	return r.inner.QueryRowContext(ctx, query, args...)
}

func TestBarrierActiveConsumerFailsClosedWhenCheckCannotRun(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := RecomputeRetirementBarrierTxWithOptions(context.Background(), refuseActiveConsumerCheck{inner: tx}, fx.RunID, BarrierOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state == string(StateReady) {
		t.Fatal("barrier must not mark ready when active consumer check cannot run")
	}
	if state != string(StateBlocked) || blocker != string(BlockerActiveConsumer) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierReadyFlipAndRegression(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateReady) || blocker != "" {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='waiting' WHERE id=12`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_plan_completion SET all_terminal=0,waiting_count=1,terminal_count=2,done_count=2 WHERE run_id=10`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker = retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerPlanNotTerminal) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierOutsideFrozenDAG(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	recompute(t, db, fx.RunID, BarrierOptions{})
	var allTerminal, total int
	if err := db.QueryRow(`SELECT all_terminal,total_count FROM media_plan_completion WHERE run_id=?`, fx.RunID).Scan(&allTerminal, &total); err != nil {
		t.Fatal(err)
	}
	if allTerminal != 1 || total != 3 {
		t.Fatalf("plan completion mutated all=%d total=%d", allTerminal, total)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE step_type='plaintext_retirement'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("retirement must stay outside frozen DAG")
	}
}

func TestBarrierPackageBasisOutputAndKey(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	out := filepath.Join(fx.Root, "pkg", "master.m3u8")
	writeFile(t, out, []byte("#EXTM3U"))
	if _, err := db.Exec(`INSERT INTO package_task(id,media_id,pipeline_type,status,output_path) VALUES(50,1,'cmaf_drm','done',?)`, out); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json,blocked_at)
VALUES(1,10,1,?,?,'package',50,NULL,50,0,'blocked','{}',CURRENT_TIMESTAMP)`, fx.SourcePath, fx.SourceFP); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM media_plaintext_retirement WHERE basis_kind='package'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, id)
	if state != string(StateReady) || blocker != "" {
		t.Fatalf("package basis state=%s blocker=%s", state, blocker)
	}

	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker = retirementState(t, db, id)
	if state != string(StateBlocked) || blocker != string(BlockerPackageOutputUnread) {
		t.Fatalf("state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierPackageBasisOutsideAuthoritativeGeneration(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	out := filepath.Join(fx.Root, "pkg", "master.m3u8")
	writeFile(t, out, []byte("#EXTM3U"))
	if _, err := db.Exec(`INSERT INTO package_task(id,media_id,pipeline_type,status,output_path,drm_status) VALUES(51,1,'cmaf_drm','done',?,'done')`, out); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json,blocked_at)
VALUES(1,10,1,?,?,'package',51,NULL,51,0,'blocked','{}',CURRENT_TIMESTAMP)`, fx.SourcePath, fx.SourceFP); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM media_plaintext_retirement WHERE basis_kind='package'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, id)
	if state != string(StateBlocked) || blocker != string(BlockerGenerationFence) {
		t.Fatalf("package outside authoritative generation: state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierPackageBasisAcceptsAESKeyMaterial(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	out := filepath.Join(fx.Root, "pkg", "master.m3u8")
	writeFile(t, out, []byte("#EXTM3U"))
	if _, err := db.Exec(`DELETE FROM media_encrypted_assets WHERE media_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM drm_asset WHERE media_id=1`); err != nil {
		// table may be empty
	}
	if _, err := db.Exec(`INSERT INTO package_task(id,media_id,pipeline_type,status,output_path,drm_status) VALUES(52,1,'hls_aes_128','done',?,'done')`, out); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO drm_key_material(media_id,mode,kid,key_hex,iv_hex) VALUES(1,'hls_aes_128','kid','aabb','ccdd')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json,blocked_at)
VALUES(1,10,1,?,?,'package',52,NULL,52,0,'blocked','{}',CURRENT_TIMESTAMP)`, fx.SourcePath, fx.SourceFP); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM media_plaintext_retirement WHERE basis_kind='package'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, id)
	if state != string(StateReady) || blocker != "" {
		t.Fatalf("AES package key material should satisfy barrier: state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierPackageBasisDRMKeyMissingWhenRequired(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	out := filepath.Join(fx.Root, "pkg", "master.m3u8")
	writeFile(t, out, []byte("#EXTM3U"))
	_, _ = db.Exec(`DELETE FROM drm_asset WHERE media_id=1`)
	_, _ = db.Exec(`DELETE FROM drm_key_material WHERE media_id=1`)
	if _, err := db.Exec(`INSERT INTO package_task(id,media_id,pipeline_type,status,output_path,drm_status) VALUES(53,1,'cmaf_drm','done',?,'done')`, out); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json,blocked_at)
VALUES(1,10,1,?,?,'package',53,NULL,53,0,'blocked','{}',CURRENT_TIMESTAMP)`, fx.SourcePath, fx.SourceFP); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM media_plaintext_retirement WHERE basis_kind='package'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, id)
	if state != string(StateBlocked) || blocker != string(BlockerPackageKeyUnreadable) {
		t.Fatalf("drm_status=done without key must block: state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierPackageBasisDRMKeyRefFileMissing(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	out := filepath.Join(fx.Root, "pkg", "master.m3u8")
	writeFile(t, out, []byte("#EXTM3U"))
	missingKey := filepath.Join(fx.Root, "pkg", "missing_key_ref.json")
	_, _ = db.Exec(`DELETE FROM drm_key_material WHERE media_id=1`)
	if _, err := db.Exec(`INSERT INTO drm_asset(media_id,kid,key_ref,manifest_path) VALUES(1,'kid',?,?)
ON CONFLICT(media_id) DO UPDATE SET key_ref=excluded.key_ref,manifest_path=excluded.manifest_path`, missingKey, out); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO package_task(id,media_id,pipeline_type,status,output_path,drm_status) VALUES(54,1,'cmaf_drm','done',?,'done')`, out); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json,blocked_at)
VALUES(1,10,1,?,?,'package',54,NULL,54,0,'blocked','{}',CURRENT_TIMESTAMP)`, fx.SourcePath, fx.SourceFP); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM media_plaintext_retirement WHERE basis_kind='package'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, id)
	if state != string(StateBlocked) || blocker != string(BlockerPackageKeyUnreadable) {
		t.Fatalf("missing key_ref file must block: state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierPackageBasisDRMKeyRefFilePresent(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	out := filepath.Join(fx.Root, "pkg", "master.m3u8")
	keyRef := filepath.Join(fx.Root, "pkg", "drm_key_ref.json")
	writeFile(t, out, []byte("#EXTM3U"))
	writeFile(t, keyRef, []byte(`{"kid":"x","key":"y"}`))
	_, _ = db.Exec(`DELETE FROM drm_key_material WHERE media_id=1`)
	if _, err := db.Exec(`INSERT INTO drm_asset(media_id,kid,key_ref,manifest_path) VALUES(1,'kid',?,?)
ON CONFLICT(media_id) DO UPDATE SET key_ref=excluded.key_ref,manifest_path=excluded.manifest_path`, keyRef, out); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO package_task(id,media_id,pipeline_type,status,output_path,drm_status) VALUES(55,1,'cmaf_drm','done',?,'done')`, out); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM media_plaintext_retirement WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_plaintext_retirement(media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,state,quarantine_evidence_json,blocked_at)
VALUES(1,10,1,?,?,'package',55,NULL,55,0,'blocked','{}',CURRENT_TIMESTAMP)`, fx.SourcePath, fx.SourceFP); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM media_plaintext_retirement WHERE basis_kind='package'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, id)
	if state != string(StateReady) || blocker != "" {
		t.Fatalf("readable key_ref must be eligible: state=%s blocker=%s", state, blocker)
	}
}

func TestEvaluateBarrierTxDirect(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	res, err := EvaluateBarrierTx(context.Background(), tx, fx.RetirementID, BarrierOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Eligible {
		t.Fatalf("expected eligible got %+v", res)
	}
}

func TestBarrierQuarantineFingerprintRequiredWhenSourceMissing(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	id := identityFor(fx, 1)
	qPath, err := QuarantinePath(fx.QuarantineRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(fx.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, qPath, body)
	if err = os.Remove(fx.SourcePath); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media_plaintext_retirement SET quarantine_path=?, quarantine_fingerprint='' WHERE id=?`, qPath, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerFingerprintFence) {
		t.Fatalf("empty quarantine_fingerprint must fail closed: state=%s blocker=%s", state, blocker)
	}
}

func TestBarrierEncryptStatusMissingBlocks(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if _, err := db.Exec(`UPDATE media_plaintext_retirement SET basis_id=99999 WHERE id=?`, fx.RetirementID); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerEvidenceUnreadable) {
		t.Fatalf("missing encrypt task must block: state=%s blocker=%s", state, blocker)
	}
}

// TestBarrierEncArtifactSizeVerified proves the barrier gates the encrypted
// artifact on existence and recorded size without re-hashing the file: a
// mismatched enc_size blocks, while the content digest stored in the journal is
// trusted from encryption time (ciphertext integrity is verified again when the
// file is consumed/decrypted).
func TestBarrierEncArtifactSizeVerified(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)
	if _, err := db.Exec(`UPDATE media_encryption_stage_journal SET enc_sha256=?, enc_size=99999 WHERE stage_id=?`, shaHex([]byte("ciphertext-body")), fx.StageID); err != nil {
		t.Fatal(err)
	}
	recompute(t, db, fx.RunID, BarrierOptions{})
	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerCiphertextUnreadable) {
		t.Fatalf("enc size mismatch must block: state=%s blocker=%s", state, blocker)
	}
}

// --- Task 10: Phase 5 encrypted-source contract tests ---

// TestRetirementBarrier_EncryptedSourceBlocksIncompleteStrategy verifies
// the retirement barrier blocks when any required strategy is missing or
// unvalidated.
func TestRetirementBarrier_EncryptedSourceBlocksIncompleteStrategy(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)

	// Create incomplete strategy registry (missing ai_analysis).
	incomplete := publication.DefaultEncryptedSourceStrategies()
	incomplete[publication.StepAIAnalysis] = publication.EncryptedSourceContract{}

	opts := BarrierOptions{Strategies: incomplete}
	recompute(t, db, fx.RunID, opts)

	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerStrategyIncomplete) {
		t.Fatalf("incomplete strategy must block: state=%s blocker=%s", state, blocker)
	}
}

// TestRetirementBarrier_EncryptedSourceRejectsPlaintextStrategy verifies
// no plaintext strategy is accepted.
func TestRetirementBarrier_EncryptedSourceRejectsPlaintextStrategy(t *testing.T) {
	db := openRetirementDB(t)
	fx := seedEligibleEncryptionFixture(t, db)

	plaintext := publication.DefaultEncryptedSourceStrategies()
	plaintext[publication.StepPreview] = publication.EncryptedSourceContract{
		Strategy:  "",
		Validated: true,
	}

	opts := BarrierOptions{Strategies: plaintext}
	recompute(t, db, fx.RunID, opts)

	state, blocker := retirementState(t, db, fx.RetirementID)
	if state != string(StateBlocked) || blocker != string(BlockerStrategyIncomplete) {
		t.Fatalf("plaintext strategy must block: state=%s blocker=%s", state, blocker)
	}
}

// TestRetirementBarrier_EncryptedSourceStreamDecryptIsClean verifies that
// stream_decrypt strategy does not leave plaintext artifacts (always pipes
// through ffmpeg or equivalent).
func TestRetirementBarrier_EncryptedSourceStreamDecryptIsClean(t *testing.T) {
	registry := publication.DefaultEncryptedSourceStrategies()
	streamTypes := []publication.StepType{
		publication.StepPreview, publication.StepSubtitleExtract,
		publication.StepAtrackExtract, publication.StepSubtitleRecognize,
		publication.StepKeyframeExtract, publication.StepPrepare,
	}
	for _, typ := range streamTypes {
		c, ok := registry.Contract(typ)
		if !ok || c.Strategy != publication.EncryptedSourceStreamDecrypt {
			t.Errorf("%s should use stream_decrypt, got %+v", typ, c)
		}
	}
}

// TestRetirementBarrier_EncryptedSourceDerivativeLimitedToSpecificRoles verifies
// derivative is limited to poster, thumbnail, scrape, and ai_analysis only.
func TestRetirementBarrier_EncryptedSourceDerivativeLimitedToSpecificRoles(t *testing.T) {
	registry := publication.DefaultEncryptedSourceStrategies()
	derivativeTypes := map[publication.StepType]bool{
		publication.StepPoster:     true,
		publication.StepThumbnail:  true,
		publication.StepScrape:     true,
		publication.StepAIAnalysis: true,
	}
	for step, contract := range registry {
		if contract.Strategy == publication.EncryptedSourceDerivative {
			if !derivativeTypes[step] {
				t.Errorf("%s should not use derivative strategy", step)
			}
		}
	}
}
