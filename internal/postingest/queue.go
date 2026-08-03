package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"knox-media/internal/publication"
	"knox-media/internal/storage"

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
	case TaskPoster, TaskPosterRepair, TaskThumbnail, TaskPreview, TaskKeyframe, TaskSubtitle, TaskAtrack, TaskEncrypt, TaskSubtitleRecognize, TaskAIAnalysis:
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

// mirrorLinkedStepTx copies queue status/lease fields onto the linked ingest step.
func mirrorLinkedStepTx(ctx context.Context, tx store.SQLExecutor, taskID int64) (runID sql.NullInt64, err error) {
	_, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET
		status=(SELECT status FROM post_ingest_task WHERE id=?), attempts=(SELECT attempts FROM post_ingest_task WHERE id=?), max_attempts=(SELECT max_attempts FROM post_ingest_task WHERE id=?), retry_round=(SELECT retry_round FROM post_ingest_task WHERE id=?), last_error=(SELECT last_error FROM post_ingest_task WHERE id=?), available_at=(SELECT available_at FROM post_ingest_task WHERE id=?), lease_owner=(SELECT lease_owner FROM post_ingest_task WHERE id=?), lease_until=(SELECT lease_until FROM post_ingest_task WHERE id=?), started_at=(SELECT started_at FROM post_ingest_task WHERE id=?), finished_at=(SELECT finished_at FROM post_ingest_task WHERE id=?), updated_at=CURRENT_TIMESTAMP
		WHERE id=(SELECT ingest_step_id FROM post_ingest_task WHERE id=?)`, taskID, taskID, taskID, taskID, taskID, taskID, taskID, taskID, taskID, taskID, taskID)
	if err != nil {
		return runID, err
	}
	err = tx.QueryRowContext(ctx, `SELECT ingest_run_id FROM post_ingest_task WHERE id=?`, taskID).Scan(&runID)
	return runID, err
}

// syncLinkedStepLeaseTx mirrors running/lease metadata without plan projection.
// Use for heartbeat Renew and FailureShutdown last-error-only writes: status stays
// running, so waiting/running counts, dependency skips, retirement barrier, and
// aggregate must not thrash.
func syncLinkedStepLeaseTx(ctx context.Context, tx store.SQLExecutor, taskID int64) error {
	_, err := mirrorLinkedStepTx(ctx, tx, taskID)
	return err
}

// syncLinkedStepTx mirrors queue/step status and fully finalizes linked plan projection
// (propagate, completion, retirement barrier hook, aggregate) when the task belongs to
// an ingest run. Use for Complete/Fail/Cancel/Recover/Retry and other status changes.
func syncLinkedStepTx(ctx context.Context, tx store.SQLExecutor, taskID int64) error {
	runID, err := mirrorLinkedStepTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if runID.Valid {
		return publication.FinalizeNodeTransitionTx(ctx, tx, runID.Int64)
	}
	return nil
}

// SyncLinkedStepTx mirrors queue/step status for a post_ingest_task row inside an open
// transaction and finalizes linked plan projection (propagate, completion, retirement
// barrier hook, aggregate) when the task belongs to an ingest run.
func SyncLinkedStepTx(ctx context.Context, tx store.SQLExecutor, taskID int64) error {
	return syncLinkedStepTx(ctx, tx, taskID)
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
			INSERT INTO post_ingest_task (media_id, scan_task_id, generation, task_type, max_attempts, source_class, base_priority, library_id, resource_profile_version, resource_profile_json)
			SELECT ?, ?, COALESCE(ingest_generation, 0), ?, ?, 200, 200, library_id, ?, '{}' FROM media WHERE id=?
			ON CONFLICT(media_id, generation, task_type) DO NOTHING`, mediaID, nullableInt64(scanTaskID), typ, publication.DefaultMaxAttempts(string(typ)), publication.CurrentPolicyVersion, mediaID)
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
	task := &Task{ID: payload.QueueID, MediaID: payload.MediaID, Type: TaskType(payload.TaskType), Status: StatusRunning, Attempts: payload.Attempts, MaxAttempts: payload.MaxAttempts, RetryRound: payload.RetryRound, LeaseOwner: payload.Owner, LeaseUntil: payload.LeaseUntil, SourceClass: payload.SourceClass, BasePriority: payload.BasePriority, ResourceProfileVersion: payload.ResourceProfileVersion, ResourceProfileJSON: payload.ResourceProfileJSON}
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
	if payload.LibraryID.Valid {
		v := payload.LibraryID.Int64
		task.LibraryID = &v
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
			if err := syncLinkedStepLeaseTx(ctx, tx, task.ID); err != nil {
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
		releaseTaskPlaintextTemp(task)
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
		releaseTaskPlaintextTemp(task)
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
		if kind != FailureShutdown {
			releaseTaskPlaintextTemp(*task)
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
			sync := syncLinkedStepTx
			if kind == FailureShutdown {
				sync = syncLinkedStepLeaseTx
			}
			if err := sync(ctx, tx, task.ID); err != nil {
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
		if kind != FailureShutdown {
			releaseTaskPlaintextTemp(*task)
		}
		return nil
	}
	return q.ownerWriteError(ctx, task.ID, task.LeaseOwner, "fail")
}

func releaseTaskPlaintextTemp(task Task) {
	_ = storage.ReleaseBoundForTask(storage.BoundFromPostIngestTask(task.MediaID, task.Generation, task.ID, string(task.Type), task.LeaseOwner))
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
	// Identity includes media/generation so we can release lease-bound plaintext
	// temps after the previous lease ends. Requeue to waiting still releases:
	// the next claim must materialize a fresh bound under the same task id.
	selectSQL := `SELECT id, media_id, generation FROM post_ingest_task WHERE status='running'`
	updateSQL := `UPDATE post_ingest_task SET status=CASE WHEN scan_task_id IS NOT NULL AND EXISTS(SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id AND (s.cancelled=1 OR s.status='cancelled')) THEN 'cancelled' WHEN attempts>=max_attempts THEN 'failed' ELSE 'waiting' END,last_error=CASE WHEN attempts>=max_attempts THEN ? ELSE last_error END,available_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,finished_at=CASE WHEN attempts>=max_attempts OR (scan_task_id IS NOT NULL AND EXISTS(SELECT 1 FROM scan_task s WHERE s.id=post_ingest_task.scan_task_id AND (s.cancelled=1 OR s.status='cancelled'))) THEN CURRENT_TIMESTAMP ELSE NULL END,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running'`
	exhaustedMsg := "interrupted and attempts exhausted"
	if onlyExpired {
		selectSQL += ` AND lease_until<CURRENT_TIMESTAMP`
		updateSQL += ` AND lease_until<CURRENT_TIMESTAMP`
		exhaustedMsg = "lease expired and attempts exhausted"
	}
	selectSQL += ` ORDER BY id LIMIT ?`
	type recoverAttempt struct{ taskID, mediaID, generation int64 }
	var n int64
	var released []recoverAttempt
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		n = 0
		released = nil
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
		var attempts []recoverAttempt
		for rows.Next() {
			var a recoverAttempt
			if err := rows.Scan(&a.taskID, &a.mediaID, &a.generation); err != nil {
				_ = rows.Close()
				return err
			}
			attempts = append(attempts, a)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		var batch []recoverAttempt
		for _, a := range attempts {
			result, err := tx.ExecContext(ctx, updateSQL, exhaustedMsg, a.taskID)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			n += changed
			if changed == 1 {
				if err := syncLinkedStepTx(ctx, tx, a.taskID); err != nil {
					return err
				}
				batch = append(batch, a)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		released = batch
		return nil
	})
	if err != nil {
		return n, err
	}
	for _, a := range released {
		publication.ReleasePostIngestTempAttempt(a.mediaID, a.generation, a.taskID)
	}
	return n, nil
}

func (q *Queue) CancelScan(ctx context.Context, id int64) (int64, error) {
	if err := q.validate(false); err != nil {
		return 0, err
	}
	if id <= 0 {
		return 0, fmt.Errorf("postingest queue: scan task id must be positive: %d", id)
	}
	var n int64
	type legacyAttempt struct{ taskID, mediaID, generation int64 }
	var legacy []legacyAttempt
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
		legacyRows, err := tx.QueryContext(ctx, `SELECT id, media_id, generation FROM post_ingest_task WHERE scan_task_id=? AND ingest_run_id IS NULL AND status IN ('waiting','running')`, id)
		if err != nil {
			return err
		}
		legacy = nil
		for legacyRows.Next() {
			var a legacyAttempt
			if err = legacyRows.Scan(&a.taskID, &a.mediaID, &a.generation); err != nil {
				_ = legacyRows.Close()
				return err
			}
			legacy = append(legacy, a)
		}
		if err = legacyRows.Err(); err != nil {
			_ = legacyRows.Close()
			return err
		}
		if err = legacyRows.Close(); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error='scan cancelled',finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE scan_task_id=? AND ingest_run_id IS NULL AND status IN ('waiting','running')`, id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	for _, a := range legacy {
		publication.ReleasePostIngestTempAttempt(a.mediaID, a.generation, a.taskID)
	}
	return n, nil
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
// By default tombstoned rows are hidden; includeRemoved surfaces them.
func (q *Queue) ListEncrypt(ctx context.Context, status string, limit int, includeRemoved bool) ([]EncryptTaskRow, error) {
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
	if !includeRemoved {
		query += ` AND p.removed_at IS NULL`
	}
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
		_, err := q.withImmediate(ctx, func(tx store.ImmediateConnTx) error {
			var generation int64
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(ingest_generation,0) FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("postingest queue: media %d not found", mediaID)
				}
				return err
			}
			var existingID int64
			var existingStatus Status
			var prevRound, attempts int
			var removedAt sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT id,status,retry_round,attempts,CAST(removed_at AS TEXT) FROM post_ingest_task WHERE media_id=? AND generation=? AND task_type='encrypt'`, mediaID, generation).Scan(&existingID, &existingStatus, &prevRound, &attempts, &removedAt)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				switch existingStatus {
				case StatusWaiting, StatusRunning:
					if !removedAt.Valid {
						taskID, alreadyActive = existingID, true
						return nil
					}
					// Tombstoned waiting/running: reopen with monotonic retry_round.
				case StatusFailed, StatusCancelled, StatusDone:
				default:
					return fmt.Errorf("postingest queue: encrypt task %d in status %s", existingID, existingStatus)
				}
				nextRound := prevRound + 1
				action := "reset"
				reason := "enqueue_reopen"
				if removedAt.Valid {
					action = "reset_from_removed"
					reason = "enqueue_revive"
				}
				result, e := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='waiting',last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,attempts=0,retry_round=?,removed_at=NULL,removed_by='',remove_reason='',available_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND task_type='encrypt' AND generation=? AND retry_round=? AND (status IN ('failed','cancelled','done') OR removed_at IS NOT NULL)`, nextRound, existingID, generation, prevRound)
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
				if _, e = tx.ExecContext(ctx, `INSERT INTO media_encrypt_admin_audit(task_id,media_id,generation,action,actor_id,reason,previous_status,previous_attempts,previous_retry_round,new_retry_round,previous_error) VALUES(?,?,?,?,0,?,?,?,?,?,?)`, existingID, mediaID, generation, action, reason, existingStatus, attempts, prevRound, nextRound, ""); e != nil {
					return e
				}
				if e := syncLinkedStepTx(ctx, tx, existingID); e != nil {
					return e
				}
				taskID = existingID
				return nil
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,generation,task_type,status,max_attempts,source_class,base_priority,library_id,resource_profile_version,resource_profile_json) VALUES(?,?,'encrypt','waiting',?,400,400,(SELECT library_id FROM media WHERE id=?),?,'{}')`, mediaID, generation, publication.DefaultMaxAttempts(string(TaskEncrypt)), mediaID, publication.CurrentPolicyVersion)
			if err != nil {
				return err
			}
			id, err := result.LastInsertId()
			if err != nil {
				return err
			}
			taskID = id
			return nil
		})
		return err
	})
	return taskID, alreadyActive, err
}

// AdminCancelEncrypt marks an encrypt task cancelled (waiting or running).
func (q *Queue) AdminCancelEncrypt(ctx context.Context, id int64) error {
	return q.adminMutateEncrypt(ctx, id, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error='cancelled by admin',finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND task_type='encrypt' AND status IN ('waiting','running')`, "cancel")
}

// AdminCancelTask marks any post_ingest task cancelled (waiting or running).
func (q *Queue) AdminCancelTask(ctx context.Context, id int64) error {
	return q.adminMutateAny(ctx, id, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error='cancelled by admin',finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('waiting','running')`, "cancel")
}

// FindCurrentTask returns the current-generation queue row for media+type, if any.
func (q *Queue) FindCurrentTask(ctx context.Context, mediaID int64, typ TaskType) (id int64, status Status, err error) {
	if err := q.validate(false); err != nil {
		return 0, "", err
	}
	if mediaID <= 0 {
		return 0, "", fmt.Errorf("postingest queue: media id must be positive")
	}
	err = q.db.QueryRowContext(ctx, `
SELECT q.id, q.status FROM post_ingest_task q
WHERE q.media_id=? AND q.task_type=?
  AND q.generation=(SELECT COALESCE(ingest_generation,0) FROM media WHERE id=?)
ORDER BY q.id DESC LIMIT 1`, mediaID, typ, mediaID).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("postingest queue: %s task for media %d not found", typ, mediaID)
	}
	return id, status, err
}

// AdminBumpWaiting raises priority and makes a waiting task eligible immediately.
// Priority becomes MAX(priority)+1 so run-now jumps ahead of FIFO peers of the same type.
func (q *Queue) AdminBumpWaiting(ctx context.Context, id int64) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
	var updated bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		updated = false
		var next int64
		if err := q.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(priority),0)+1 FROM post_ingest_task`).Scan(&next); err != nil {
			return err
		}
		res, err := q.db.ExecContext(ctx, `
UPDATE post_ingest_task
SET priority=?, available_at=datetime('now','-100 years'), updated_at=CURRENT_TIMESTAMP
WHERE id=? AND status='waiting'`, next, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		updated = n == 1
		return nil
	})
	if err != nil {
		return err
	}
	if !updated {
		var status Status
		if qerr := q.db.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				return fmt.Errorf("postingest queue: task %d not found", id)
			}
			return qerr
		}
		return fmt.Errorf("postingest queue: task %d in status %s cannot be bumped", id, status)
	}
	return nil
}

func (q *Queue) adminMutateAny(ctx context.Context, id int64, updateSQL, op string) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
	var updated bool
	var mediaID, generation int64
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		updated = false
		mediaID, generation = 0, 0
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
		if err := tx.QueryRowContext(ctx, `SELECT media_id, generation FROM post_ingest_task WHERE id=?`, id).Scan(&mediaID, &generation); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
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
		// Admin cancel (and any mutate that ends the lease) drops lease-bound temps.
		publication.ReleasePostIngestTempAttempt(mediaID, generation, id)
		return nil
	}
	var status Status
	if err := q.db.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("postingest queue: task %d not found", id)
		}
		return err
	}
	return fmt.Errorf("postingest queue: task %d in status %s cannot be %sed", id, status, op)
}

// AdminResetEncrypt requeues failed/cancelled/done or stranded (expired-lease) running encrypt tasks.
// It increments retry_round monotonically and never reuses historical (task_id,retry_round,attempt) identity.
// Clearing a tombstone is audited as reset_from_removed.
func (q *Queue) AdminResetEncrypt(ctx context.Context, id, actorID int64) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
	var updated bool
	var mediaID, generation int64
	var nextRound int
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		updated = false
		mediaID, generation, nextRound = 0, 0, 0
		outcome, err := q.withImmediate(ctx, func(tx store.ImmediateConnTx) error {
			var status Status
			var attempts, retryRound int
			var lastError string
			var leaseUntil, removedAt sql.NullString
			var mediaGen int64
			err := tx.QueryRowContext(ctx, `SELECT p.media_id,p.generation,p.status,p.attempts,p.retry_round,COALESCE(p.last_error,''),CAST(p.lease_until AS TEXT),CAST(p.removed_at AS TEXT),COALESCE(m.ingest_generation,0)
FROM post_ingest_task p JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.task_type='encrypt'`, id).Scan(&mediaID, &generation, &status, &attempts, &retryRound, &lastError, &leaseUntil, &removedAt, &mediaGen)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("postingest queue: encrypt task %d not found", id)
			}
			if err != nil {
				return err
			}
			if generation > 0 && generation != mediaGen {
				return fmt.Errorf("postingest queue: encrypt task %d generation %d is stale (media generation %d)", id, generation, mediaGen)
			}
			switch status {
			case StatusFailed, StatusCancelled, StatusDone:
			case StatusRunning:
				if leaseUntil.Valid && leaseUntil.String != "" {
					var active int
					if e := tx.QueryRowContext(ctx, `SELECT CASE WHEN lease_until IS NOT NULL AND lease_until>=CURRENT_TIMESTAMP THEN 1 ELSE 0 END FROM post_ingest_task WHERE id=?`, id).Scan(&active); e != nil {
						return e
					}
					if active == 1 {
						return fmt.Errorf("postingest queue: encrypt task %d in status %s cannot be reset", id, status)
					}
				}
			default:
				return fmt.Errorf("postingest queue: encrypt task %d in status %s cannot be reset", id, status)
			}
			nextRound = retryRound + 1
			action, reason := "reset", "admin_reset"
			if removedAt.Valid {
				action, reason = "reset_from_removed", "admin_revive"
			}
			result, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='waiting',last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,attempts=0,retry_round=?,removed_at=NULL,removed_by='',remove_reason='',available_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND task_type='encrypt' AND retry_round=? AND (status IN ('failed','cancelled','done') OR (status='running' AND (lease_until IS NULL OR lease_until<CURRENT_TIMESTAMP)))`, nextRound, id, retryRound)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("postingest queue: encrypt task %d reset raced", id)
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO media_encrypt_admin_audit(task_id,media_id,generation,action,actor_id,reason,previous_status,previous_attempts,previous_retry_round,new_retry_round,previous_error) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, mediaID, generation, action, actorID, reason, status, attempts, retryRound, nextRound, lastError); err != nil {
				return err
			}
			if err := syncLinkedStepTx(ctx, tx, id); err != nil {
				return err
			}
			updated = true
			return nil
		})
		if err != nil && outcome.CommitAttempted {
			var round int
			if q.db.QueryRowContext(ctx, `SELECT retry_round FROM post_ingest_task WHERE id=?`, id).Scan(&round) == nil && nextRound > 0 && round == nextRound {
				updated = true
				return nil
			}
		}
		return err
	})
	if err != nil {
		return err
	}
	if updated {
		publication.ReleasePostIngestTempAttempt(mediaID, generation, id)
		return nil
	}
	return fmt.Errorf("postingest queue: encrypt task %d cannot be reset", id)
}

// AdminRemoveEncrypt logically tombstones a non-running encrypt queue row.
// The HTTP DELETE route is a compatibility logical-remove endpoint; physical purge is AdminPurgeEncrypt.
func (q *Queue) AdminRemoveEncrypt(ctx context.Context, id, actorID int64) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
	var updated bool
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		updated = false
		outcome, err := q.withImmediate(ctx, func(tx store.ImmediateConnTx) error {
			var status Status
			var mediaID, generation, mediaGen int64
			var attempts, retryRound int
			var lastError string
			var removedAt sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT p.media_id,p.generation,p.status,p.attempts,p.retry_round,COALESCE(p.last_error,''),CAST(p.removed_at AS TEXT),COALESCE(m.ingest_generation,0)
FROM post_ingest_task p JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.task_type='encrypt'`, id).Scan(&mediaID, &generation, &status, &attempts, &retryRound, &lastError, &removedAt, &mediaGen)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("postingest queue: encrypt task %d not found", id)
			}
			if err != nil {
				return err
			}
			if generation > 0 && generation != mediaGen {
				return fmt.Errorf("postingest queue: encrypt task %d generation %d is stale (media generation %d)", id, generation, mediaGen)
			}
			if removedAt.Valid {
				updated = true
				return nil
			}
			if status == StatusRunning {
				return fmt.Errorf("postingest queue: encrypt task %d is running", id)
			}
			if status != StatusWaiting && status != StatusFailed && status != StatusCancelled && status != StatusDone {
				return fmt.Errorf("postingest queue: encrypt task %d in status %s cannot be removed", id, status)
			}
			newStatus := status
			if status == StatusWaiting {
				newStatus = StatusCancelled
			}
			removedBy := strconv.FormatInt(actorID, 10)
			result, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status=?,removed_at=CURRENT_TIMESTAMP,removed_by=?,remove_reason='admin_remove',lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND task_type='encrypt' AND removed_at IS NULL AND status=? AND generation=?`, newStatus, removedBy, id, status, generation)
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
			if _, err = tx.ExecContext(ctx, `INSERT INTO media_encrypt_admin_audit(task_id,media_id,generation,action,actor_id,reason,previous_status,previous_attempts,previous_retry_round,new_retry_round,previous_error) VALUES(?,?,?,'remove',?,'admin_remove',?,?,?,?,?)`, id, mediaID, generation, actorID, status, attempts, retryRound, retryRound, lastError); err != nil {
				return err
			}
			// Do not cancel linked steps or delete journals: recovery continues through the tombstone.
			if err := syncLinkedStepTx(ctx, tx, id); err != nil {
				return err
			}
			updated = true
			return nil
		})
		if err != nil && outcome.CommitAttempted {
			var removed sql.NullString
			if q.db.QueryRowContext(ctx, `SELECT CAST(removed_at AS TEXT) FROM post_ingest_task WHERE id=?`, id).Scan(&removed) == nil && removed.Valid {
				updated = true
				return nil
			}
		}
		return err
	})
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("postingest queue: encrypt task %d not removed", id)
	}
	return nil
}

func (q *Queue) appendPurgeRejectedAudit(ctx context.Context, id, mediaID, generation, actorID int64, reason string) {
	_, _ = store.WithImmediateConnTx(ctx, q.db, func(tx store.ImmediateConnTx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO media_encrypt_admin_audit(task_id,media_id,generation,action,actor_id,reason,previous_status,previous_attempts,previous_retry_round,new_retry_round) VALUES(?,?,?,'purge_rejected',?,?, 'removed',0,0,0)`, id, mediaID, generation, actorID, reason)
		return err
	})
}

// AdminPurgeEncrypt physically deletes a tombstoned encrypt row when no journal,
// retirement, or dependency references remain. Audits alone do not block: they are
// archived then removed in the same transaction so ON DELETE RESTRICT is satisfied.
func (q *Queue) AdminPurgeEncrypt(ctx context.Context, id, actorID int64) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
	return store.WithBusyRetry(ctx, q.metrics, func() error {
		var mediaID, generation int64
		var rejectReason string
		_, err := q.withImmediate(ctx, func(tx store.ImmediateConnTx) error {
			var removedAt sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT media_id,generation,CAST(removed_at AS TEXT) FROM post_ingest_task WHERE id=? AND task_type='encrypt'`, id).Scan(&mediaID, &generation, &removedAt)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("postingest queue: encrypt task %d not found", id)
			}
			if err != nil {
				return err
			}
			if !removedAt.Valid {
				return fmt.Errorf("postingest queue: encrypt task %d is not tombstoned", id)
			}
			var journals, retirements, deps int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_encryption_stage_journal WHERE task_id=?`, id).Scan(&journals); err != nil {
				return err
			}
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_plaintext_retirement WHERE media_id=? AND generation=?`, mediaID, generation).Scan(&retirements); err != nil {
				return err
			}
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN post_ingest_task p ON p.ingest_step_id=d.step_id OR p.ingest_step_id=d.depends_on_step_id WHERE p.id=?`, id).Scan(&deps); err != nil {
				return err
			}
			if journals > 0 {
				rejectReason = "journal_refs"
				return fmt.Errorf("postingest queue: encrypt task %d purge rejected while journal references remain", id)
			}
			if retirements > 0 {
				rejectReason = "retirement_refs"
				return fmt.Errorf("postingest queue: encrypt task %d purge rejected while retirement references remain", id)
			}
			if deps > 0 {
				rejectReason = "dependency_refs"
				return fmt.Errorf("postingest queue: encrypt task %d purge rejected while dependency references remain", id)
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO media_encrypt_admin_audit_archive(task_id,media_id,generation,action,actor_id,reason,previous_status,previous_attempts,previous_retry_round,new_retry_round,previous_error,created_at,archived_at)
SELECT task_id,media_id,generation,action,actor_id,reason,previous_status,previous_attempts,previous_retry_round,new_retry_round,previous_error,created_at,CURRENT_TIMESTAMP FROM media_encrypt_admin_audit WHERE task_id=?`, id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO media_encrypt_admin_audit_archive(task_id,media_id,generation,action,actor_id,reason,previous_status,previous_attempts,previous_retry_round,new_retry_round) VALUES(?,?,?,'purge',?,'admin_purge','removed',0,0,0)`, id, mediaID, generation, actorID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM media_encrypt_admin_audit WHERE task_id=?`, id); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `DELETE FROM post_ingest_task WHERE id=? AND task_type='encrypt' AND removed_at IS NOT NULL`, id)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("postingest queue: encrypt task %d purge raced", id)
			}
			return nil
		})
		if err != nil && rejectReason != "" {
			q.appendPurgeRejectedAudit(ctx, id, mediaID, generation, actorID, rejectReason)
		}
		return err
	})
}

func (q *Queue) adminMutateEncrypt(ctx context.Context, id int64, updateSQL, op string) error {
	if err := q.validate(false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("postingest queue: task id must be positive: %d", id)
	}
	var updated bool
	var mediaID, generation int64
	err := store.WithBusyRetry(ctx, q.metrics, func() error {
		updated = false
		mediaID, generation = 0, 0
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
		if err := tx.QueryRowContext(ctx, `SELECT media_id, generation FROM post_ingest_task WHERE id=? AND task_type='encrypt'`, id).Scan(&mediaID, &generation); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
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
		publication.ReleasePostIngestTempAttempt(mediaID, generation, id)
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
