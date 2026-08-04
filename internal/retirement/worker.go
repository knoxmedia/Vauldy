package retirement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"knox-media/internal/store"
)

// CrashSeams inject failures around durable filesystem/state transitions.
type CrashSeams struct {
	BeforeMove           func() error
	AfterMove            func() error
	BeforeStateCommit    func() error
	AfterStateCommit     func() error
	BeforeDelete         func() error
	AfterDelete          func() error
	BeforeVerify         func() error
	AfterVerify          func() error
	ImmediateTx          func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error)
	FileOps              FileOps
	QuarantineRoot       string
	MaxAttempts          int
	LeaseTTL             time.Duration
	ActiveConsumer       ActiveConsumerFunc
	Now                  func() time.Time
	OnRenew              func()
}

func (s CrashSeams) immediate(ctx context.Context, db *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
	if s.ImmediateTx != nil {
		return s.ImmediateTx(ctx, db, fn)
	}
	return store.WithImmediateConnTx(ctx, db, fn)
}

func (s CrashSeams) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s CrashSeams) maxAttempts() int {
	if s.MaxAttempts > 0 {
		return s.MaxAttempts
	}
	return DefaultMaxAttempts
}

func (s CrashSeams) leaseTTL() time.Duration {
	if s.LeaseTTL > 0 {
		return s.LeaseTTL
	}
	return DefaultLeaseTTL
}

func (s CrashSeams) ops() FileOps {
	if s.FileOps.Rename != nil || s.FileOps.Remove != nil {
		return s.FileOps
	}
	return defaultFileOps()
}

// Worker claims and executes plaintext retirement work.
type Worker struct {
	DB    *sql.DB
	Owner string
	Seams CrashSeams
}

// ClaimReady leases one ready/retryable_failed retirement row.
func (w *Worker) ClaimReady(ctx context.Context) (*Row, error) {
	if w == nil || w.DB == nil || strings.TrimSpace(w.Owner) == "" {
		return nil, ErrInvalidIdentity
	}
	owner := fmt.Sprintf("%s/%d", w.Owner, w.Seams.now().UnixNano())
	ttl := w.Seams.leaseTTL()
	var claimedID int64
	_, err := w.Seams.immediate(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		var id int64
		e := tx.QueryRowContext(ctx, `
SELECT id FROM media_plaintext_retirement
WHERE state IN ('ready','retryable_failed')
  AND (lease_until IS NULL OR lease_until < CURRENT_TIMESTAMP)
  AND (next_retry_at IS NULL OR next_retry_at <= CURRENT_TIMESTAMP)
ORDER BY updated_at ASC LIMIT 1`).Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotClaimable
		}
		if e != nil {
			return e
		}
		res, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET lease_owner=?, lease_until=datetime(CURRENT_TIMESTAMP, ?),
    attempts=attempts+1, last_attempt_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP,
    last_error='', state=CASE WHEN state='retryable_failed' THEN 'ready' ELSE state END
WHERE id=? AND state IN ('ready','retryable_failed')
  AND (lease_until IS NULL OR lease_until < CURRENT_TIMESTAMP)`,
			owner, fmt.Sprintf("+%d seconds", int(ttl.Seconds())), id)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrNotClaimable
		}
		claimedID = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	row, err := LoadRow(ctx, w.DB, claimedID)
	if err != nil {
		return nil, err
	}
	row.LeaseOwner = owner
	return &row, nil
}

// RenewLease extends an owned lease.
func (w *Worker) RenewLease(ctx context.Context, row Row) error {
	if w == nil || w.DB == nil || row.RetirementID <= 0 || row.LeaseOwner == "" {
		return ErrInvalidIdentity
	}
	ttl := w.Seams.leaseTTL()
	res, err := w.DB.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET lease_until=datetime(CURRENT_TIMESTAMP, ?), updated_at=CURRENT_TIMESTAMP
WHERE id=? AND lease_owner=? AND state IN ('ready','quarantining','quarantined','deleting')`,
		fmt.Sprintf("+%d seconds", int(ttl.Seconds())), row.RetirementID, row.LeaseOwner)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Execute runs one full quarantine→delete→verify attempt under the owned lease.
func (w *Worker) Execute(ctx context.Context, row Row) error {
	if w == nil || w.DB == nil {
		return ErrInvalidIdentity
	}
	current, err := LoadRow(ctx, w.DB, row.RetirementID)
	if err != nil {
		return err
	}
	if current.LeaseOwner != row.LeaseOwner {
		return ErrLeaseLost
	}

	root := w.Seams.QuarantineRoot
	if root == "" {
		root = ResolveQuarantineRoot(current.SourcePath, "")
	}
	attempt := current.Attempts
	if attempt <= 0 {
		attempt = 1
	}
	id := current.Identity
	id.Attempt = attempt

	// Post-delete / post-quarantine resume: skip live-source barrier when durable
	// quarantine identity proves prior retirement ownership of the deleted bytes.
	if canResumeDelete(current) {
		if n := attemptFromQuarantinePath(current.QuarantinePath); n > 0 {
			id.Attempt = n
		}
		if err := validateResumeEvidence(root, current, id); err != nil {
			return w.failClosedOperatorAttempt(ctx, current, row.LeaseOwner, err.Error(), err)
		}
		return w.executeFromQuarantined(ctx, current, row.LeaseOwner, root, id)
	}

	barrier := EvaluateBarrier(ctx, w.DB, current, BarrierOptions{ActiveConsumer: w.Seams.ActiveConsumer})
	if !barrier.Eligible {
		_, _ = w.DB.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='blocked', blocker_code=?, lease_owner=NULL, lease_until=NULL, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND lease_owner=?`, string(barrier.Blocker), current.RetirementID, row.LeaseOwner)
		return fmt.Errorf("%w: %s", ErrBarrierBlocked, barrier.Blocker)
	}

	if err := w.renewOrFail(ctx, row); err != nil {
		return w.failAttempt(ctx, current, row.LeaseOwner, err)
	}
	if err := w.markQuarantining(ctx, current, row.LeaseOwner, id, root); err != nil {
		return w.failAttempt(ctx, current, row.LeaseOwner, err)
	}
	current.State = StateQuarantining

	if w.Seams.BeforeMove != nil {
		if err := w.Seams.BeforeMove(); err != nil {
			return w.failAttempt(ctx, current, row.LeaseOwner, err)
		}
	}
	if err := w.renewOrFail(ctx, row); err != nil {
		return w.failAttempt(ctx, current, row.LeaseOwner, err)
	}
	qPath, qFP, err := MoveToQuarantine(current.SourcePath, root, id, w.Seams.ops())
	if err != nil {
		return w.failAttempt(ctx, current, row.LeaseOwner, err)
	}
	current.QuarantinePath = qPath
	current.QuarantineFingerprint = qFP
	if w.Seams.AfterMove != nil {
		if err := w.Seams.AfterMove(); err != nil {
			return w.failAttempt(ctx, current, row.LeaseOwner, err)
		}
	}
	if err := w.renewOrFail(ctx, row); err != nil {
		return w.failAttempt(ctx, current, row.LeaseOwner, err)
	}
	if w.Seams.BeforeStateCommit != nil {
		if err := w.Seams.BeforeStateCommit(); err != nil {
			return w.failAttempt(ctx, current, row.LeaseOwner, err)
		}
	}
	if err := w.markQuarantined(ctx, current, row.LeaseOwner, qPath, qFP); err != nil {
		return w.failAttempt(ctx, current, row.LeaseOwner, err)
	}
	current.State = StateQuarantined
	if w.Seams.AfterStateCommit != nil {
		if err := w.Seams.AfterStateCommit(); err != nil {
			return w.failAttempt(ctx, current, row.LeaseOwner, err)
		}
	}

	return w.executeFromQuarantined(ctx, current, row.LeaseOwner, root, id)
}

func canResumeDelete(row Row) bool {
	qPath := strings.TrimSpace(row.QuarantinePath)
	qFP := strings.TrimSpace(row.QuarantineFingerprint)
	if qPath == "" || qFP == "" {
		return false
	}
	if pathExists(row.SourcePath) {
		return false
	}
	// Source gone with durable quarantine identity: resume delete and/or verify-only.
	return true
}

// validateResumeEvidence fail-closes unless path layout + fingerprint prove the
// prior quarantine/delete belonged to this retirement identity.
func validateResumeEvidence(root string, row Row, id Identity) error {
	qPath := strings.TrimSpace(row.QuarantinePath)
	qFP := strings.TrimSpace(row.QuarantineFingerprint)
	if qPath == "" || qFP == "" {
		return fmt.Errorf("%w: resume missing quarantine identity/fingerprint", ErrUnsafeQuarantinePath)
	}
	if err := ValidateQuarantineLayout(root, qPath, id); err != nil {
		return err
	}
	return nil
}

func attemptFromQuarantinePath(path string) int {
	clean := filepath.ToSlash(path)
	parts := strings.Split(clean, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		if len(p) > 1 && p[0] == 'a' {
			var n int
			if _, err := fmt.Sscanf(p, "a%d", &n); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func (w *Worker) renewOrFail(ctx context.Context, row Row) error {
	if err := w.RenewLease(ctx, row); err != nil {
		return err
	}
	if w.Seams.OnRenew != nil {
		w.Seams.OnRenew()
	}
	return nil
}

func (w *Worker) executeFromQuarantined(ctx context.Context, current Row, owner, root string, id Identity) error {
	leaseRow := Row{Identity: Identity{RetirementID: current.RetirementID}, LeaseOwner: owner}

	qPath := strings.TrimSpace(current.QuarantinePath)
	qFP := strings.TrimSpace(current.QuarantineFingerprint)
	if qPath == "" || qFP == "" {
		return w.failAttempt(ctx, current, owner, fmt.Errorf("%w: resume missing quarantine identity", ErrUnsafeQuarantinePath))
	}

	if err := w.renewOrFail(ctx, leaseRow); err != nil {
		return w.failAttempt(ctx, current, owner, err)
	}
	if err := w.ensureQuarantinedForResume(ctx, current, owner, qPath, qFP); err != nil {
		return w.failAttempt(ctx, current, owner, err)
	}
	current.State = StateQuarantined
	current.QuarantinePath = qPath
	current.QuarantineFingerprint = qFP

	if err := w.markDeleting(ctx, current, owner); err != nil {
		return w.failAttempt(ctx, current, owner, err)
	}
	current.State = StateDeleting

	if err := w.renewOrFail(ctx, leaseRow); err != nil {
		return w.failAttempt(ctx, current, owner, err)
	}
	if w.Seams.BeforeDelete != nil {
		if err := w.Seams.BeforeDelete(); err != nil {
			return w.failAttempt(ctx, current, owner, err)
		}
	}
	if err := w.renewOrFail(ctx, leaseRow); err != nil {
		return w.failAttempt(ctx, current, owner, err)
	}
	if pathExists(qPath) {
		if err := DeleteQuarantine(root, qPath, qFP, id, w.Seams.ops()); err != nil {
			return w.failAttempt(ctx, current, owner, err)
		}
	}
	if w.Seams.AfterDelete != nil {
		if err := w.Seams.AfterDelete(); err != nil {
			return w.failAttempt(ctx, current, owner, err)
		}
	}
	if err := w.renewOrFail(ctx, leaseRow); err != nil {
		return w.failAttempt(ctx, current, owner, err)
	}
	if w.Seams.BeforeVerify != nil {
		if err := w.Seams.BeforeVerify(); err != nil {
			return w.failAttempt(ctx, current, owner, err)
		}
	}
	if err := VerifyAbsent(current.SourcePath, qPath); err != nil {
		return w.failAttempt(ctx, current, owner, err)
	}
	if w.Seams.AfterVerify != nil {
		if err := w.Seams.AfterVerify(); err != nil {
			return w.failAttempt(ctx, current, owner, err)
		}
	}
	return w.markVerified(ctx, current, owner, id, qPath, qFP)
}

func (w *Worker) ensureQuarantinedForResume(ctx context.Context, row Row, owner, qPath, qFP string) error {
	_, err := w.Seams.immediate(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='quarantined', quarantine_path=?, quarantine_fingerprint=?,
    quarantined_at=COALESCE(quarantined_at,CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP
WHERE id=? AND lease_owner=? AND state IN ('ready','quarantined','deleting')`, qPath, qFP, row.RetirementID, owner)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrLeaseLost
		}
		return nil
	})
	return err
}

func (w *Worker) markQuarantining(ctx context.Context, row Row, owner string, id Identity, root string) error {
	path, err := QuarantinePath(root, id)
	if err != nil {
		return err
	}
	evidence, _ := json.Marshal(map[string]any{
		"quarantine_root": root,
		"attempt":         id.Attempt,
		"retry_round":     id.RetryRound,
	})
	_, err = w.Seams.immediate(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='quarantining', quarantine_path=?, blocker_code='', updated_at=CURRENT_TIMESTAMP,
    quarantine_evidence_json=?
WHERE id=? AND lease_owner=? AND state IN ('ready','quarantining')`, path, string(evidence), row.RetirementID, owner)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrLeaseLost
		}
		_, e = tx.ExecContext(ctx, `
INSERT INTO media_plaintext_retirement_attempt(retirement_id,retry_round,attempt,outcome,source_fingerprint,quarantine_path,evidence_json)
VALUES(?,?,?,'started',?,?,?)
ON CONFLICT(retirement_id,retry_round,attempt) DO UPDATE SET outcome='started', started_at=CURRENT_TIMESTAMP`,
			row.RetirementID, row.RetryRound, id.Attempt, row.SourceFingerprint, path, string(evidence))
		return e
	})
	return err
}

func (w *Worker) markQuarantined(ctx context.Context, row Row, owner, qPath, qFP string) error {
	_, err := w.Seams.immediate(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='quarantined', quarantine_path=?, quarantine_fingerprint=?, quarantined_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND lease_owner=? AND state='quarantining'`, qPath, qFP, row.RetirementID, owner)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrLeaseLost
		}
		_, e = tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement_attempt
SET outcome='quarantined', quarantine_path=?, quarantine_fingerprint=?, finished_at=NULL
WHERE retirement_id=? AND retry_round=? AND attempt=?`, qPath, qFP, row.RetirementID, row.RetryRound, row.Attempts)
		return e
	})
	return err
}

func (w *Worker) markDeleting(ctx context.Context, row Row, owner string) error {
	_, err := w.Seams.immediate(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='deleting', deleting_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND lease_owner=? AND state='quarantined'`, row.RetirementID, owner)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrLeaseLost
		}
		return nil
	})
	return err
}

func (w *Worker) markVerified(ctx context.Context, row Row, owner string, id Identity, qPath, qFP string) error {
	_, err := w.Seams.immediate(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='verified', verified_at=CURRENT_TIMESTAMP, lease_owner=NULL, lease_until=NULL,
    last_error='', blocker_code='', updated_at=CURRENT_TIMESTAMP
WHERE id=? AND lease_owner=? AND state='deleting'`, row.RetirementID, owner)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrLeaseLost
		}
		_, e = tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement_attempt
SET outcome='verified', quarantine_path=?, quarantine_fingerprint=?, finished_at=CURRENT_TIMESTAMP
WHERE retirement_id=? AND retry_round=? AND attempt=?`, qPath, qFP, row.RetirementID, row.RetryRound, id.Attempt)
		return e
	})
	return err
}

func (w *Worker) failAttempt(ctx context.Context, row Row, owner string, cause error) error {
	limit := w.Seams.maxAttempts()
	msg := cause.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	// Restore uncommitted quarantine bytes before regressing state.
	if row.State == StateQuarantining {
		root := w.Seams.QuarantineRoot
		if root == "" {
			root = ResolveQuarantineRoot(row.SourcePath, "")
		}
		id := row.Identity
		if id.Attempt <= 0 {
			id.Attempt = row.Attempts
		}
		if id.Attempt <= 0 {
			id.Attempt = 1
		}
		qPath := strings.TrimSpace(row.QuarantinePath)
		if qPath == "" {
			qPath, _ = QuarantinePath(root, id)
		}
		if qPath != "" && pathExists(qPath) && !pathExists(row.SourcePath) {
			if restoreErr := RestoreQuarantine(qPath, row.SourcePath, root, id, w.Seams.ops()); restoreErr != nil {
				cause = errors.Join(cause, fmt.Errorf("restore: %w", restoreErr))
				msg = cause.Error()
				if len(msg) > 500 {
					msg = msg[:500]
				}
				return w.failClosedOperatorAttempt(ctx, row, owner, msg, cause)
			}
		}
	}
	// After quarantine commit, regress to retryable_failed so ClaimReady can resume
	// without ReconcileStartup. Preserve quarantine_path/fingerprint.
	if row.State == StateQuarantined || row.State == StateDeleting {
		escalate := row.Attempts >= limit
		_, err := w.Seams.immediate(ctx, w.DB, func(tx store.ImmediateConnTx) error {
			outcome := "retryable_failed"
			nextState := "retryable_failed"
			if escalate {
				outcome = "operator_required"
				nextState = "operator_required"
			}
			backoff := fmt.Sprintf("+%d seconds", minInt(300, 5*row.Attempts*row.Attempts+5))
			_, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state=?, last_error=?, lease_owner=NULL, lease_until=NULL,
    next_retry_at=CASE WHEN ?=1 THEN NULL ELSE datetime(CURRENT_TIMESTAMP, ?) END,
    updated_at=CURRENT_TIMESTAMP
WHERE id=? AND (lease_owner=? OR lease_owner IS NULL) AND state IN ('quarantined','deleting')`,
				nextState, msg, boolInt(escalate), backoff, row.RetirementID, owner)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `
INSERT INTO media_plaintext_retirement_attempt(retirement_id,retry_round,attempt,outcome,error_message,source_fingerprint,quarantine_path,finished_at)
VALUES(?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(retirement_id,retry_round,attempt) DO UPDATE SET
  outcome=excluded.outcome, error_message=excluded.error_message, finished_at=CURRENT_TIMESTAMP`,
				row.RetirementID, row.RetryRound, maxInt(row.Attempts, 1), outcome, msg, row.SourceFingerprint, row.QuarantinePath)
			return e
		})
		if err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	escalate := row.Attempts >= limit
	_, err := w.Seams.immediate(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		outcome := "retryable_failed"
		nextState := "retryable_failed"
		if escalate {
			outcome = "operator_required"
			nextState = "operator_required"
		}
		backoff := fmt.Sprintf("+%d seconds", minInt(300, 5*row.Attempts*row.Attempts+5))
		res, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state=?, last_error=?, lease_owner=NULL, lease_until=NULL,
    next_retry_at=CASE WHEN ?=1 THEN NULL ELSE datetime(CURRENT_TIMESTAMP, ?) END,
    updated_at=CURRENT_TIMESTAMP
WHERE id=? AND state IN ('ready','quarantining','quarantined','deleting','retryable_failed')
  AND (lease_owner=? OR lease_owner IS NULL OR ?='')`, nextState, msg, boolInt(escalate), backoff, row.RetirementID, owner, owner)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return fmt.Errorf("%w: failAttempt rows=%d state transition to %s", ErrLeaseLost, n, nextState)
		}
		_, e = tx.ExecContext(ctx, `
INSERT INTO media_plaintext_retirement_attempt(retirement_id,retry_round,attempt,outcome,error_message,source_fingerprint,quarantine_path,finished_at)
VALUES(?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(retirement_id,retry_round,attempt) DO UPDATE SET
  outcome=excluded.outcome, error_message=excluded.error_message, finished_at=CURRENT_TIMESTAMP`,
			row.RetirementID, row.RetryRound, maxInt(row.Attempts, 1), outcome, msg, row.SourceFingerprint, row.QuarantinePath)
		return e
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	// Encryption must remain done — never touch post_ingest_task here.
	return cause
}

func (w *Worker) failClosedOperatorAttempt(ctx context.Context, row Row, owner, msg string, cause error) error {
	_, err := w.Seams.immediate(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(ctx, `
UPDATE media_plaintext_retirement
SET state='operator_required', last_error=?, lease_owner=NULL, lease_until=NULL, next_retry_at=NULL, updated_at=CURRENT_TIMESTAMP
WHERE id=? AND (lease_owner=? OR lease_owner IS NULL) AND state IN ('ready','quarantining','quarantined','deleting','retryable_failed')`,
			msg, row.RetirementID, owner)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `
INSERT INTO media_plaintext_retirement_attempt(retirement_id,retry_round,attempt,outcome,error_message,source_fingerprint,quarantine_path,finished_at)
VALUES(?,?,?,'operator_required',?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(retirement_id,retry_round,attempt) DO UPDATE SET
  outcome='operator_required', error_message=excluded.error_message, finished_at=CURRENT_TIMESTAMP`,
			row.RetirementID, row.RetryRound, maxInt(row.Attempts, 1), msg, row.SourceFingerprint, row.QuarantinePath)
		return e
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// AssertEncryptionStillDone verifies encryption task was not regressed by retirement.
func AssertEncryptionStillDone(ctx context.Context, q store.SQLExecutor, mediaID, generation, basisID int64) error {
	if basisID <= 0 {
		return nil
	}
	var status string
	err := q.QueryRowContext(ctx, `
SELECT COALESCE(status,'') FROM post_ingest_task
WHERE id=? AND media_id=? AND generation=? AND task_type='encrypt'`, basisID, mediaID, generation).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "done" {
		return fmt.Errorf("retirement: encryption status=%s want done", status)
	}
	return nil
}
