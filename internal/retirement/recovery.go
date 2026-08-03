package retirement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"knox-media/internal/storage"
	"knox-media/internal/store"
)

// RecoveryOptions configures startup reconciliation.
type RecoveryOptions struct {
	QuarantineRoot string
	FileOps        FileOps
	Seams          CrashSeams
	MaxAttempts    int
}

// ReconcileStartup repairs interrupted retirement rows idempotently.
// Order: restore uncommitted quarantining moves, continue quarantined/deleting,
// finish post-delete/pre-verify retryable_failed when evidence is sufficient.
func ReconcileStartup(ctx context.Context, db *sql.DB, opts RecoveryOptions) error {
	if db == nil {
		return ErrInvalidIdentity
	}
	rows, err := db.QueryContext(ctx, `
SELECT id,media_id,run_id,generation,source_path,source_fingerprint,basis_kind,basis_id,
       COALESCE(encryption_stage_id,''),COALESCE(package_task_id,0),retry_round,state,attempts,
       COALESCE(quarantine_path,''),COALESCE(quarantine_fingerprint,''),COALESCE(lease_owner,''),
       COALESCE(quarantine_evidence_json,'{}')
FROM media_plaintext_retirement
WHERE state IN ('quarantining','quarantined','deleting','retryable_failed')
ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		row      Row
		evidence string
	}
	var list []item
	for rows.Next() {
		var it item
		var state string
		if e := rows.Scan(&it.row.RetirementID, &it.row.MediaID, &it.row.RunID, &it.row.Generation,
			&it.row.SourcePath, &it.row.SourceFingerprint, &it.row.BasisKind, &it.row.BasisID,
			&it.row.EncryptionStageID, &it.row.PackageTaskID, &it.row.RetryRound, &state, &it.row.Attempts,
			&it.row.QuarantinePath, &it.row.QuarantineFingerprint, &it.row.LeaseOwner, &it.evidence); e != nil {
			return e
		}
		it.row.State = State(state)
		list = append(list, it)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, it := range list {
		if err := reconcileOne(ctx, db, it.row, opts); err != nil {
			return err
		}
	}
	return nil
}

func reconcileOne(ctx context.Context, db *sql.DB, row Row, opts RecoveryOptions) error {
	root := opts.QuarantineRoot
	if root == "" {
		root = ResolveQuarantineRoot(row.SourcePath, "")
	}
	ops := opts.FileOps
	if ops.Rename == nil && ops.Remove == nil {
		ops = defaultFileOps()
	}
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	attempt := row.Attempts
	if attempt <= 0 {
		attempt = 1
	}
	id := row.Identity
	id.Attempt = attempt
	if n := attemptFromQuarantinePath(row.QuarantinePath); n > 0 {
		id.Attempt = n
	}

	switch row.State {
	case StateQuarantining:
		return reconcileQuarantining(ctx, db, row, id, root, ops, maxAttempts)
	case StateQuarantined:
		return continueFromQuarantined(ctx, db, row, id, root, ops, maxAttempts)
	case StateDeleting:
		return continueFromDeleting(ctx, db, row, id, root, ops, maxAttempts)
	case StateRetryableFailed:
		return reconcileRetryableFailed(ctx, db, row, id, root, ops, maxAttempts)
	default:
		return nil
	}
}

// reconcileRetryableFailed finishes post-delete/pre-verify rows when source and
// quarantine are both absent and durable identity/fingerprint evidence is present.
// Other retryable_failed rows are left for ClaimReady.
func reconcileRetryableFailed(ctx context.Context, db *sql.DB, row Row, id Identity, root string, ops FileOps, maxAttempts int) error {
	qPath := strings.TrimSpace(row.QuarantinePath)
	qFP := strings.TrimSpace(row.QuarantineFingerprint)
	if pathExists(row.SourcePath) {
		return nil // normal claim/retry path
	}
	if qPath == "" || qFP == "" {
		// Insufficient evidence to prove delete was ours — leave intact (fail closed).
		return nil
	}
	if err := ValidateQuarantineLayout(root, qPath, id); err != nil {
		return failClosedOperator(ctx, db, row, err.Error())
	}
	if pathExists(qPath) {
		// Quarantine still present: continue delete under recovery.
		if err := markDeletingFromRetryable(ctx, db, row); err != nil {
			return err
		}
		row.State = StateDeleting
		return continueFromDeleting(ctx, db, row, id, root, ops, maxAttempts)
	}
	// Verify-only: both source and quarantine already gone.
	if err := VerifyAbsent(row.SourcePath, qPath); err != nil {
		return failClosedOperator(ctx, db, row, err.Error())
	}
	return markVerifiedRecovery(ctx, db, row)
}

func markDeletingFromRetryable(ctx context.Context, db *sql.DB, row Row) error {
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='deleting', deleting_at=COALESCE(deleting_at,CURRENT_TIMESTAMP), lease_owner=NULL, lease_until=NULL, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state='retryable_failed'`, row.RetirementID)
		return e
	})
	return err
}

func reconcileQuarantining(ctx context.Context, db *sql.DB, row Row, id Identity, root string, ops FileOps, maxAttempts int) error {
	qPath := strings.TrimSpace(row.QuarantinePath)
	sourcePresent := pathExists(row.SourcePath)
	qPresent := qPath != "" && pathExists(qPath)

	switch {
	case sourcePresent && !qPresent:
		// Crash before move or after failed move: release to retryable_failed/ready.
		return resetInterrupted(ctx, db, row, maxAttempts, "startup: quarantining without move")
	case !sourcePresent && qPresent:
		// Move completed but state commit missing: commit quarantined then continue.
		if err := ValidateQuarantinePath(root, qPath, id); err != nil {
			return failClosedOperator(ctx, db, row, err.Error())
		}
		fp := strings.TrimSpace(row.QuarantineFingerprint)
		if fp == "" {
			var e error
			fp, e = fingerprintOrEmpty(qPath)
			if e != nil || fp == "" {
				return failClosedOperator(ctx, db, row, fmt.Sprintf("startup: quarantine fingerprint unavailable: %v", e))
			}
		} else {
			got, e := fingerprintOrEmpty(qPath)
			if e != nil || got != fp {
				return failClosedOperator(ctx, db, row, "startup: quarantine fingerprint mismatch")
			}
		}
		if err := commitQuarantinedState(ctx, db, row, qPath, fp); err != nil {
			return err
		}
		row.QuarantinePath = qPath
		row.QuarantineFingerprint = fp
		row.State = StateQuarantined
		return continueFromQuarantined(ctx, db, row, id, root, ops, maxAttempts)
	case sourcePresent && qPresent:
		return reconcileAmbiguousBothPresent(ctx, db, row, id, root, qPath, ops)
	default:
		return resetInterrupted(ctx, db, row, maxAttempts, "startup: quarantining missing bytes")
	}
}

func reconcileAmbiguousBothPresent(ctx context.Context, db *sql.DB, row Row, id Identity, root, qPath string, ops FileOps) error {
	// Require fingerprint compare; never delete without validated root+fingerprint.
	// Never call ops.Remove directly (may be nil on partial FileOps).
	srcFP, err1 := fingerprintOrEmpty(row.SourcePath)
	qFP, err2 := fingerprintOrEmpty(qPath)
	if err1 != nil || err2 != nil || srcFP == "" || qFP == "" {
		return failClosedOperator(ctx, db, row, "startup: ambiguous quarantining; fingerprint compare unavailable")
	}
	if srcFP != qFP {
		return failClosedOperator(ctx, db, row, "startup: ambiguous quarantining fingerprint mismatch")
	}
	// Duplicate of source under quarantine: safe remove only via validated DeleteQuarantine.
	if err := DeleteQuarantine(root, qPath, qFP, id, ops); err != nil {
		return failClosedOperator(ctx, db, row, fmt.Sprintf("startup: ambiguous duplicate unsafe to delete: %v", err))
	}
	return resetInterrupted(ctx, db, row, DefaultMaxAttempts, "startup: ambiguous duplicate quarantine removed")
}

func continueFromQuarantined(ctx context.Context, db *sql.DB, row Row, id Identity, root string, ops FileOps, maxAttempts int) error {
	if err := markDeletingRecovery(ctx, db, row); err != nil {
		return err
	}
	row.State = StateDeleting
	return continueFromDeleting(ctx, db, row, id, root, ops, maxAttempts)
}

func continueFromDeleting(ctx context.Context, db *sql.DB, row Row, id Identity, root string, ops FileOps, maxAttempts int) error {
	qPath := strings.TrimSpace(row.QuarantinePath)
	if qPath != "" && pathExists(qPath) {
		if err := DeleteQuarantine(root, qPath, row.QuarantineFingerprint, id, ops); err != nil {
			// Never delete on ErrUnsafeQuarantinePath via weak substring match.
			if errors.Is(err, ErrUnsafeQuarantinePath) || errors.Is(err, ErrFingerprintMismatch) {
				return failClosedOperator(ctx, db, row, err.Error())
			}
			return resetInterrupted(ctx, db, row, maxAttempts, err.Error())
		}
	}
	if err := VerifyAbsent(row.SourcePath, qPath); err != nil {
		return resetInterrupted(ctx, db, row, maxAttempts, err.Error())
	}
	return markVerifiedRecovery(ctx, db, row)
}

func commitQuarantinedState(ctx context.Context, db *sql.DB, row Row, qPath, qFP string) error {
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='quarantined', quarantine_path=?, quarantine_fingerprint=?, quarantined_at=COALESCE(quarantined_at,CURRENT_TIMESTAMP),
    lease_owner=NULL, lease_until=NULL, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state='quarantining'`, qPath, qFP, row.RetirementID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return nil // already advanced
		}
		return nil
	})
	return err
}

func markDeletingRecovery(ctx context.Context, db *sql.DB, row Row) error {
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='deleting', deleting_at=COALESCE(deleting_at,CURRENT_TIMESTAMP), lease_owner=NULL, lease_until=NULL, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state='quarantined'`, row.RetirementID)
		return e
	})
	return err
}

func markVerifiedRecovery(ctx context.Context, db *sql.DB, row Row) error {
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='verified', verified_at=COALESCE(verified_at,CURRENT_TIMESTAMP), lease_owner=NULL, lease_until=NULL,
    last_error='', blocker_code='', updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state IN ('deleting','quarantined','retryable_failed')`, row.RetirementID)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `
INSERT INTO media_plaintext_retirement_attempt(retirement_id,retry_round,attempt,outcome,source_fingerprint,quarantine_path,quarantine_fingerprint,finished_at)
VALUES(?,?,?,'verified',?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(retirement_id,retry_round,attempt) DO UPDATE SET outcome='verified', finished_at=CURRENT_TIMESTAMP`,
			row.RetirementID, row.RetryRound, maxInt(row.Attempts, 1), row.SourceFingerprint, row.QuarantinePath, row.QuarantineFingerprint)
		return e
	})
	return err
}

func resetInterrupted(ctx context.Context, db *sql.DB, row Row, maxAttempts int, reason string) error {
	escalate := row.Attempts >= maxAttempts
	state := "retryable_failed"
	outcome := "retryable_failed"
	if escalate {
		state = "operator_required"
		outcome = "operator_required"
	}
	if len(reason) > 400 {
		reason = reason[:400]
	}
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state=?, last_error=?, lease_owner=NULL, lease_until=NULL,
    next_retry_at=CASE WHEN ?=1 THEN NULL ELSE datetime(CURRENT_TIMESTAMP,'+5 seconds') END,
    updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state IN ('quarantining','quarantined','deleting')`, state, reason, boolInt(escalate), row.RetirementID)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `
INSERT INTO media_plaintext_retirement_attempt(retirement_id,retry_round,attempt,outcome,error_message,source_fingerprint,finished_at)
VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(retirement_id,retry_round,attempt) DO UPDATE SET outcome=excluded.outcome, error_message=excluded.error_message, finished_at=CURRENT_TIMESTAMP`,
			row.RetirementID, row.RetryRound, maxInt(row.Attempts, 1), outcome, reason, row.SourceFingerprint)
		return e
	})
	return err
}

func failClosedOperator(ctx context.Context, db *sql.DB, row Row, reason string) error {
	if len(reason) > 400 {
		reason = reason[:400]
	}
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='operator_required', last_error=?, lease_owner=NULL, lease_until=NULL, next_retry_at=NULL, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state IN ('quarantining','quarantined','deleting','retryable_failed','ready')`, reason, row.RetirementID)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `
INSERT INTO media_plaintext_retirement_attempt(retirement_id,retry_round,attempt,outcome,error_message,source_fingerprint,finished_at)
VALUES(?,?,?,'operator_required',?,?,CURRENT_TIMESTAMP)
ON CONFLICT(retirement_id,retry_round,attempt) DO UPDATE SET outcome='operator_required', error_message=excluded.error_message, finished_at=CURRENT_TIMESTAMP`,
			row.RetirementID, row.RetryRound, maxInt(row.Attempts, 1), reason, row.SourceFingerprint)
		return e
	})
	return err
}

func pathExists(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	_, err := os.Lstat(p)
	return err == nil
}

func fingerprintOrEmpty(path string) (string, error) {
	return fingerprintPath(path)
}

func fingerprintPath(path string) (string, error) {
	return quarantineFingerprint(path)
}

func quarantineFingerprint(path string) (string, error) {
	// local wrapper keeps storage import out of recovery hot path tests when stubbing
	return loadFingerprint(path)
}

var loadFingerprint = storage.EncryptionSourceFingerprint
