package publication

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrIngestNotFound = errors.New("publication ingest not found")
var ErrNoRetryableWork = errors.New("publication ingest has no retryable work")

// RetryIngest retries exactly one current media generation. Failed or cancelled
// whole-ingest runs get a new immutable generation planned from current policy.
// Degraded retries continue to requeue failed required work in place.
func RetryIngest(ctx context.Context, db *sql.DB, mediaID int64, planner *Planner) error {
	if db == nil || mediaID <= 0 {
		return ErrIngestNotFound
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var generation int64
	var mediaState string
	if err = tx.QueryRowContext(ctx, `SELECT ingest_generation,publication_state FROM media WHERE id=?`, mediaID).Scan(&generation, &mediaState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIngestNotFound
		}
		return err
	}
	var runID int64
	var runState string
	err = tx.QueryRowContext(ctx, `SELECT id,status FROM media_ingest_run WHERE media_id=? AND generation=?`, mediaID, generation).Scan(&runID, &runState)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrIngestNotFound
	}
	if err != nil {
		return err
	}

	available := time.Now().UTC().Add(5 * time.Minute).Format("2006-01-02 15:04:05")
	if mediaState == "degraded" && runState == "degraded" {
		changed, retryErr := retryDegradedRunTx(ctx, tx, runID, generation, available)
		if retryErr != nil {
			return retryErr
		}
		if changed == 0 {
			return ErrNoRetryableWork
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_run SET reason='manual_retry',preserve_visibility=1,error_message='',updated_at=CURRENT_TIMESTAMP WHERE id=? AND generation=?`, runID, generation); err != nil {
			return err
		}
		return tx.Commit()
	}

	if (mediaState != "failed" && mediaState != "cancelled") || (runState != "failed" && runState != "cancelled") {
		return ErrNoRetryableWork
	}
	if planner == nil {
		return errors.New("publication retry: nil planner")
	}
	_, err = planner.PlanReplacementTx(ctx, tx, mediaID, ReplacementOptions{
		Reason: PlanReasonManualRetry, PreserveVisibility: false, ExpectedGeneration: generation,
	})
	if errors.Is(err, ErrGenerationConflict) {
		return ErrNoRetryableWork
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func retryDegradedRunTx(ctx context.Context, tx *sql.Tx, runID, generation int64, available string) (int64, error) {
	var changed int64
	res, err := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=?,updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND generation=? AND required=1 AND status IN ('failed','cancelled') AND ((step_type='scrape' AND EXISTS(SELECT 1 FROM scrape_task q WHERE q.ingest_step_id=media_ingest_step.id AND q.ingest_run_id=? AND q.generation=? AND q.status IN ('failed','cancelled','abandoned'))) OR (step_type='prepare' AND EXISTS(SELECT 1 FROM transcode_task q WHERE q.ingest_step_id=media_ingest_step.id AND q.ingest_run_id=? AND q.generation=? AND q.status IN ('failed','cancelled'))) OR (step_type NOT IN ('scrape','prepare') AND EXISTS(SELECT 1 FROM post_ingest_task q WHERE q.ingest_step_id=media_ingest_step.id AND q.ingest_run_id=? AND q.generation=? AND q.status IN ('failed','cancelled'))))`, available, runID, generation, runID, generation, runID, generation, runID, generation)
	if err != nil {
		return 0, err
	}
	changed, _ = res.RowsAffected()
	if changed == 0 {
		return 0, nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=?,updated_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND generation=? AND status IN ('failed','cancelled') AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.id=post_ingest_task.ingest_step_id AND s.required=1 AND s.status='waiting')`, available, runID, generation); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scrape_task SET status='waiting',fail_count=0,message='',progress=0,lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=? WHERE ingest_run_id=? AND generation=? AND status IN ('failed','cancelled','abandoned') AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.id=scrape_task.ingest_step_id AND s.required=1 AND s.status='waiting')`, available, runID, generation); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE transcode_task SET status='waiting',progress=0,error_message='',started_at=NULL,completed_at=NULL WHERE ingest_run_id=? AND generation=? AND status IN ('failed','cancelled') AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.id=transcode_task.ingest_step_id AND s.required=1 AND s.status='waiting')`, runID, generation); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE pretranscode_rendition_job SET status='waiting',progress=0,error_message='',available_at=?,lease_owner=NULL,lease_until=NULL,started_at=NULL,completed_at=NULL WHERE task_id IN(SELECT id FROM transcode_task WHERE ingest_run_id=? AND generation=? AND status='waiting') AND status IN ('failed','cancelled')`, available, runID, generation); err != nil {
		return 0, err
	}
	return changed, nil
}
