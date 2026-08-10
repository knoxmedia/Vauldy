package publication

import (
	"context"
	"fmt"
	"knox-media/internal/store"
)

// CancelRunTx records an explicit whole-run cancellation before fencing all
// outstanding work. The caller owns the transaction so intent and work
// cancellation commit or roll back together.
//
// Post-ingest plaintext temps for cancelled waiting/running tasks are released
// after the cancel statements succeed. Release uses media/generation/task
// identity (not lease_owner) because this path nulls the lease. If the outer
// transaction later rolls back, temps may already be gone — preferred over
// orphaning on successful commit; a subsequent claim rematerializes.
func CancelRunTx(ctx context.Context, tx store.SQLExecutor, runID int64, reason string) (bool, error) {
	if tx == nil || runID <= 0 || reason == "" {
		return false, fmt.Errorf("publication cancel: invalid transaction, run, or reason")
	}
	res, err := tx.ExecContext(ctx, `UPDATE media_ingest_run SET status='cancelled',terminal_reason=?,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='processing' AND superseded_by_generation IS NULL AND superseded_at IS NULL AND EXISTS(SELECT 1 FROM media m WHERE m.id=media_ingest_run.media_id AND m.ingest_generation=media_ingest_run.generation)`, reason, runID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}

	// Capture identities before lease_owner is cleared.
	rows, err := tx.QueryContext(ctx, `SELECT id, media_id, generation FROM post_ingest_task WHERE ingest_run_id=? AND status IN ('waiting','running')`, runID)
	if err != nil {
		return false, err
	}
	type attempt struct{ taskID, mediaID, generation int64 }
	var attempts []attempt
	for rows.Next() {
		var a attempt
		if err := rows.Scan(&a.taskID, &a.mediaID, &a.generation); err != nil {
			_ = rows.Close()
			return false, err
		}
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}

	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE media_ingest_step SET status='cancelled',last_error=CASE WHEN last_error='' THEN ? ELSE last_error END,lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND status IN ('waiting','running')`, []any{reason, runID}},
		{`UPDATE post_ingest_task SET status='cancelled',last_error=CASE WHEN last_error='' THEN ? ELSE last_error END,lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND status IN ('waiting','running')`, []any{reason, runID}},
		{`UPDATE scrape_task SET status='cancelled',message=CASE WHEN COALESCE(message,'')='' THEN ? ELSE message END,lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND status IN ('waiting','running')`, []any{reason, runID}},
		{`UPDATE transcode_task SET status='cancelled',error_message=CASE WHEN COALESCE(error_message,'')='' THEN ? ELSE error_message END,lease_owner=NULL,lease_until=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND status IN ('waiting','running','paused')`, []any{reason, runID}},
	}
	// The community build has no pretranscode_rendition_job table; cancel its
	// rendition jobs only when the commercial schema is present.
	if jobsExist, e := publicationTableExistsTx(ctx, tx, "pretranscode_rendition_job"); e == nil && jobsExist {
		statements = append(statements, struct {
			query string
			args  []any
		}{`UPDATE pretranscode_rendition_job SET status='cancelled',error_message=CASE WHEN COALESCE(error_message,'')='' THEN ? ELSE error_message END,lease_owner=NULL,lease_until=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE task_id IN (SELECT id FROM transcode_task WHERE ingest_run_id=?) AND status IN ('waiting','running')`, []any{reason, runID}})
	}
	for _, stmt := range statements {
		if _, err = tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			return false, err
		}
	}
	if err = FinalizeNodeTransitionTx(ctx, tx, runID); err != nil {
		return false, err
	}
	for _, a := range attempts {
		invokePostIngestTempRelease(a.mediaID, a.generation, a.taskID)
	}
	return true, nil
}

// CancelRunForRequiredStepTx validates a specific required linked task and step
// before recording whole-run cancellation intent. No run or sibling work is
// changed when the linkage is stale or either target is already terminal.
func CancelRunForRequiredStepTx(ctx context.Context, tx store.SQLExecutor, runID, stepID, taskID int64, reason string) (bool, error) {
	if tx == nil || runID <= 0 || stepID <= 0 || taskID <= 0 || reason == "" {
		return false, fmt.Errorf("publication cancel target: invalid transaction, run, step, task, or reason")
	}
	var valid int
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(
		SELECT 1 FROM media_ingest_run r
		JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation
		JOIN media_ingest_step s ON s.id=? AND s.run_id=r.id AND s.media_id=r.media_id AND s.generation=r.generation
		JOIN transcode_task t ON t.id=? AND t.ingest_run_id=r.id AND t.ingest_step_id=s.id AND t.media_id=r.media_id AND t.generation=r.generation
		WHERE r.id=? AND r.status='processing' AND r.superseded_by_generation IS NULL AND r.superseded_at IS NULL
		AND s.step_type='prepare' AND s.required=1 AND s.status IN ('waiting','running')
		AND t.task_type='pretranscode' AND t.status IN ('waiting','running','paused')) THEN 1 ELSE 0 END`, stepID, taskID, runID).Scan(&valid); err != nil {
		return false, err
	}
	if valid != 1 {
		return false, nil
	}
	return CancelRunTx(ctx, tx, runID, reason)
}
