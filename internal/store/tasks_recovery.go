package store

import (
	"context"
	"database/sql"
	"log"
)

const (
	restartResetMessage           = "服务重启，任务已复位"
	scrapeRetriesExhaustedMessage = "服务重启，任务已耗尽重试次数"
	maxScrapeTaskFailures         = 3
)

// ResetInterruptedTasks marks in-flight tasks as recoverable after process restart.
func ResetInterruptedTasks(db *sql.DB) {
	if db == nil {
		return
	}
	reset := func(label, query string, args ...any) {
		res, err := db.Exec(query, args...)
		if err != nil {
			log.Printf("reset interrupted %s: %v", label, err)
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("reset interrupted %s: %d task(s)", label, n)
		}
	}

	if err := resetInterruptedScrapeTasks(context.Background(), db); err != nil {
		log.Printf("reset interrupted scrape_task: %v", err)
	}

	reset("transcode_task", `
		UPDATE transcode_task
		SET status = 'waiting', progress = 0, error_message = ?
		WHERE status = 'running'`, restartResetMessage)

	reset("pretranscode_rendition_job", `
		UPDATE pretranscode_rendition_job
		SET status = 'waiting', progress = 0, error_message = ?, available_at=CURRENT_TIMESTAMP, lease_owner=NULL, lease_until=NULL
		WHERE status = 'running'`, restartResetMessage)

	reset("linked prepare step", `
		UPDATE media_ingest_step
		SET status='waiting', lease_owner=NULL, lease_until=NULL, finished_at=NULL,
			last_error='', updated_at=CURRENT_TIMESTAMP
		WHERE step_type='prepare' AND status='running'
		  AND id IN (SELECT ingest_step_id FROM transcode_task
		             WHERE task_type='pretranscode' AND status='waiting'
		               AND ingest_step_id IS NOT NULL)`)

	reset("package_task", `
		UPDATE package_task
		SET status = 'waiting', progress = 0, drm_status = '', error_message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'`, restartResetMessage)

	reset("preview_task", `
		UPDATE preview_task
		SET status = 'waiting', error_message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'`, restartResetMessage)

	reset("subtitle_task", `
		UPDATE subtitle_task
		SET status = 'pending', message = ?, started_at = NULL, finished_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'`, restartResetMessage)

	reset("lyric_task", `
		UPDATE lyric_task
		SET status = 'pending', message = ?, started_at = NULL, finished_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'`, restartResetMessage)

	reset("atrack_task", `
		UPDATE atrack_task
		SET status = 'waiting', error_message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'`, restartResetMessage)

	reset("keyframe_task", `
		UPDATE keyframe_task
		SET status = 'waiting', error_message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'`, restartResetMessage)
}

func resetInterruptedScrapeTasks(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE scrape_task SET status='failed', progress=100, finished_at=CURRENT_TIMESTAMP, message=?, lease_owner=NULL, lease_until=NULL WHERE status IN ('running','waiting') AND COALESCE(fail_count,0)>=?`, scrapeRetriesExhaustedMessage, maxScrapeTaskFailures); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scrape_task SET status='waiting', progress=0, message=?, lease_owner=NULL, lease_until=NULL, available_at=COALESCE(available_at,CURRENT_TIMESTAMP) WHERE status='running' AND COALESCE(fail_count,0)<?`, restartResetMessage, maxScrapeTaskFailures); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='failed', lease_owner=NULL, lease_until=NULL, last_error=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id IN (SELECT ingest_step_id FROM scrape_task WHERE status='failed' AND COALESCE(fail_count,0)>=? AND ingest_step_id IS NOT NULL) AND status IN ('running','waiting')`, scrapeRetriesExhaustedMessage, maxScrapeTaskFailures); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='waiting', lease_owner=NULL, lease_until=NULL, available_at=(SELECT t.available_at FROM scrape_task t WHERE t.ingest_step_id=media_ingest_step.id AND t.status='waiting'), attempts=(SELECT COALESCE(t.fail_count,0) FROM scrape_task t WHERE t.ingest_step_id=media_ingest_step.id AND t.status='waiting'), last_error='', finished_at=NULL, updated_at=CURRENT_TIMESTAMP WHERE id IN (SELECT ingest_step_id FROM scrape_task WHERE status='waiting' AND ingest_step_id IS NOT NULL) AND status='running'`); err != nil {
		return err
	}
	return tx.Commit()
}
