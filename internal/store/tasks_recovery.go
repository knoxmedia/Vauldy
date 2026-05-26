package store

import (
	"database/sql"
	"log"
)

const restartResetMessage = "服务重启，任务已复位"

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

	reset("scrape_task", `
		UPDATE scrape_task
		SET status = 'waiting', progress = 0, message = ?
		WHERE status = 'running'`, restartResetMessage)

	reset("scan_task", `
		UPDATE scan_task
		SET status = 'failed',
		    cancelled = 1,
		    error_message = CASE
		      WHEN COALESCE(error_message, '') = '' THEN 'scan interrupted (service restarted)'
		      ELSE error_message
		    END,
		    finished_at = CURRENT_TIMESTAMP,
		    updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'`)

	reset("transcode_task", `
		UPDATE transcode_task
		SET status = 'waiting', progress = 0, error_message = ?
		WHERE status = 'running'`, restartResetMessage)

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
