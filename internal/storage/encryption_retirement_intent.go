package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"knox-media/internal/store"
)

// ErrEncryptionRetirementConflict is returned when an active retirement row cannot
// safely accept a new encryption-basis identity.
var ErrEncryptionRetirementConflict = errors.New("encryption retirement: conflicting active retirement")

// EncryptionRetirementIntent is the durable identity required to request plaintext
// retirement after encryption commits ciphertext selection (Task 11/14 handoff).
type EncryptionRetirementIntent struct {
	MediaID           int64
	RunID             int64
	Generation        int64
	BasisID           int64 // post_ingest encrypt task id
	StageID           string
	SourcePath        string
	SourceFingerprint string
	RetryRound        int
	QuarantinePath    string
	Cleanup           bool
}

// UpsertEncryptionRetirementIntentTx creates or refreshes the generation-scoped
// retirement row for an encryption basis. Matching Task 11 contracts: blocked start,
// updatable blocked/ready/retryable_failed, idempotent matching in-flight/terminal,
// conflict fail-closed otherwise. No-op when Cleanup is false.
func UpsertEncryptionRetirementIntentTx(ctx context.Context, tx store.SQLExecutor, intent EncryptionRetirementIntent) error {
	if tx == nil {
		return fmt.Errorf("encryption retirement: nil tx")
	}
	if !intent.Cleanup {
		return nil
	}
	if intent.MediaID <= 0 || intent.RunID <= 0 || intent.Generation <= 0 || intent.BasisID <= 0 {
		return fmt.Errorf("encryption retirement: invalid intent identity")
	}
	if strings.TrimSpace(intent.StageID) == "" || strings.TrimSpace(intent.SourcePath) == "" || strings.TrimSpace(intent.SourceFingerprint) == "" {
		return fmt.Errorf("encryption retirement: missing source identity")
	}
	evidence := "{}"
	qPath := strings.TrimSpace(intent.QuarantinePath)
	if qPath != "" {
		raw, err := json.Marshal(map[string]string{"encryption_quarantine_path": qPath})
		if err != nil {
			return err
		}
		evidence = string(raw)
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO media_plaintext_retirement(
  media_id,run_id,generation,source_path,source_fingerprint,
  basis_kind,basis_id,encryption_stage_id,package_task_id,retry_round,
  state,quarantine_path,quarantine_evidence_json,blocked_at,updated_at
) VALUES(?,?,?,?,?,'encryption',?,?,NULL,?,'blocked',?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(media_id,generation) DO UPDATE SET
  run_id=excluded.run_id,
  source_path=excluded.source_path,
  source_fingerprint=excluded.source_fingerprint,
  basis_kind='encryption',
  basis_id=excluded.basis_id,
  encryption_stage_id=excluded.encryption_stage_id,
  package_task_id=NULL,
  retry_round=excluded.retry_round,
  quarantine_path=CASE WHEN excluded.quarantine_path<>'' THEN excluded.quarantine_path ELSE media_plaintext_retirement.quarantine_path END,
  quarantine_evidence_json=CASE WHEN excluded.quarantine_evidence_json<>'{}' THEN excluded.quarantine_evidence_json ELSE media_plaintext_retirement.quarantine_evidence_json END,
  updated_at=CURRENT_TIMESTAMP
WHERE media_plaintext_retirement.state IN ('blocked','ready','retryable_failed')`,
		intent.MediaID, intent.RunID, intent.Generation, intent.SourcePath, intent.SourceFingerprint,
		intent.BasisID, intent.StageID, intent.RetryRound, qPath, evidence)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}
	var (
		existingStage, existingKind, existingFP, existingState string
		existingBasis                                          int64
	)
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(encryption_stage_id,''),basis_kind,basis_id,source_fingerprint,state FROM media_plaintext_retirement WHERE media_id=? AND generation=?`,
		intent.MediaID, intent.Generation).Scan(&existingStage, &existingKind, &existingBasis, &existingFP, &existingState)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("encryption retirement: upsert affected no rows")
	}
	if err != nil {
		return err
	}
	switch existingState {
	case "quarantining", "quarantined", "deleting", "verified", "operator_required":
		if existingKind == "encryption" && existingStage == intent.StageID && existingBasis == intent.BasisID && existingFP == intent.SourceFingerprint {
			_, _ = tx.ExecContext(ctx, `UPDATE media_plaintext_retirement SET updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND generation=? AND encryption_stage_id=? AND state=?`,
				intent.MediaID, intent.Generation, intent.StageID, existingState)
			return nil
		}
		return fmt.Errorf("%w: state=%s stage=%s", ErrEncryptionRetirementConflict, existingState, existingStage)
	default:
		return fmt.Errorf("%w: unexpected state=%s", ErrEncryptionRetirementConflict, existingState)
	}
}
