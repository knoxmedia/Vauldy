package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"knox-media/internal/store"
)

// ErrCleanupRetirementUnavailable is returned when cleanup_plaintext is requested
// but no publication encrypt identity (run/task) exists to fence a retirement row.
var ErrCleanupRetirementUnavailable = errors.New("asset encrypt: cleanup_plaintext requires publication encryption identity")

type encryptionRetirementIdentity struct {
	RunID      int64
	TaskID     int64
	StepID     int64
	RetryRound int
	Attempts   int
	Owner      string
}

func lookupEncryptionRetirementIdentity(ctx context.Context, q store.SQLExecutor, mediaID, generation int64) (encryptionRetirementIdentity, error) {
	var id encryptionRetirementIdentity
	if q == nil || mediaID <= 0 || generation <= 0 {
		return id, ErrCleanupRetirementUnavailable
	}
	err := q.QueryRowContext(ctx, `
SELECT r.id
FROM media_ingest_run r
WHERE r.media_id=? AND r.generation=?
  AND (r.superseded_at IS NULL OR TRIM(COALESCE(r.superseded_at,''))='')
ORDER BY r.id DESC LIMIT 1`, mediaID, generation).Scan(&id.RunID)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return id, fmt.Errorf("%w: missing current ingest run", ErrCleanupRetirementUnavailable)
	}
	err = q.QueryRowContext(ctx, `
SELECT id, COALESCE(ingest_step_id,0), COALESCE(retry_round,0), COALESCE(attempts,0), COALESCE(lease_owner,'')
FROM post_ingest_task
WHERE media_id=? AND generation=? AND task_type='encrypt' AND ingest_run_id=?
ORDER BY id DESC LIMIT 1`, mediaID, generation, id.RunID).
		Scan(&id.TaskID, &id.StepID, &id.RetryRound, &id.Attempts, &id.Owner)
	if errors.Is(err, sql.ErrNoRows) || err != nil || id.TaskID <= 0 || id.StepID <= 0 {
		return id, fmt.Errorf("%w: missing encrypt task", ErrCleanupRetirementUnavailable)
	}
	if strings.TrimSpace(id.Owner) == "" {
		id.Owner = "manual-encrypt"
	}
	if id.Attempts <= 0 {
		id.Attempts = 1
	}
	return id, nil
}

func ensureCommittedEncryptionJournalTx(ctx context.Context, tx store.SQLExecutor, mediaID, generation int64, id encryptionRetirementIdentity, plainPath, fingerprint string, output resumableEncryptOutput, cleanupPlain bool) (string, error) {
	var stageID string
	err := tx.QueryRowContext(ctx, `
SELECT stage_id FROM media_encryption_stage_journal
WHERE task_id=? AND media_id=? AND generation=?
ORDER BY attempt DESC LIMIT 1`, id.TaskID, mediaID, generation).Scan(&stageID)
	cleanupFlag := 0
	if cleanupPlain {
		cleanupFlag = 1
	}
	if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(stageID) == "" {
		stageID = uuid.NewString()
		_, err = tx.ExecContext(ctx, `
INSERT INTO media_encryption_stage_journal(
  stage_id,task_id,retry_round,attempt,media_id,run_id,step_id,generation,owner_token,
  source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state,recovery_error,recovery_attempts
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'committed','retirement_handoff',0)`,
			stageID, id.TaskID, id.RetryRound, id.Attempts, mediaID, id.RunID, id.StepID, generation, id.Owner,
			plainPath, fingerprint, output.EncPath, output.WrappedDEK, output.IV, output.SHA256, output.Size, cleanupFlag)
		if err != nil {
			return "", err
		}
		return stageID, nil
	}
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE media_encryption_stage_journal
SET source_path=?, source_fingerprint=?, enc_path=?, wrapped_dek=?, iv=?, enc_sha256=?, enc_size=?,
    cleanup_plaintext=?, state='committed', recovery_error='retirement_handoff', updated_at=CURRENT_TIMESTAMP
WHERE stage_id=? AND media_id=? AND generation=?`,
		plainPath, fingerprint, output.EncPath, output.WrappedDEK, output.IV, output.SHA256, output.Size,
		cleanupFlag, stageID, mediaID, generation)
	if err != nil {
		return "", err
	}
	return stageID, nil
}

// upsertManualEncryptRetirementTx records retirement intent in the encrypt commit
// transaction when cleanup_plaintext is requested. Never deletes the source.
func upsertManualEncryptRetirementTx(ctx context.Context, tx store.SQLExecutor, mediaID, generation int64, plainPath string, output resumableEncryptOutput, cleanupPlain bool) error {
	if !cleanupPlain {
		return nil
	}
	if generation <= 0 {
		return fmt.Errorf("%w: missing ingest generation", ErrCleanupRetirementUnavailable)
	}
	id, err := lookupEncryptionRetirementIdentity(ctx, tx, mediaID, generation)
	if err != nil {
		return err
	}
	fp, err := EncryptionSourceFingerprint(plainPath)
	if err != nil || strings.TrimSpace(fp) == "" {
		return fmt.Errorf("asset encrypt: source fingerprint: %w", err)
	}
	stageID, err := ensureCommittedEncryptionJournalTx(ctx, tx, mediaID, generation, id, plainPath, fp, output, true)
	if err != nil {
		return err
	}
	return UpsertEncryptionRetirementIntentTx(ctx, tx, EncryptionRetirementIntent{
		MediaID:           mediaID,
		RunID:             id.RunID,
		Generation:        generation,
		BasisID:           id.TaskID,
		StageID:           stageID,
		SourcePath:        plainPath,
		SourceFingerprint: fp,
		RetryRound:        id.RetryRound,
		Cleanup:           true,
	})
}
