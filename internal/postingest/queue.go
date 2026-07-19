package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"knox-media/internal/store"
)

const leaseDuration = 90 * time.Second

func leaseModifier() string {
	return fmt.Sprintf("+%d seconds", int64(leaseDuration/time.Second))
}

func (q *Queue) validate(needOwner bool) error {
	if q == nil || q.db == nil {
		return errors.New("postingest queue: database is not configured")
	}
	if needOwner {
		owner := strings.TrimSpace(q.owner)
		if owner == "" {
			return errors.New("postingest queue: owner is not configured")
		}
		if owner != q.owner || strings.Contains(owner, "/") {
			return fmt.Errorf("postingest queue: owner %q is invalid: must be trimmed and must not contain /", q.owner)
		}
	}
	return nil
}

func validTaskType(typ TaskType) bool {
	switch typ {
	case TaskPoster, TaskPreview, TaskKeyframe, TaskSubtitle, TaskAtrack, TaskEncrypt:
		return true
	default:
		return false
	}
}

func validateTaskType(typ TaskType) error {
	if !validTaskType(typ) {
		return fmt.Errorf("postingest queue: invalid task type %q", typ)
	}
	return nil
}

func validateScanTaskID(id *int64) error {
	if id != nil && *id <= 0 {
		return fmt.Errorf("postingest queue: scan task id must be positive: %d", *id)
	}
	return nil
}

func nullableInt64(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func (q *Queue) Enqueue(ctx context.Context, mediaID int64, scanTaskID *int64, typ TaskType) (bool, error) {
	if err := q.validate(false); err != nil {
		return false, err
	}
	if mediaID <= 0 {
		return false, fmt.Errorf("postingest queue: media id must be positive: %d", mediaID)
	}
	if err := validateTaskType(typ); err != nil {
		return false, err
	}
	if err := validateScanTaskID(scanTaskID); err != nil {
		return false, err
	}

	var inserted bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		result, err := q.db.ExecContext(ctx, `
			INSERT INTO post_ingest_task (media_id, scan_task_id, task_type)
			VALUES (?, ?, ?)
			ON CONFLICT(media_id, task_type) DO NOTHING`, mediaID, nullableInt64(scanTaskID), typ)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		inserted = n == 1
		return nil
	})
	return inserted, err
}

func (q *Queue) Retry(ctx context.Context, id int64, scanTaskID *int64) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
	if err := validateScanTaskID(scanTaskID); err != nil {
		return err
	}

	var updated bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		result, err := q.db.ExecContext(ctx, `
			UPDATE post_ingest_task
			SET status='waiting', scan_task_id=?, last_error='', lease_owner=NULL,
				lease_until=NULL, started_at=NULL, finished_at=NULL, attempts=0,
				available_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND status IN ('failed','cancelled')`, nullableInt64(scanTaskID), id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		updated = n == 1
		return nil
	})
	if err != nil || updated {
		return err
	}

	var status Status
	if err := q.db.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("postingest queue: task %d not found", id)
		}
		return fmt.Errorf("postingest queue: inspect task %d after retry: %w", id, err)
	}
	return fmt.Errorf("postingest queue: task %d in status %s cannot be retried", id, status)
}

func (q *Queue) RetryExplicit(ctx context.Context, id int64, scanTaskID *int64) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
	if err := validateScanTaskID(scanTaskID); err != nil {
		return err
	}
	var updated bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		updated = false
		result, err := q.db.ExecContext(ctx, `UPDATE post_ingest_task SET status='waiting',scan_task_id=?,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,attempts=0,available_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('failed','cancelled','done')`, nullableInt64(scanTaskID), id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		updated = n == 1
		return nil
	})
	if err != nil {
		return err
	}
	if updated {
		return nil
	}
	return fmt.Errorf("postingest queue: task %d cannot be explicitly retried", id)
}
func (q *Queue) Claim(ctx context.Context, typ TaskType) (*Task, error) {
	if err := q.validate(true); err != nil {
		return nil, err
	}
	if err := validateTaskType(typ); err != nil {
		return nil, err
	}

	var claimed *Task
	leaseToken := q.owner + "/" + uuid.NewString()
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		claimed = nil
		tx, err := q.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		var id int64
		err = tx.QueryRowContext(ctx, `
			SELECT p.id
			FROM post_ingest_task p
			LEFT JOIN scan_task s ON s.id=p.scan_task_id
			WHERE p.task_type=? AND p.status='waiting'
			  AND p.available_at<=CURRENT_TIMESTAMP AND p.attempts<p.max_attempts
			  AND (p.scan_task_id IS NULL OR (s.cancelled=0 AND s.status<>'cancelled'))
			ORDER BY p.created_at, p.id
			LIMIT 1`, typ).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if err = tx.Commit(); err != nil {
				return err
			}
			committed = true
			return nil
		}
		if err != nil {
			return err
		}

		result, err := tx.ExecContext(ctx, `
			UPDATE post_ingest_task
			SET status='running', lease_owner=?,
				lease_until=datetime(CURRENT_TIMESTAMP, ?),
				started_at=COALESCE(started_at, CURRENT_TIMESTAMP),
				attempts=attempts+1, updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND task_type=? AND status='waiting'
			  AND available_at<=CURRENT_TIMESTAMP AND attempts<max_attempts
			  AND (scan_task_id IS NULL OR EXISTS (
				SELECT 1 FROM scan_task s
				WHERE s.id=post_ingest_task.scan_task_id
				  AND s.cancelled=0 AND s.status<>'cancelled'
			  ))`, leaseToken, leaseModifier(), id, typ)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			if err = tx.Commit(); err != nil {
				return err
			}
			committed = true
			return nil
		}

		task := &Task{}
		var scanID sql.NullInt64
		var leaseOwner, lastError sql.NullString
		var leaseUntil time.Time
		err = tx.QueryRowContext(ctx, `
			SELECT id, media_id, scan_task_id, task_type, status, attempts,
				max_attempts, lease_owner, lease_until, last_error
			FROM post_ingest_task WHERE id=?`, id).Scan(
			&task.ID, &task.MediaID, &scanID, &task.Type, &task.Status,
			&task.Attempts, &task.MaxAttempts, &leaseOwner, &leaseUntil, &lastError)
		if err != nil {
			return err
		}
		if scanID.Valid {
			task.ScanTaskID = &scanID.Int64
		}
		task.LeaseOwner = leaseOwner.String
		task.LeaseUntil = leaseUntil
		task.LastError = lastError.String
		if err = tx.Commit(); err != nil {
			return err
		}
		committed = true
		claimed = task
		return nil
	})
	return claimed, err
}

func (q *Queue) validateClaimedTask(task Task) error {
	if task.ID <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", task.ID)
	}
	if task.Attempts <= 0 {
		return fmt.Errorf("postingest queue: task %d attempts must be positive: %d", task.ID, task.Attempts)
	}
	owner, suffix, found := strings.Cut(task.LeaseOwner, "/")
	if !found || owner != q.owner || suffix == "" || strings.Contains(suffix, "/") {
		return fmt.Errorf("postingest queue: task %d ownership token %q does not belong to owner %q", task.ID, task.LeaseOwner, q.owner)
	}
	parsed, err := uuid.Parse(suffix)
	if err != nil || parsed.String() != suffix {
		return fmt.Errorf("postingest queue: task %d ownership token %q has invalid UUID", task.ID, task.LeaseOwner)
	}
	return nil
}

func (q *Queue) Renew(ctx context.Context, task Task) (bool, error) {
	if err := q.validate(true); err != nil {
		return false, err
	}
	if err := q.validateClaimedTask(task); err != nil {
		return false, err
	}
	var renewed bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		result, err := q.db.ExecContext(ctx, `UPDATE post_ingest_task SET lease_until=datetime(CURRENT_TIMESTAMP, ?), updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=?`, leaseModifier(), task.ID, task.LeaseOwner)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		renewed = n == 1
		return err
	})
	return renewed, err
}

func (q *Queue) Complete(ctx context.Context, task Task) error {
	if err := q.validate(true); err != nil {
		return err
	}
	if err := q.validateClaimedTask(task); err != nil {
		return err
	}
	var updated bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		result, err := q.db.ExecContext(ctx, `
			UPDATE post_ingest_task SET
				status=CASE WHEN scan_task_id IS NOT NULL AND EXISTS (
					SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id
					AND (s.cancelled=1 OR s.status='cancelled')
				) THEN 'cancelled' ELSE 'done' END,
				lease_owner=NULL, lease_until=NULL,
				last_error=CASE WHEN scan_task_id IS NOT NULL AND EXISTS (
					SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id
					AND (s.cancelled=1 OR s.status='cancelled')
				) THEN 'scan cancelled' ELSE '' END,
				finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND status='running' AND lease_owner=?`, task.ID, task.LeaseOwner)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		updated = n == 1
		return err
	})
	if err != nil || updated {
		return err
	}
	return q.ownerWriteError(ctx, task.ID, task.LeaseOwner, "complete")
}

func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func failureText(cause error) string {
	if cause == nil {
		return "post-ingest task failed without an error"
	}
	return truncateUTF8(cause.Error(), 4096)
}

func (q *Queue) Fail(ctx context.Context, task *Task, kind FailureKind, cause error) error {
	if err := q.validate(true); err != nil {
		return err
	}
	if task == nil {
		return errors.New("postingest queue: valid task is required")
	}
	if err := q.validateClaimedTask(*task); err != nil {
		return err
	}
	if kind < FailureRetryable || kind > FailureShutdown {
		return fmt.Errorf("postingest queue: invalid failure kind %d", kind)
	}
	requestedKind := kind
	requestedError := failureText(cause)
	var updated bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		updated = false
		tx, err := q.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		kind := requestedKind
		lastError := requestedError
		if task.ScanTaskID != nil {
			var cancelled bool
			var scanStatus string
			if err := tx.QueryRowContext(ctx, `SELECT cancelled,status FROM scan_task WHERE id=?`, *task.ScanTaskID).Scan(&cancelled, &scanStatus); err != nil {
				return fmt.Errorf("postingest queue: check scan cancellation before fail: %w", err)
			}
			if cancelled || scanStatus == "cancelled" {
				kind = FailureCancelled
				lastError = "scan cancelled"
			}
		}
		if q.beforeFailTransition != nil {
			q.beforeFailTransition()
		}

		var query string
		var args []any
		if kind == FailureShutdown {
			query = `UPDATE post_ingest_task SET last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=?`
			args = []any{lastError, task.ID, task.LeaseOwner}
		} else {
			status := "'failed'"
			available := "CURRENT_TIMESTAMP"
			if kind == FailureCancelled {
				status = "'cancelled'"
			}
			if kind == FailureRetryable {
				status = `CASE WHEN attempts>=max_attempts THEN 'failed' ELSE 'waiting' END`
				available = `CASE WHEN attempts>=max_attempts THEN CURRENT_TIMESTAMP WHEN attempts=1 THEN datetime(CURRENT_TIMESTAMP,'+5 seconds') WHEN attempts=2 THEN datetime(CURRENT_TIMESTAMP,'+30 seconds') ELSE datetime(CURRENT_TIMESTAMP,'+120 seconds') END`
			}
			query = fmt.Sprintf(`UPDATE post_ingest_task SET status=%s, available_at=%s, lease_owner=NULL, lease_until=NULL, last_error=?, finished_at=CASE WHEN %s='waiting' THEN NULL ELSE CURRENT_TIMESTAMP END, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=?`, status, available, status)
			args = []any{lastError, task.ID, task.LeaseOwner}
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		updated = n == 1
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if err != nil || updated {
		return err
	}
	return q.ownerWriteError(ctx, task.ID, task.LeaseOwner, "fail")
}

func (q *Queue) ownerWriteError(ctx context.Context, taskID int64, leaseToken string, operation string) error {
	var status Status
	var owner sql.NullString
	if err := q.db.QueryRowContext(ctx, `SELECT status,lease_owner FROM post_ingest_task WHERE id=?`, taskID).Scan(&status, &owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("postingest queue: task %d not found", taskID)
		}
		return fmt.Errorf("postingest queue: inspect task %d: %w", taskID, err)
	}
	if status != StatusRunning {
		return fmt.Errorf("postingest queue: cannot %s task %d in status %s", operation, taskID, status)
	}
	return fmt.Errorf("postingest queue: cannot %s task %d: ownership token mismatch, current %q, supplied %q", operation, taskID, owner.String, leaseToken)
}

func (q *Queue) RecoverExpired(ctx context.Context) (int64, error) {
	if err := q.validate(false); err != nil {
		return 0, err
	}
	var affected int64
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		result, err := q.db.ExecContext(ctx, `UPDATE post_ingest_task SET
			status=CASE WHEN scan_task_id IS NOT NULL AND EXISTS(SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id AND (s.cancelled=1 OR s.status='cancelled')) THEN 'cancelled' WHEN attempts>=max_attempts THEN 'failed' ELSE 'waiting' END,
			last_error=CASE WHEN scan_task_id IS NOT NULL AND EXISTS(SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id AND (s.cancelled=1 OR s.status='cancelled')) THEN 'scan cancelled' WHEN attempts>=max_attempts THEN 'lease expired and attempts exhausted' ELSE last_error END,
			available_at=CURRENT_TIMESTAMP, lease_owner=NULL, lease_until=NULL,
			finished_at=CASE WHEN (scan_task_id IS NOT NULL AND EXISTS(SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id AND (s.cancelled=1 OR s.status='cancelled'))) OR attempts>=max_attempts THEN CURRENT_TIMESTAMP ELSE NULL END,
			updated_at=CURRENT_TIMESTAMP WHERE status='running' AND lease_until<CURRENT_TIMESTAMP`)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, err
}

func (q *Queue) CancelScan(ctx context.Context, scanTaskID int64) (int64, error) {
	if err := q.validate(false); err != nil {
		return 0, err
	}
	if scanTaskID <= 0 {
		return 0, fmt.Errorf("postingest queue: scan task id must be positive: %d", scanTaskID)
	}
	var affected int64
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		result, err := q.db.ExecContext(ctx, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error='scan cancelled',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE scan_task_id=? AND status='waiting'`, scanTaskID)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, err
}

func (q *Queue) IsScanCancelled(ctx context.Context, scanTaskID int64) (bool, error) {
	if q.isScanCancelled != nil {
		return q.isScanCancelled(ctx, scanTaskID)
	}
	if err := q.validate(false); err != nil {
		return false, err
	}
	if scanTaskID <= 0 {
		return false, fmt.Errorf("postingest queue: scan task id must be positive: %d", scanTaskID)
	}
	var cancelled bool
	var status string
	if err := q.db.QueryRowContext(ctx, `SELECT cancelled,status FROM scan_task WHERE id=?`, scanTaskID).Scan(&cancelled, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("postingest queue: scan task %d not found", scanTaskID)
		}
		return false, err
	}
	return cancelled || status == "cancelled", nil
}
