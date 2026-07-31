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

	"knox-media/internal/publication"

	"knox-media/internal/store"
)

const leaseDuration = 90 * time.Second

const recoverExpiredLimit = 100

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
	case TaskPoster, TaskPosterRepair, TaskThumbnail, TaskPreview, TaskKeyframe, TaskSubtitle, TaskAtrack, TaskEncrypt:
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

func nullableNullInt(id sql.NullInt64) any {
	if !id.Valid {
		return nil
	}
	return id.Int64
}

func nullableTaskID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func syncLinkedStepTx(ctx context.Context, tx *sql.Tx, taskID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE media_ingest_step SET
		status=(SELECT status FROM post_ingest_task WHERE id=?), attempts=(SELECT attempts FROM post_ingest_task WHERE id=?), max_attempts=(SELECT max_attempts FROM post_ingest_task WHERE id=?), last_error=(SELECT last_error FROM post_ingest_task WHERE id=?), available_at=(SELECT available_at FROM post_ingest_task WHERE id=?), lease_owner=(SELECT lease_owner FROM post_ingest_task WHERE id=?), lease_until=(SELECT lease_until FROM post_ingest_task WHERE id=?), started_at=(SELECT started_at FROM post_ingest_task WHERE id=?), finished_at=(SELECT finished_at FROM post_ingest_task WHERE id=?), updated_at=CURRENT_TIMESTAMP
		WHERE id=(SELECT ingest_step_id FROM post_ingest_task WHERE id=?)`, taskID, taskID, taskID, taskID, taskID, taskID, taskID, taskID, taskID, taskID)
	if err != nil {
		return err
	}
	var runID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT ingest_run_id FROM post_ingest_task WHERE id=?`, taskID).Scan(&runID); err != nil {
		return err
	}
	if runID.Valid {
		return publication.AggregateTx(ctx, tx, runID.Int64)
	}
	return nil
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
			INSERT INTO post_ingest_task (media_id, scan_task_id, generation, task_type)
			SELECT ?, ?, COALESCE(ingest_generation, 0), ? FROM media WHERE id=?
			ON CONFLICT(media_id, generation, task_type) DO NOTHING`, mediaID, nullableInt64(scanTaskID), typ, mediaID)
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

func (q *Queue) retry(ctx context.Context, id int64, scanTaskID *int64, explicit bool) error {
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
		statuses := "'failed','cancelled'"
		if explicit {
			statuses += ",'done'"
		}
		result, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='waiting', scan_task_id=?, last_error='', lease_owner=NULL, lease_until=NULL, started_at=NULL, finished_at=NULL, attempts=0, available_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN (`+statuses+`)`, nullableInt64(scanTaskID), id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		updated = n == 1
		if updated {
			if err := syncLinkedStepTx(ctx, tx, id); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if err != nil {
		return err
	}
	if updated {
		return nil
	}
	if explicit {
		return fmt.Errorf("postingest queue: task %d cannot be explicitly retried", id)
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

func (q *Queue) Retry(ctx context.Context, id int64, scanTaskID *int64) error {
	return q.retry(ctx, id, scanTaskID, false)
}

func (q *Queue) RetryExplicit(ctx context.Context, id int64, scanTaskID *int64) error {
	return q.retry(ctx, id, scanTaskID, true)
}

func (q *Queue) ClaimAny(ctx context.Context, types []TaskType) (*Task, error) {
	if err := q.validate(true); err != nil {
		return nil, err
	}
	requested := make([]string, 0, len(types))
	for _, typ := range types {
		if err := validateTaskType(typ); err != nil {
			return nil, err
		}
		requested = append(requested, string(typ))
	}
	payload, err := publication.ClaimEligibleAny(ctx, q.db, publication.ClaimRequest{Family: publication.QueuePostIngest, TaskTypes: requested, Owner: q.owner, Registry: q.registry, Metrics: q.metrics})
	if err != nil || payload == nil {
		return nil, err
	}
	return q.taskFromClaimPayload(ctx, payload)
}
func (q *Queue) Claim(ctx context.Context, typ TaskType) (*Task, error) {
	if err := q.validate(true); err != nil {
		return nil, err
	}
	if err := validateTaskType(typ); err != nil {
		return nil, err
	}
	payload, err := publication.ClaimEligible(ctx, q.db, publication.ClaimRequest{Family: publication.QueuePostIngest, TaskType: string(typ), Owner: q.owner, Registry: q.registry, Metrics: q.metrics})
	if err != nil || payload == nil {
		return nil, err
	}
	return q.taskFromClaimPayload(ctx, payload)
}

func (q *Queue) taskFromClaimPayload(ctx context.Context, payload *publication.ClaimPayload) (*Task, error) {
	var stillOwned int
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task WHERE id=? AND status='running' AND lease_owner=? AND ((ingest_run_id IS NULL AND ingest_step_id IS NULL AND ? IS NULL AND ? IS NULL) OR (ingest_run_id=? AND ingest_step_id=? AND generation=? AND retry_round=? AND media_id=? AND EXISTS(SELECT 1 FROM media_ingest_step st JOIN media_ingest_run r ON r.id=st.run_id JOIN media m ON m.id=st.media_id WHERE st.id=post_ingest_task.ingest_step_id AND st.status='running' AND st.lease_owner=post_ingest_task.lease_owner AND st.run_id=post_ingest_task.ingest_run_id AND st.media_id=post_ingest_task.media_id AND st.generation=post_ingest_task.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=post_ingest_task.generation)))`, payload.QueueID, payload.Owner, nullableNullInt(payload.RunID), nullableNullInt(payload.StepID), nullableNullInt(payload.RunID), nullableNullInt(payload.StepID), payload.Generation.Int64, payload.RetryRound, payload.MediaID).Scan(&stillOwned); err != nil {
		return nil, err
	}
	if stillOwned != 1 && TaskType(payload.TaskType) == TaskPosterRepair {
		err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task p JOIN media m ON m.id=p.media_id JOIN media_ingest_run r ON r.id=p.ingest_run_id WHERE p.id=? AND p.task_type='poster_repair' AND p.status='running' AND p.lease_owner=? AND p.ingest_step_id IS NULL AND p.generation=m.ingest_generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL AND r.id=p.ingest_run_id AND r.media_id=p.media_id AND r.generation=p.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL`, payload.QueueID, payload.Owner).Scan(&stillOwned)
		if err != nil {
			return nil, err
		}
	}
	if stillOwned != 1 {
		return nil, nil
	}
	task := &Task{ID: payload.QueueID, MediaID: payload.MediaID, Type: TaskType(payload.TaskType), Status: StatusRunning, Attempts: payload.Attempts, MaxAttempts: payload.MaxAttempts, RetryRound: payload.RetryRound, LeaseOwner: payload.Owner, LeaseUntil: payload.LeaseUntil}
	if payload.ScanTaskID.Valid {
		v := payload.ScanTaskID.Int64
		task.ScanTaskID = &v
	}
	if payload.RunID.Valid {
		v := payload.RunID.Int64
		task.RunID = &v
	}
	if payload.StepID.Valid {
		v := payload.StepID.Int64
		task.StepID = &v
	}
	if payload.Generation.Valid {
		v := payload.Generation.Int64
		task.Generation = v
	}
	return task, nil
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

func posterRepairTask(task Task) bool {
	return task.Type == TaskPosterRepair && task.RunID != nil && task.StepID == nil && task.Generation > 0
}

const posterRepairLifecyclePredicate = `id=? AND media_id=? AND task_type='poster_repair' AND status='running' AND lease_owner=? AND attempts=? AND ingest_run_id=? AND ingest_step_id IS NULL AND generation=? AND EXISTS(SELECT 1 FROM media m JOIN media_ingest_run r ON r.id=post_ingest_task.ingest_run_id WHERE m.id=post_ingest_task.media_id AND m.ingest_generation=post_ingest_task.generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL AND r.media_id=post_ingest_task.media_id AND r.generation=post_ingest_task.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL)`

func posterRepairArgs(t Task) []any {
	return []any{t.ID, t.MediaID, t.LeaseOwner, t.Attempts, *t.RunID, t.Generation}
}

func (q *Queue) Renew(ctx context.Context, task Task) (bool, error) {
	if err := q.validate(true); err != nil {
		return false, err
	}
	if err := q.validateClaimedTask(task); err != nil {
		return false, err
	}
	if posterRepairTask(task) {
		var renewed bool
		err := store.WithBusyRetry(ctx, q.metrics, func() error {
			args := append([]any{leaseModifier()}, posterRepairArgs(task)...)
			r, e := q.db.ExecContext(ctx, `UPDATE post_ingest_task SET lease_until=datetime(CURRENT_TIMESTAMP,?),updated_at=CURRENT_TIMESTAMP WHERE `+posterRepairLifecyclePredicate, args...)
			if e != nil {
				return e
			}
			n, _ := r.RowsAffected()
			renewed = n == 1
			return nil
		})
		return renewed, err
	}
	var renewed bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
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
		result, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET lease_until=datetime(CURRENT_TIMESTAMP, ?), updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND ((ingest_run_id IS NULL AND ingest_step_id IS NULL AND ? IS NULL AND ? IS NULL) OR (ingest_run_id=? AND ingest_step_id=? AND generation=? AND retry_round=? AND media_id=? AND EXISTS(SELECT 1 FROM media_ingest_step st JOIN media_ingest_run r ON r.id=st.run_id JOIN media m ON m.id=st.media_id WHERE st.id=post_ingest_task.ingest_step_id AND st.status='running' AND st.lease_owner=post_ingest_task.lease_owner AND st.run_id=post_ingest_task.ingest_run_id AND st.media_id=post_ingest_task.media_id AND st.generation=post_ingest_task.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=post_ingest_task.generation)))`, leaseModifier(), task.ID, task.LeaseOwner, nullableTaskID(task.RunID), nullableTaskID(task.StepID), nullableTaskID(task.RunID), nullableTaskID(task.StepID), task.Generation, task.RetryRound, task.MediaID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		renewed = n == 1
		if renewed {
			if err := syncLinkedStepTx(ctx, tx, task.ID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if err != nil || renewed {
		return renewed, err
	}
	var status Status
	if inspectErr := q.db.QueryRowContext(ctx, "SELECT status FROM post_ingest_task WHERE id=?", task.ID).Scan(&status); inspectErr != nil {
		return false, inspectErr
	}
	if status == StatusRunning {
		return false, nil
	}
	return false, q.ownerWriteError(ctx, task.ID, task.LeaseOwner, "renew")
}

func (q *Queue) Complete(ctx context.Context, task Task) error {
	if err := q.validate(true); err != nil {
		return err
	}
	if err := q.validateClaimedTask(task); err != nil {
		return err
	}
	if posterRepairTask(task) {
		r, e := q.db.ExecContext(ctx, `UPDATE post_ingest_task SET status='done',lease_owner=NULL,lease_until=NULL,last_error='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE `+posterRepairLifecyclePredicate, posterRepairArgs(task)...)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return q.ownerWriteError(ctx, task.ID, task.LeaseOwner, "complete")
		}
		return nil
	}
	var updated bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
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
		result, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status=CASE WHEN scan_task_id IS NOT NULL AND EXISTS (SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id AND (s.cancelled=1 OR s.status='cancelled')) THEN 'cancelled' ELSE 'done' END, lease_owner=NULL, lease_until=NULL, last_error=CASE WHEN scan_task_id IS NOT NULL AND EXISTS (SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id AND (s.cancelled=1 OR s.status='cancelled')) THEN 'scan cancelled' ELSE '' END, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND ((ingest_run_id IS NULL AND ingest_step_id IS NULL AND ? IS NULL AND ? IS NULL) OR (ingest_run_id=? AND ingest_step_id=? AND generation=? AND retry_round=? AND media_id=? AND EXISTS(SELECT 1 FROM media_ingest_step st JOIN media_ingest_run r ON r.id=st.run_id JOIN media m ON m.id=st.media_id WHERE st.id=post_ingest_task.ingest_step_id AND st.status='running' AND st.lease_owner=post_ingest_task.lease_owner AND st.run_id=post_ingest_task.ingest_run_id AND st.media_id=post_ingest_task.media_id AND st.generation=post_ingest_task.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=post_ingest_task.generation)))`, task.ID, task.LeaseOwner, nullableTaskID(task.RunID), nullableTaskID(task.StepID), nullableTaskID(task.RunID), nullableTaskID(task.StepID), task.Generation, task.RetryRound, task.MediaID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		updated = n == 1
		if updated {
			if err := syncLinkedStepTx(ctx, tx, task.ID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if err != nil {
		return err
	}
	if updated {
		return nil
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
	if posterRepairTask(*task) {
		status := "failed"
		available := "CURRENT_TIMESTAMP"
		if kind == FailureRetryable {
			status = "CASE WHEN attempts>=max_attempts THEN 'failed' ELSE 'waiting' END"
			available = "datetime(CURRENT_TIMESTAMP,'+5 seconds')"
		}
		qtext := fmt.Sprintf(`UPDATE post_ingest_task SET status=%s,available_at=%s,lease_owner=NULL,lease_until=NULL,last_error=?,finished_at=CASE WHEN %s='waiting' THEN NULL ELSE CURRENT_TIMESTAMP END,updated_at=CURRENT_TIMESTAMP WHERE `+posterRepairLifecyclePredicate, status, available, status)
		args := append([]any{failureText(cause)}, posterRepairArgs(*task)...)
		r, e := q.db.ExecContext(ctx, qtext, args...)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return q.ownerWriteError(ctx, task.ID, task.LeaseOwner, "fail")
		}
		return nil
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
			query = `UPDATE post_ingest_task SET last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND ((ingest_run_id IS NULL AND ingest_step_id IS NULL AND ? IS NULL AND ? IS NULL) OR (ingest_run_id=? AND ingest_step_id=? AND generation=? AND retry_round=? AND media_id=? AND EXISTS(SELECT 1 FROM media_ingest_step st JOIN media_ingest_run r ON r.id=st.run_id JOIN media m ON m.id=st.media_id WHERE st.id=post_ingest_task.ingest_step_id AND st.status='running' AND st.lease_owner=post_ingest_task.lease_owner AND st.run_id=post_ingest_task.ingest_run_id AND st.media_id=post_ingest_task.media_id AND st.generation=post_ingest_task.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=post_ingest_task.generation)))`
			args = []any{lastError, task.ID, task.LeaseOwner, nullableTaskID(task.RunID), nullableTaskID(task.StepID), nullableTaskID(task.RunID), nullableTaskID(task.StepID), task.Generation, task.RetryRound, task.MediaID}
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
			query = fmt.Sprintf(`UPDATE post_ingest_task SET status=%s, available_at=%s, lease_owner=NULL, lease_until=NULL, last_error=?, finished_at=CASE WHEN %s='waiting' THEN NULL ELSE CURRENT_TIMESTAMP END, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND ((ingest_run_id IS NULL AND ingest_step_id IS NULL AND ? IS NULL AND ? IS NULL) OR (ingest_run_id=? AND ingest_step_id=? AND generation=? AND retry_round=? AND media_id=? AND EXISTS(SELECT 1 FROM media_ingest_step st JOIN media_ingest_run r ON r.id=st.run_id JOIN media m ON m.id=st.media_id WHERE st.id=post_ingest_task.ingest_step_id AND st.status='running' AND st.lease_owner=post_ingest_task.lease_owner AND st.run_id=post_ingest_task.ingest_run_id AND st.media_id=post_ingest_task.media_id AND st.generation=post_ingest_task.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=post_ingest_task.generation)))`, status, available, status)
			args = []any{lastError, task.ID, task.LeaseOwner, nullableTaskID(task.RunID), nullableTaskID(task.StepID), nullableTaskID(task.RunID), nullableTaskID(task.StepID), task.Generation, task.RetryRound, task.MediaID}
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
		if updated {
			if err := syncLinkedStepTx(ctx, tx, task.ID); err != nil {
				return err
			}
		}
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

// RecoverExpired resets running tasks whose leases have expired so they can be reclaimed.
func (q *Queue) RecoverExpired(ctx context.Context) (int64, error) {
	return q.recoverRunning(ctx, true)
}

// RecoverInterrupted resets every running post-ingest task (startup / process takeover).
// Unlike RecoverExpired, it does not wait for lease expiry—safe because a new process
// owner never inherits in-memory executors from the previous process.
func (q *Queue) RecoverInterrupted(ctx context.Context) (int64, error) {
	return q.recoverRunning(ctx, false)
}

// RecoverAllInterrupted drains RecoverInterrupted until a batch returns zero rows.
func (q *Queue) RecoverAllInterrupted(ctx context.Context) (int64, error) {
	var total int64
	for {
		n, err := q.RecoverInterrupted(ctx)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
}

func (q *Queue) recoverRunning(ctx context.Context, onlyExpired bool) (int64, error) {
	if err := q.validate(false); err != nil {
		return 0, err
	}
	selectSQL := `SELECT id FROM post_ingest_task WHERE status='running'`
	updateSQL := `UPDATE post_ingest_task SET status=CASE WHEN scan_task_id IS NOT NULL AND EXISTS(SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id AND (s.cancelled=1 OR s.status='cancelled')) THEN 'cancelled' WHEN attempts>=max_attempts THEN 'failed' ELSE 'waiting' END,last_error=CASE WHEN attempts>=max_attempts THEN ? ELSE last_error END,available_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,finished_at=CASE WHEN attempts>=max_attempts OR (scan_task_id IS NOT NULL AND EXISTS(SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id AND (s.cancelled=1 OR s.status='cancelled'))) THEN CURRENT_TIMESTAMP ELSE NULL END,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running'`
	exhaustedMsg := "interrupted and attempts exhausted"
	if onlyExpired {
		selectSQL += ` AND lease_until<CURRENT_TIMESTAMP`
		updateSQL += ` AND lease_until<CURRENT_TIMESTAMP`
		exhaustedMsg = "lease expired and attempts exhausted"
	}
	selectSQL += ` ORDER BY id LIMIT ?`
	var n int64
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
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
		rows, err := tx.QueryContext(ctx, selectSQL, recoverExpiredLimit)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			result, err := tx.ExecContext(ctx, updateSQL, exhaustedMsg, id)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			n += changed
			if changed == 1 {
				if err := syncLinkedStepTx(ctx, tx, id); err != nil {
					return err
				}
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
	return n, err
}

func (q *Queue) CancelScan(ctx context.Context, id int64) (int64, error) {
	if err := q.validate(false); err != nil {
		return 0, err
	}
	if id <= 0 {
		return 0, fmt.Errorf("postingest queue: scan task id must be positive: %d", id)
	}
	var n int64
	_, err := store.WithImmediateConnTx(ctx, q.db, func(tx store.ImmediateConnTx) error {
		if scanErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task WHERE scan_task_id=? AND status IN ('waiting','running')`, id).Scan(&n); scanErr != nil {
			return scanErr
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT r.id FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.scan_task_id=? AND r.status='processing' AND r.superseded_by_generation IS NULL AND r.superseded_at IS NULL`, id)
		if err != nil {
			return err
		}
		var runIDs []int64
		for rows.Next() {
			var runID int64
			if err = rows.Scan(&runID); err != nil {
				rows.Close()
				return err
			}
			runIDs = append(runIDs, runID)
		}
		if err = rows.Close(); err != nil {
			return err
		}
		for _, runID := range runIDs {
			if _, err = publication.CancelRunTx(ctx, tx, runID, "scan_cancelled"); err != nil {
				return err
			}
		}
		// Legacy unlinked tasks still need durable cancellation.
		if _, err = tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error='scan cancelled',finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE scan_task_id=? AND ingest_run_id IS NULL AND status IN ('waiting','running')`, id); err != nil {
			return err
		}
		return nil
	})
	return n, err
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

// EncryptTaskRow is an admin list projection for encrypt queue rows.
type EncryptTaskRow struct {
	ID          int64
	MediaID     int64
	Title       string
	Status      Status
	Attempts    int
	MaxAttempts int
	LastError   string
	StartedAt   sql.NullString
	FinishedAt  sql.NullString
	AvailableAt sql.NullString
	LeaseOwner  sql.NullString
	LeaseUntil  sql.NullString
	UpdatedAt   sql.NullString
}

// ListEncrypt returns encrypt tasks newest-first, optionally filtered by status.
func (q *Queue) ListEncrypt(ctx context.Context, status string, limit int) ([]EncryptTaskRow, error) {
	if err := q.validate(false); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	status = strings.TrimSpace(status)
	query := `
SELECT p.id,p.media_id,COALESCE(m.title,''),p.status,p.attempts,p.max_attempts,COALESCE(p.last_error,''),
       CAST(p.started_at AS TEXT),CAST(p.finished_at AS TEXT),CAST(p.available_at AS TEXT),
       p.lease_owner,CAST(p.lease_until AS TEXT),CAST(p.updated_at AS TEXT)
FROM post_ingest_task p
LEFT JOIN media m ON m.id=p.media_id
WHERE p.task_type='encrypt'`
	args := []any{}
	if status != "" && status != "all" {
		query += ` AND p.status=?`
		args = append(args, status)
	}
	query += ` ORDER BY p.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]EncryptTaskRow, 0)
	for rows.Next() {
		var r EncryptTaskRow
		if err := rows.Scan(&r.ID, &r.MediaID, &r.Title, &r.Status, &r.Attempts, &r.MaxAttempts, &r.LastError,
			&r.StartedAt, &r.FinishedAt, &r.AvailableAt, &r.LeaseOwner, &r.LeaseUntil, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EnqueueEncryptManual inserts or reopens an encrypt task for on-demand encryption.
// Returns (taskID, alreadyActive, error). alreadyActive is true when waiting/running existed.
func (q *Queue) EnqueueEncryptManual(ctx context.Context, mediaID int64) (taskID int64, alreadyActive bool, err error) {
	if err := q.validate(false); err != nil {
		return 0, false, err
	}
	if mediaID <= 0 {
		return 0, false, fmt.Errorf("postingest queue: media id must be positive: %d", mediaID)
	}
	err = store.WithBusyRetry(ctx, q.metrics, func() error {
		taskID, alreadyActive = 0, false
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
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(ingest_generation,0) FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("postingest queue: media %d not found", mediaID)
			}
			return err
		}
		var existingID int64
		var existingStatus Status
		err = tx.QueryRowContext(ctx, `SELECT id,status FROM post_ingest_task WHERE media_id=? AND generation=? AND task_type='encrypt'`, mediaID, generation).Scan(&existingID, &existingStatus)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			switch existingStatus {
			case StatusWaiting, StatusRunning:
				taskID, alreadyActive = existingID, true
				if err := tx.Commit(); err != nil {
					return err
				}
				committed = true
				return nil
			case StatusFailed, StatusCancelled, StatusDone:
				result, e := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='waiting',last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,attempts=0,available_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND task_type='encrypt' AND status=?`, existingID, existingStatus)
				if e != nil {
					return e
				}
				n, e := result.RowsAffected()
				if e != nil {
					return e
				}
				if n != 1 {
					return fmt.Errorf("postingest queue: encrypt task %d reset raced", existingID)
				}
				if e := syncLinkedStepTx(ctx, tx, existingID); e != nil {
					return e
				}
				taskID = existingID
				if err := tx.Commit(); err != nil {
					return err
				}
				committed = true
				return nil
			default:
				return fmt.Errorf("postingest queue: encrypt task %d in status %s", existingID, existingStatus)
			}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,generation,task_type,status) VALUES(?,?,'encrypt','waiting')`, mediaID, generation)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		taskID = id
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
	return taskID, alreadyActive, err
}

// AdminCancelEncrypt marks an encrypt task cancelled (waiting or running).
func (q *Queue) AdminCancelEncrypt(ctx context.Context, id int64) error {
	return q.adminMutateEncrypt(ctx, id, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error='cancelled by admin',finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND task_type='encrypt' AND status IN ('waiting','running')`, "cancel")
}

// AdminResetEncrypt requeues failed/cancelled/done or stranded (expired-lease) running encrypt tasks.
func (q *Queue) AdminResetEncrypt(ctx context.Context, id int64) error {
	return q.adminMutateEncrypt(ctx, id, `UPDATE post_ingest_task SET status='waiting',last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,attempts=0,available_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND task_type='encrypt' AND (status IN ('failed','cancelled','done') OR (status='running' AND (lease_until IS NULL OR lease_until<CURRENT_TIMESTAMP)))`, "reset")
}

// AdminRemoveEncrypt deletes a non-running encrypt queue row.
func (q *Queue) AdminRemoveEncrypt(ctx context.Context, id int64) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
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
		var status Status
		var stepID sql.NullInt64
		var runID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT status,ingest_step_id,ingest_run_id FROM post_ingest_task WHERE id=? AND task_type='encrypt'`, id).Scan(&status, &stepID, &runID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("postingest queue: encrypt task %d not found", id)
			}
			return err
		}
		if status == StatusRunning {
			return fmt.Errorf("postingest queue: encrypt task %d is running", id)
		}
		if status != StatusWaiting && status != StatusFailed && status != StatusCancelled {
			return fmt.Errorf("postingest queue: encrypt task %d in status %s cannot be removed", id, status)
		}
		if stepID.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error='encrypt task removed',finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('waiting','running','failed')`, stepID.Int64); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM post_ingest_task WHERE id=? AND task_type='encrypt' AND status IN ('waiting','failed','cancelled')`, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("postingest queue: encrypt task %d remove raced", id)
		}
		if runID.Valid {
			if err := publication.AggregateTx(ctx, tx, runID.Int64); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		updated = true
		return nil
	})
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("postingest queue: encrypt task %d not removed", id)
	}
	return nil
}

func (q *Queue) adminMutateEncrypt(ctx context.Context, id int64, updateSQL, op string) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
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
		result, err := tx.ExecContext(ctx, updateSQL, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			if err := syncLinkedStepTx(ctx, tx, id); err != nil {
				return err
			}
			updated = true
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if err != nil {
		return err
	}
	if updated {
		return nil
	}
	var status Status
	var typ TaskType
	if err := q.db.QueryRowContext(ctx, `SELECT status,task_type FROM post_ingest_task WHERE id=?`, id).Scan(&status, &typ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("postingest queue: task %d not found", id)
		}
		return err
	}
	if typ != TaskEncrypt {
		return fmt.Errorf("postingest queue: task %d is not encrypt", id)
	}
	return fmt.Errorf("postingest queue: encrypt task %d in status %s cannot be %sed", id, status, op)
}
