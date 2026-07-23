package publication

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AggregateTx reconciles a run and, when it is current, its media visibility.
func AggregateTx(ctx context.Context, tx *sql.Tx, runID int64) error {
	if tx == nil || runID <= 0 {
		return fmt.Errorf("publication aggregate: invalid transaction or run")
	}
	var mediaID, generation int64
	var preserve int
	var runState, terminalReason, runError string
	var supersededBy sql.NullInt64
	var supersededAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT media_id,generation,status,preserve_visibility,terminal_reason,error_message,superseded_by_generation,superseded_at FROM media_ingest_run WHERE id=?`, runID).Scan(&mediaID, &generation, &runState, &preserve, &terminalReason, &runError, &supersededBy, &supersededAt); err != nil {
		return err
	}
	if supersededBy.Valid || supersededAt.Valid {
		return nil
	}
	var pending, failed, cancelled int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(status IN ('waiting','running')),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(status='cancelled'),0) FROM media_ingest_step WHERE run_id=? AND required=1`, runID).Scan(&pending, &failed, &cancelled); err != nil {
		return err
	}
	next := runState
	switch {
	case runState == "cancelled":
		next = "cancelled"
	case runState == "failed":
		next = "failed"
	case pending > 0:
		next = "processing"
	case failed > 0 || cancelled > 0:
		if preserve == 1 {
			next = "degraded"
		} else {
			next = "failed"
		}
	default:
		next = "published"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_ingest_run SET status=?,error_message=CASE WHEN ?='cancelled' THEN error_message WHEN ? IN ('degraded','failed') THEN COALESCE(NULLIF((SELECT last_error FROM media_ingest_step WHERE run_id=? AND required=1 AND status IN ('failed','cancelled') ORDER BY CASE status WHEN 'failed' THEN 0 ELSE 1 END,id LIMIT 1),''),'required step exhausted') ELSE '' END,finished_at=CASE WHEN ? IN ('published','degraded','failed','cancelled') THEN COALESCE(finished_at,CURRENT_TIMESTAMP) ELSE NULL END WHERE id=?`, next, next, next, runID, next, runID); err != nil {
		return err
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&current); err != nil {
		return err
	}
	if current != generation {
		return nil
	}
	if next == "processing" && preserve == 1 {
		return nil
	}
	if next == "published" {
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='published',published_at=COALESCE(published_at,CURRENT_TIMESTAMP),publication_error='' WHERE id=?`, mediaID)
		return err
	}
	if next == "degraded" {
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='degraded',publication_error=COALESCE(NULLIF((SELECT last_error FROM media_ingest_step WHERE run_id=? AND required=1 AND status IN ('failed','cancelled') ORDER BY CASE status WHEN 'failed' THEN 0 ELSE 1 END,id LIMIT 1),''),'required step exhausted') WHERE id=?`, runID, mediaID)
		return err
	}
	if next == "cancelled" {
		if preserve == 1 {
			_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='degraded',publication_error=COALESCE(NULLIF(?,''),'cancelled') WHERE id=?`, terminalReason, mediaID)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='cancelled',published_at=NULL,publication_error=COALESCE(NULLIF(?,''),'cancelled') WHERE id=?`, terminalReason, mediaID)
		return err
	}
	if next == "failed" {
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='failed',published_at=NULL,publication_error=COALESCE(NULLIF((SELECT last_error FROM media_ingest_step WHERE run_id=? AND required=1 AND status IN ('failed','cancelled') ORDER BY CASE status WHEN 'failed' THEN 0 ELSE 1 END,id LIMIT 1),''),'required step exhausted') WHERE id=?`, runID, mediaID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='processing',publication_error='' WHERE id=?`, mediaID)
	return err
}

// degradedRetryDelay is intentionally short but non-zero so background workers cannot claim a newly opened retry round immediately.
const degradedRetryDelay = 5 * time.Minute

// RetryDegradedRuns resets a bounded batch of exhausted required steps and requeues linked tasks.
func RetryDegradedRuns(ctx context.Context, db *sql.DB, limit int) (int, error) {
	if db == nil || limit <= 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM media_ingest_run WHERE status='degraded' AND (EXISTS (SELECT 1 FROM post_ingest_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE q.ingest_run_id=media_ingest_run.id AND q.ingest_step_id IS NOT NULL AND s.required=1 AND s.status='failed' AND s.attempts>=s.max_attempts) OR EXISTS (SELECT 1 FROM scrape_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE q.ingest_run_id=media_ingest_run.id AND q.ingest_step_id IS NOT NULL AND s.required=1 AND s.status='failed' AND s.attempts>=s.max_attempts) OR EXISTS (SELECT 1 FROM transcode_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE q.ingest_run_id=media_ingest_run.id AND q.ingest_step_id IS NOT NULL AND q.task_type='pretranscode' AND q.status='failed' AND s.step_type='prepare' AND s.required=1 AND s.status='failed' AND s.attempts>=s.max_attempts)) ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		availableAt := time.Now().UTC().Add(degradedRetryDelay).Format("2006-01-02 15:04:05")
		if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=?,updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND step_type<>'scrape' AND required=1 AND status='failed' AND attempts>=max_attempts AND EXISTS (SELECT 1 FROM post_ingest_task q WHERE q.ingest_run_id=? AND q.ingest_step_id=media_ingest_step.id AND q.status='failed')`, availableAt, id, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=?,updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND required=1 AND status='failed' AND attempts>=max_attempts AND EXISTS (SELECT 1 FROM scrape_task q WHERE q.ingest_run_id=? AND q.ingest_step_id=media_ingest_step.id AND q.status='failed')`, availableAt, id, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=?,updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND step_type='prepare' AND required=1 AND status='failed' AND attempts>=max_attempts AND EXISTS (SELECT 1 FROM transcode_task q WHERE q.ingest_run_id=? AND q.ingest_step_id=media_ingest_step.id AND q.task_type='pretranscode' AND q.status='failed')`, availableAt, id, id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=?,updated_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND status='failed' AND ingest_step_id IS NOT NULL AND EXISTS (SELECT 1 FROM media_ingest_step s WHERE s.id=post_ingest_task.ingest_step_id AND s.run_id=? AND s.required=1 AND s.status='waiting' AND s.attempts=0 AND s.available_at=?)`, availableAt, id, id, availableAt); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE scrape_task SET status='waiting',fail_count=0,message='',lease_owner=NULL,lease_until=NULL,finished_at=NULL,available_at=? WHERE ingest_run_id=? AND status='failed' AND ingest_step_id IS NOT NULL AND EXISTS (SELECT 1 FROM media_ingest_step s WHERE s.id=scrape_task.ingest_step_id AND s.run_id=? AND s.required=1 AND s.status='waiting' AND s.attempts=0 AND s.available_at=?)`, availableAt, id, id, availableAt); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE transcode_task SET status='waiting',progress=0,error_message='',started_at=NULL,completed_at=NULL WHERE ingest_run_id=? AND task_type='pretranscode' AND status='failed' AND ingest_step_id IS NOT NULL AND EXISTS (SELECT 1 FROM media_ingest_step s WHERE s.id=transcode_task.ingest_step_id AND s.run_id=? AND s.step_type='prepare' AND s.required=1 AND s.status='waiting' AND s.attempts=0 AND s.available_at=?)`, id, id, availableAt); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE pretranscode_rendition_job SET status='waiting',progress=0,error_message='',available_at=?,lease_owner=NULL,lease_until=NULL,started_at=NULL,completed_at=NULL WHERE task_id IN (SELECT t.id FROM transcode_task t JOIN media_ingest_step s ON s.id=t.ingest_step_id WHERE t.ingest_run_id=? AND t.task_type='pretranscode' AND t.status='waiting' AND s.run_id=? AND s.step_type='prepare' AND s.required=1 AND s.status='waiting' AND s.available_at=?) AND status='failed'`, availableAt, id, id, availableAt); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_run SET status='degraded',preserve_visibility=1,error_message='' WHERE id=?`, id); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}
