package retirement

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

// BarrierOptions configures optional barrier predicates.
type BarrierOptions struct {
	Strategies     publication.EncryptedSourceRegistry
	ActiveConsumer ActiveConsumerFunc
	// Fingerprint computes a source identity fence; defaults to storage.EncryptionSourceFingerprint.
	Fingerprint func(path string) (string, error)
}

func defaultStrategies(reg publication.EncryptedSourceRegistry) publication.EncryptedSourceRegistry {
	if reg != nil {
		return reg
	}
	return publication.DefaultEncryptedSourceStrategies()
}

func defaultFingerprint(fn func(string) (string, error)) func(string) (string, error) {
	if fn != nil {
		return fn
	}
	return storage.EncryptionSourceFingerprint
}

// EvaluateBarrier is the authoritative eligibility check for one retirement row.
func EvaluateBarrier(ctx context.Context, q store.SQLExecutor, row Row, opts BarrierOptions) BarrierResult {
	if q == nil || row.MediaID <= 0 || row.RunID <= 0 || row.Generation <= 0 {
		return BarrierResult{Blocker: BlockerNoIntent, Detail: "invalid retirement identity"}
	}
	fpFn := defaultFingerprint(opts.Fingerprint)
	strategies := defaultStrategies(opts.Strategies)

	// Generation fence: current media generation + non-superseded run.
	var mediaGen int64
	var superseded sql.NullString
	var runGen int64
	err := q.QueryRowContext(ctx, `
SELECT m.ingest_generation, r.generation, r.superseded_at
FROM media m
JOIN media_ingest_run r ON r.id=? AND r.media_id=m.id
WHERE m.id=?`, row.RunID, row.MediaID).Scan(&mediaGen, &runGen, &superseded)
	if err != nil {
		return BarrierResult{Blocker: BlockerGenerationFence, Detail: formatErr("load generation", err)}
	}
	if superseded.Valid && strings.TrimSpace(superseded.String) != "" {
		return BarrierResult{Blocker: BlockerSuperseded, Detail: "run superseded"}
	}
	if mediaGen != row.Generation || runGen != row.Generation {
		return BarrierResult{Blocker: BlockerGenerationFence, Detail: fmt.Sprintf("media_gen=%d run_gen=%d row_gen=%d", mediaGen, runGen, row.Generation)}
	}

	// Plan terminal: waiting/running and retryable waiting block; permanent fail/cancel/skip OK.
	var allTerminal, waiting, running int
	err = q.QueryRowContext(ctx, `
SELECT COALESCE(all_terminal,0), COALESCE(waiting_count,0), COALESCE(running_count,0)
FROM media_plan_completion WHERE run_id=? AND media_id=? AND generation=?`,
		row.RunID, row.MediaID, row.Generation).Scan(&allTerminal, &waiting, &running)
	if err != nil {
		return BarrierResult{Blocker: BlockerPlanNotTerminal, Detail: formatErr("plan completion", err)}
	}
	if allTerminal != 1 || waiting > 0 || running > 0 {
		return BarrierResult{Blocker: BlockerPlanNotTerminal, Detail: fmt.Sprintf("all_terminal=%d waiting=%d running=%d", allTerminal, waiting, running)}
	}
	var nonTerminal, retryableWaiting int
	_ = q.QueryRowContext(ctx, `
SELECT COALESCE(SUM(status IN ('waiting','running')),0),
       COALESCE(SUM(status='waiting' AND attempts>0 AND attempts<max_attempts),0)
FROM media_ingest_step WHERE run_id=? AND generation=?`, row.RunID, row.Generation).Scan(&nonTerminal, &retryableWaiting)
	if nonTerminal > 0 || retryableWaiting > 0 {
		return BarrierResult{Blocker: BlockerPlanNotTerminal, Detail: fmt.Sprintf("non_terminal=%d retryable_waiting=%d", nonTerminal, retryableWaiting)}
	}

	// Cleanup policy / intent (encryption vs package library flags).
	var cleanupPolicy int
	policySQL := `SELECT COALESCE(l.encrypted_assets_cleanup_plaintext,0) FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`
	if row.BasisKind == BasisPackage {
		policySQL = `SELECT COALESCE(l.cleanup_local_source_after_package,0) FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`
	}
	err = q.QueryRowContext(ctx, policySQL, row.MediaID).Scan(&cleanupPolicy)
	if err != nil || cleanupPolicy != 1 {
		return BarrierResult{Blocker: BlockerPolicyDisabled, Detail: "library cleanup policy disabled"}
	}

	// Encrypted-source strategies for all retryable types.
	if err := publication.ValidateStrategyRegistry(strategies); err != nil {
		return BarrierResult{Blocker: BlockerStrategyIncomplete, Detail: err.Error()}
	}
	var snapRaw string
	_ = q.QueryRowContext(ctx, `SELECT COALESCE(config_snapshot_json,'{}') FROM media_ingest_run WHERE id=?`, row.RunID).Scan(&snapRaw)
	var snap publication.ConfigSnapshot
	if json.Unmarshal([]byte(snapRaw), &snap) == nil && len(snap.Steps) > 0 {
		if _, err := publication.ValidateEncryptedSourceContracts(snap.Steps, true, strategies); err != nil {
			return BarrierResult{Blocker: BlockerStrategyIncomplete, Detail: err.Error()}
		}
	}

	// Basis-specific ciphertext/key/evidence (or package output/key).
	switch row.BasisKind {
	case BasisEncryption:
		if res := evaluateEncryptionBasis(ctx, q, row); !res.Eligible {
			return res
		}
	case BasisPackage:
		if res := evaluatePackageBasis(ctx, q, row); !res.Eligible {
			return res
		}
	default:
		return BarrierResult{Blocker: BlockerNoIntent, Detail: "unknown basis_kind"}
	}

	// Source fingerprint fence (path still present, or already in reserved quarantine).
	checkPath := strings.TrimSpace(row.SourcePath)
	if _, statErr := os.Stat(checkPath); statErr != nil {
		if qPath := strings.TrimSpace(row.QuarantinePath); qPath != "" {
			if _, qErr := os.Stat(qPath); qErr == nil {
				checkPath = qPath
			} else {
				return BarrierResult{Blocker: BlockerSourceMissing, Detail: formatErr("source", statErr)}
			}
		} else {
			return BarrierResult{Blocker: BlockerSourceMissing, Detail: formatErr("source", statErr)}
		}
	}
	if checkPath == strings.TrimSpace(row.SourcePath) {
		got, err := fpFn(checkPath)
		if err != nil {
			return BarrierResult{Blocker: BlockerFingerprintFence, Detail: formatErr("fingerprint", err)}
		}
		if got != row.SourceFingerprint {
			return BarrierResult{Blocker: BlockerFingerprintFence, Detail: "source fingerprint mismatch"}
		}
	} else {
		qfp := strings.TrimSpace(row.QuarantineFingerprint)
		if qfp == "" {
			return BarrierResult{Blocker: BlockerFingerprintFence, Detail: "quarantine fingerprint missing"}
		}
		got, err := fpFn(checkPath)
		if err != nil || got != qfp {
			return BarrierResult{Blocker: BlockerFingerprintFence, Detail: "quarantine fingerprint mismatch"}
		}
	}

	// Active plaintext consumer — always evaluate on the caller-owned executor
	// (*sql.DB, *sql.Tx, *sql.Conn / ImmediateConnTx). Never skip unknown types.
	if opts.ActiveConsumer != nil && opts.ActiveConsumer(row.MediaID) {
		return BarrierResult{Blocker: BlockerActiveConsumer, Detail: "active plaintext consumer"}
	}
	busy, err := plaintextBusy(ctx, q, row.MediaID)
	if err != nil {
		return BarrierResult{Blocker: BlockerActiveConsumer, Detail: formatErr("active consumer", err)}
	}
	if busy {
		return BarrierResult{Blocker: BlockerActiveConsumer, Detail: "active plaintext consumer"}
	}
	// JIT session callback is registered against *sql.DB only.
	if db, ok := q.(*sql.DB); ok && storage.HasActivePlaintextConsumer(db, row.MediaID) {
		return BarrierResult{Blocker: BlockerActiveConsumer, Detail: "active plaintext consumer"}
	}

	return BarrierResult{Eligible: true, Blocker: BlockerNone}
}

func evaluateEncryptionBasis(ctx context.Context, q store.SQLExecutor, row Row) BarrierResult {
	if strings.TrimSpace(row.EncryptionStageID) == "" {
		return BarrierResult{Blocker: BlockerEvidenceUnreadable, Detail: "missing encryption_stage_id"}
	}
	var encPath, wrapped, iv, encSHA, journalState string
	var encSize int64
	err := q.QueryRowContext(ctx, `
SELECT COALESCE(enc_path,''),COALESCE(wrapped_dek,''),COALESCE(iv,''),COALESCE(enc_sha256,''),COALESCE(enc_size,0),COALESCE(state,'')
FROM media_encryption_stage_journal
WHERE stage_id=? AND media_id=? AND generation=?`, row.EncryptionStageID, row.MediaID, row.Generation).
		Scan(&encPath, &wrapped, &iv, &encSHA, &encSize, &journalState)
	if err != nil {
		return BarrierResult{Blocker: BlockerEvidenceUnreadable, Detail: formatErr("encryption journal", err)}
	}
	if journalState != "committed" {
		return BarrierResult{Blocker: BlockerEvidenceUnreadable, Detail: "encryption journal not committed"}
	}
	if strings.TrimSpace(wrapped) == "" || strings.TrimSpace(iv) == "" {
		return BarrierResult{Blocker: BlockerKeyUnreadable, Detail: "missing wrapped_dek/iv"}
	}
	if strings.TrimSpace(encPath) == "" {
		return BarrierResult{Blocker: BlockerCiphertextUnreadable, Detail: "missing enc_path"}
	}
	if st, e := os.Stat(encPath); e != nil || st.IsDir() {
		return BarrierResult{Blocker: BlockerCiphertextUnreadable, Detail: formatErr("enc_path", e)}
	} else {
		if encSize > 0 && st.Size() != encSize {
			return BarrierResult{Blocker: BlockerCiphertextUnreadable, Detail: fmt.Sprintf("enc_size got=%d want=%d", st.Size(), encSize)}
		}
		if sha := strings.TrimSpace(encSHA); sha != "" {
			got, hashErr := fileSHA256(encPath)
			if hashErr != nil || got != sha {
				return BarrierResult{Blocker: BlockerCiphertextUnreadable, Detail: "enc_sha256 mismatch"}
			}
		}
	}
	var assetEnc, assetDEK, assetIV, assetStatus string
	err = q.QueryRowContext(ctx, `
SELECT COALESCE(enc_path,''),COALESCE(wrapped_dek,''),COALESCE(iv,''),COALESCE(status,'')
FROM media_encrypted_assets WHERE media_id=?`, row.MediaID).Scan(&assetEnc, &assetDEK, &assetIV, &assetStatus)
	if err != nil || assetStatus != "encrypted" || strings.TrimSpace(assetDEK) == "" || strings.TrimSpace(assetIV) == "" {
		return BarrierResult{Blocker: BlockerKeyUnreadable, Detail: "encrypted assets key missing"}
	}
	if _, e := os.Stat(assetEnc); e != nil {
		return BarrierResult{Blocker: BlockerCiphertextUnreadable, Detail: formatErr("asset enc", e)}
	}
	var evidence int
	_ = q.QueryRowContext(ctx, `
SELECT COUNT(*) FROM media_ingest_evidence
WHERE media_id=? AND generation=? AND kind='encrypt' AND stage_id=?`, row.MediaID, row.Generation, row.EncryptionStageID).Scan(&evidence)
	if evidence < 1 {
		return BarrierResult{Blocker: BlockerEvidenceUnreadable, Detail: "encrypt evidence missing"}
	}
	// Encryption task must remain done (retirement outside DAG). Missing/empty status is a blocker.
	if row.BasisID <= 0 {
		return BarrierResult{Blocker: BlockerEvidenceUnreadable, Detail: "missing encrypt task basis_id"}
	}
	var encryptStatus string
	err = q.QueryRowContext(ctx, `
SELECT COALESCE(status,'') FROM post_ingest_task
WHERE media_id=? AND generation=? AND task_type='encrypt' AND id=?`, row.MediaID, row.Generation, row.BasisID).Scan(&encryptStatus)
	if err != nil || strings.TrimSpace(encryptStatus) == "" {
		return BarrierResult{Blocker: BlockerEvidenceUnreadable, Detail: "encryption task missing"}
	}
	if encryptStatus != "done" {
		return BarrierResult{Blocker: BlockerEvidenceUnreadable, Detail: "encryption task not done"}
	}
	return BarrierResult{Eligible: true}
}

func evaluatePackageBasis(ctx context.Context, q store.SQLExecutor, row Row) BarrierResult {
	if row.PackageTaskID <= 0 {
		return BarrierResult{Blocker: BlockerPackageOutputUnread, Detail: "missing package_task_id"}
	}
	var output, status, drmStatus string
	err := q.QueryRowContext(ctx, `
SELECT COALESCE(output_path,''),COALESCE(status,''),COALESCE(drm_status,'') FROM package_task WHERE id=? AND media_id=?`,
		row.PackageTaskID, row.MediaID).Scan(&output, &status, &drmStatus)
	if err != nil {
		return BarrierResult{Blocker: BlockerPackageOutputUnread, Detail: formatErr("package_task", err)}
	}
	if status != "done" && status != "completed" {
		return BarrierResult{Blocker: BlockerPackageOutputUnread, Detail: "package not done"}
	}
	if strings.TrimSpace(output) == "" {
		return BarrierResult{Blocker: BlockerPackageOutputUnread, Detail: "missing output_path"}
	}
	if st, e := os.Stat(output); e != nil || st.IsDir() {
		return BarrierResult{Blocker: BlockerPackageOutputUnread, Detail: formatErr("output", e)}
	}
	requiresKey := strings.TrimSpace(drmStatus) != "" && !strings.EqualFold(drmStatus, "none")

	var keyRef string
	err = q.QueryRowContext(ctx, `SELECT COALESCE(key_ref,'') FROM drm_asset WHERE media_id=?`, row.MediaID).Scan(&keyRef)
	if err == nil && strings.TrimSpace(keyRef) != "" {
		if st, e := os.Stat(keyRef); e != nil || st.IsDir() {
			return BarrierResult{Blocker: BlockerPackageKeyUnreadable, Detail: formatErr("key_ref", e)}
		}
		return BarrierResult{Eligible: true}
	}
	// AES-128 / PowerDRM packages persist key material in DB instead of drm_asset.
	var keyHex string
	err = q.QueryRowContext(ctx, `SELECT COALESCE(key_hex,'') FROM drm_key_material WHERE media_id=?`, row.MediaID).Scan(&keyHex)
	if err == nil && strings.TrimSpace(keyHex) != "" {
		return BarrierResult{Eligible: true}
	}
	if requiresKey {
		return BarrierResult{Blocker: BlockerPackageKeyUnreadable, Detail: "drm key missing"}
	}
	return BarrierResult{Eligible: true}
}

// plaintextBusy reports preview/package/keyframe consumers via the caller's SQLExecutor.
// Works for *sql.DB, *sql.Tx, and *sql.Conn (ImmediateConnTx). Query failure is returned
// so callers can fail closed instead of treating an unknown result as idle.
func plaintextBusy(ctx context.Context, q store.SQLExecutor, mediaID int64) (bool, error) {
	if q == nil {
		return false, fmt.Errorf("active consumer check unavailable: nil executor")
	}
	var previewStatus, packageStatus, keyframeStatus sql.NullString
	err := q.QueryRowContext(ctx, `
SELECT (SELECT status FROM preview_task WHERE media_id = ? LIMIT 1),
       (SELECT status FROM package_task WHERE media_id = ? ORDER BY id DESC LIMIT 1),
       (SELECT status FROM keyframe_task WHERE media_id = ? LIMIT 1)`, mediaID, mediaID, mediaID).
		Scan(&previewStatus, &packageStatus, &keyframeStatus)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if previewStatus.Valid {
		switch strings.ToLower(previewStatus.String) {
		case "running", "processing":
			return true, nil
		}
	}
	if packageStatus.Valid && strings.EqualFold(packageStatus.String, "running") {
		return true, nil
	}
	if keyframeStatus.Valid && strings.EqualFold(keyframeStatus.String, "running") {
		return true, nil
	}
	return false, nil
}

// RecomputeRetirementBarrierTx flips blocked↔ready for the run's retirement rows.
func RecomputeRetirementBarrierTx(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	return RecomputeRetirementBarrierTxWithOptions(ctx, tx, runID, BarrierOptions{
		ActiveConsumer: getDefaultActiveConsumer(),
	})
}

// RecomputeBarrierTx is an alias used by publication wiring.
func RecomputeBarrierTx(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	return RecomputeRetirementBarrierTx(ctx, tx, runID)
}

// RecomputeRetirementBarrierTxWithOptions flips blocked↔ready using custom barrier options.
// In-flight and terminal states are left untouched. Encryption task status is never changed.
func RecomputeRetirementBarrierTxWithOptions(ctx context.Context, tx store.SQLExecutor, runID int64, opts BarrierOptions) error {
	if tx == nil || runID <= 0 {
		return fmt.Errorf("retirement barrier: invalid transaction or run")
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id,media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,
       COALESCE(encryption_stage_id,''),COALESCE(package_task_id,0),retry_round,state,
       COALESCE(quarantine_path,''),COALESCE(quarantine_fingerprint,''),attempts
FROM media_plaintext_retirement
WHERE run_id=? AND state IN ('blocked','ready')`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var list []Row
	for rows.Next() {
		var r Row
		var state string
		if e := rows.Scan(&r.RetirementID, &r.MediaID, &r.RunID, &r.Generation, &r.SourcePath, &r.SourceFingerprint,
			&r.BasisKind, &r.BasisID, &r.EncryptionStageID, &r.PackageTaskID, &r.RetryRound, &state,
			&r.QuarantinePath, &r.QuarantineFingerprint, &r.Attempts); e != nil {
			return e
		}
		r.State = State(state)
		list = append(list, r)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, r := range list {
		res := EvaluateBarrier(ctx, tx, r, opts)
		switch {
		case res.Eligible && r.State == StateBlocked:
			if _, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='ready', blocker_code='', ready_at=COALESCE(ready_at,CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state='blocked'`, r.RetirementID); e != nil {
				return e
			}
		case !res.Eligible && r.State == StateReady:
			if _, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='blocked', blocker_code=?, blocked_at=COALESCE(blocked_at,CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state='ready'`, string(res.Blocker), r.RetirementID); e != nil {
				return e
			}
		case !res.Eligible && r.State == StateBlocked:
			if _, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET blocker_code=?, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state='blocked'`, string(res.Blocker), r.RetirementID); e != nil {
				return e
			}
		}
	}
	return nil
}

// EvaluateBarrierTx loads a retirement row and evaluates the barrier.
func EvaluateBarrierTx(ctx context.Context, tx store.SQLExecutor, retirementID int64, opts BarrierOptions) (BarrierResult, error) {
	row, err := LoadRow(ctx, tx, retirementID)
	if err != nil {
		return BarrierResult{}, err
	}
	return EvaluateBarrier(ctx, tx, row, opts), nil
}

// LoadRow loads a retirement row by id.
func LoadRow(ctx context.Context, q store.SQLExecutor, id int64) (Row, error) {
	var r Row
	var state, blocker string
	var leaseOwner sql.NullString
	var leaseUntil sql.NullTime
	var nextRetry sql.NullTime
	err := q.QueryRowContext(ctx, `
SELECT id,media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,
       COALESCE(encryption_stage_id,''),COALESCE(package_task_id,0),retry_round,state,COALESCE(blocker_code,''),
       lease_owner,lease_until,attempts,COALESCE(last_error,''),
       COALESCE(quarantine_path,''),COALESCE(quarantine_fingerprint,''),next_retry_at
FROM media_plaintext_retirement WHERE id=?`, id).Scan(
		&r.RetirementID, &r.MediaID, &r.RunID, &r.Generation, &r.SourcePath, &r.SourceFingerprint,
		&r.BasisKind, &r.BasisID, &r.EncryptionStageID, &r.PackageTaskID, &r.RetryRound, &state, &blocker,
		&leaseOwner, &leaseUntil, &r.Attempts, &r.LastError, &r.QuarantinePath, &r.QuarantineFingerprint, &nextRetry)
	if err != nil {
		return Row{}, err
	}
	r.State = State(state)
	r.BlockerCode = BlockerCode(blocker)
	if leaseOwner.Valid {
		r.LeaseOwner = leaseOwner.String
	}
	if leaseUntil.Valid {
		t := leaseUntil.Time
		r.LeaseUntil = &t
	}
	if nextRetry.Valid {
		t := nextRetry.Time
		r.NextRetryAt = &t
	}
	return r, nil
}
