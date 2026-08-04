package store

import (
	"context"
	"database/sql"
	"log"
)

const (
	restartResetMessage = "服务重启，任务已复位"
)

// ResetInterruptedScrapeFn is optionally wired by publication so scrape restart
// resets finalize plan projection in the same durable transaction. When unset,
// scrape rows are left untouched here (callers must invoke publication reset).
var ResetInterruptedScrapeFn func(ctx context.Context, db *sql.DB) error

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

	if ResetInterruptedScrapeFn != nil {
		if err := ResetInterruptedScrapeFn(context.Background(), db); err != nil {
			log.Printf("reset interrupted scrape_task: %v", err)
		}
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
		SET status = 'pending', message = ?,
		    extract_status = CASE WHEN extract_status='running' THEN 'pending' ELSE extract_status END,
		    recognize_status = CASE WHEN recognize_status='running' THEN 'pending' ELSE recognize_status END,
		    started_at = NULL, finished_at = NULL, updated_at = CURRENT_TIMESTAMP
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
