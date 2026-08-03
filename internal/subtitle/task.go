package subtitle

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"knox-media/internal/publication"
	"knox-media/internal/taskalign"
)

// EnsurePendingSubtitleTask inserts a pending row when none exists for media_id (INSERT OR IGNORE).
func (s *Service) EnsurePendingSubtitleTask(mediaID int64) error {
	_, err := s.DB.Exec(`
		INSERT OR IGNORE INTO subtitle_task (media_id, status, message, extract_status, recognize_status, created_at, started_at, finished_at, updated_at)
		VALUES (?, 'pending', NULL, 'pending', 'pending', CURRENT_TIMESTAMP, NULL, NULL, CURRENT_TIMESTAMP)
	`, mediaID)
	return err
}

func (s *Service) setTaskDoneGuarded(ctx context.Context, mediaID int64, stage subtitleStage) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateCommitGuardTx(ctx, tx); err != nil {
		return err
	}
	query, args := stageDoneSQL(mediaID, stage)
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("subtitle done update affected %d rows", n)
	}
	return tx.Commit()
}

func stageDoneSQL(mediaID int64, stage subtitleStage) (string, []any) {
	switch stage {
	case subtitleStageExtract:
		return `UPDATE subtitle_task SET
			extract_status='done',extract_message=NULL,
			status=CASE WHEN recognize_status='failed' THEN 'failed' WHEN recognize_status='done' THEN 'done' ELSE 'done' END,
			message=CASE WHEN recognize_status='failed' THEN recognize_message ELSE NULL END,
			finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, []any{mediaID}
	case subtitleStageRecognize:
		return `UPDATE subtitle_task SET
			recognize_status='done',recognize_message=NULL,
			status=CASE WHEN extract_status='failed' THEN 'failed' ELSE 'done' END,
			message=CASE WHEN extract_status='failed' THEN extract_message ELSE NULL END,
			finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, []any{mediaID}
	default:
		return `UPDATE subtitle_task SET
			status='done',message=NULL,
			extract_status='done',recognize_status='done',
			extract_message=NULL,recognize_message=NULL,
			finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, []any{mediaID}
	}
}

func (s *Service) upsertTaskRunning(ctx context.Context, mediaID int64, stage subtitleStage) error {
	switch stage {
	case subtitleStageExtract:
		_, err := s.DB.ExecContext(ctx, `
			INSERT INTO subtitle_task (media_id, status, extract_status, recognize_status, started_at, updated_at)
			VALUES (?, 'running', 'running', 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			ON CONFLICT(media_id) DO UPDATE SET
				status='running',message=NULL,
				extract_status='running',extract_message=NULL,
				started_at=CURRENT_TIMESTAMP,finished_at=NULL,updated_at=CURRENT_TIMESTAMP`, mediaID)
		return err
	case subtitleStageRecognize:
		_, err := s.DB.ExecContext(ctx, `
			INSERT INTO subtitle_task (media_id, status, extract_status, recognize_status, started_at, updated_at)
			VALUES (?, 'running', 'pending', 'running', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			ON CONFLICT(media_id) DO UPDATE SET
				status='running',message=NULL,
				recognize_status='running',recognize_message=NULL,
				started_at=CURRENT_TIMESTAMP,finished_at=NULL,updated_at=CURRENT_TIMESTAMP`, mediaID)
		return err
	default:
		_, err := s.DB.ExecContext(ctx, `
			INSERT INTO subtitle_task (media_id, status, extract_status, recognize_status, started_at, updated_at)
			VALUES (?, 'running', 'running', 'running', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			ON CONFLICT(media_id) DO UPDATE SET
				status='running',message=NULL,
				extract_status='running',recognize_status='running',
				extract_message=NULL,recognize_message=NULL,
				started_at=CURRENT_TIMESTAMP,finished_at=NULL,updated_at=CURRENT_TIMESTAMP`, mediaID)
		return err
	}
}

func (s *Service) upsertTaskFailed(ctx context.Context, mediaID int64, stage subtitleStage, msg string) error {
	msg = strings.TrimSpace(msg)
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	switch stage {
	case subtitleStageExtract:
		_, err := s.DB.ExecContext(ctx, `UPDATE subtitle_task SET
			extract_status='failed',extract_message=?,
			status='failed',message=?,
			finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, msg, msg, mediaID)
		return err
	case subtitleStageRecognize:
		_, err := s.DB.ExecContext(ctx, `UPDATE subtitle_task SET
			recognize_status='failed',recognize_message=?,
			status='failed',message=?,
			finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, msg, msg, mediaID)
		return err
	default:
		_, err := s.DB.ExecContext(ctx, `UPDATE subtitle_task SET
			status='failed',message=?,
			extract_status='failed',recognize_status='failed',
			extract_message=?,recognize_message=?,
			finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, msg, msg, msg, mediaID)
		return err
	}
}

func (s *Service) setTaskWaiting(ctx context.Context, mediaID int64, stage subtitleStage, msg string) error {
	switch stage {
	case subtitleStageExtract:
		_, err := s.DB.ExecContext(ctx, `UPDATE subtitle_task SET
			status='pending',extract_status='pending',extract_message=?,
			started_at=NULL,finished_at=NULL,message=?,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, msg, msg, mediaID)
		return err
	case subtitleStageRecognize:
		_, err := s.DB.ExecContext(ctx, `UPDATE subtitle_task SET
			status='pending',recognize_status='pending',recognize_message=?,
			started_at=NULL,finished_at=NULL,message=?,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, msg, msg, mediaID)
		return err
	default:
		_, err := s.DB.ExecContext(ctx, `UPDATE subtitle_task SET
			status='pending',extract_status='pending',recognize_status='pending',
			extract_message=NULL,recognize_message=NULL,
			started_at=NULL,finished_at=NULL,message=?,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, msg, mediaID)
		return err
	}
}

// ResetSubtitleJob removes generated subtitle rows and files, then marks task as pending.
func (s *Service) ResetSubtitleJob(mediaID int64) error {
	unlock := s.lockMedia(mediaID)
	defer unlock()
	if _, err := s.DB.Exec(`DELETE FROM media_subtitle WHERE media_id = ?`, mediaID); err != nil {
		return err
	}
	dir := filepath.Join(s.SubtitleDir, strconv.FormatInt(mediaID, 10))
	_ = os.RemoveAll(dir)
	_, err := s.DB.Exec(`
		INSERT INTO subtitle_task (media_id, status, message, extract_status, recognize_status, extract_message, recognize_message, created_at, started_at, finished_at, updated_at)
		VALUES (?, 'pending', NULL, 'pending', 'pending', NULL, NULL, CURRENT_TIMESTAMP, NULL, NULL, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
			status = 'pending',
			message = NULL,
			extract_status = 'pending',
			recognize_status = 'pending',
			extract_message = NULL,
			recognize_message = NULL,
			started_at = NULL,
			finished_at = NULL,
			updated_at = CURRENT_TIMESTAMP
	`, mediaID)
	return err
}

const subtitleDeletedByAdminMarker = "deleted by admin"

// DeleteSubtitleTask atomically retires current-generation subtitle extract/recognize
// queue executions and removes the domain task row. Queue rows remain cancelled so
// startup repair cannot recreate them.
func (s *Service) DeleteSubtitleTask(mediaID int64) error {
	if s == nil || s.DB == nil || mediaID <= 0 {
		return fmt.Errorf("invalid media id")
	}
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var domainStatus string
	domainExists := true
	if err = tx.QueryRowContext(ctx, `SELECT status FROM subtitle_task WHERE media_id=?`, mediaID).Scan(&domainStatus); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
		domainExists = false
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT q.id,q.status,q.ingest_step_id,q.ingest_run_id
		FROM post_ingest_task q JOIN media m ON m.id=q.media_id
		WHERE q.media_id=? AND q.task_type IN ('subtitle','subtitle_recognize')
		  AND q.generation=COALESCE(m.ingest_generation,0)
		ORDER BY q.id`, mediaID)
	if err != nil {
		return err
	}
	type queueRow struct {
		id             int64
		status         string
		stepID, runID  sql.NullInt64
	}
	var queues []queueRow
	for rows.Next() {
		var q queueRow
		if err = rows.Scan(&q.id, &q.status, &q.stepID, &q.runID); err != nil {
			_ = rows.Close()
			return err
		}
		queues = append(queues, q)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if !domainExists && len(queues) == 0 {
		return fmt.Errorf("task not found")
	}
	for _, q := range queues {
		if strings.EqualFold(strings.TrimSpace(q.status), "running") {
			return fmt.Errorf("task is running")
		}
	}
	hasTerminalCurrent := false
	for _, q := range queues {
		if isTerminalSubtitleQueueStatus(q.status) {
			hasTerminalCurrent = true
			break
		}
	}
	if domainExists && strings.EqualFold(strings.TrimSpace(domainStatus), "running") && !hasTerminalCurrent {
		return fmt.Errorf("task is running")
	}

	runIDs := map[int64]struct{}{}
	for _, q := range queues {
		res, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error=?,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status<>'running'`, subtitleDeletedByAdminMarker, q.id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return fmt.Errorf("subtitle queue delete raced")
		}
		if q.stepID.Valid {
			res, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error=?,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, subtitleDeletedByAdminMarker, q.stepID.Int64)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				if err != nil {
					return err
				}
				return fmt.Errorf("linked subtitle ingest step not found")
			}
		}
		if q.runID.Valid {
			runIDs[q.runID.Int64] = struct{}{}
		}
	}
	for runID := range runIDs {
		if err := publication.FinalizeNodeTransitionTx(ctx, tx, runID); err != nil {
			return err
		}
	}
	if domainExists {
		res, err := tx.ExecContext(ctx, `DELETE FROM subtitle_task WHERE media_id=?`, mediaID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return fmt.Errorf("subtitle task delete raced")
		}
	}
	return tx.Commit()
}

func isTerminalSubtitleQueueStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "cancelled", "done":
		return true
	default:
		return false
	}
}

// CleanupSubtitleTasksFailed removes failed task rows (optional: keep media_subtitle).
func (s *Service) CleanupSubtitleTasksFailed() (int64, error) {
	rows, err := s.DB.Query(`SELECT media_id FROM subtitle_task WHERE status = 'failed'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var mediaIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		mediaIDs = append(mediaIDs, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`DELETE FROM subtitle_task WHERE status = 'failed'`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := taskalign.DeleteCurrentGenQueueTasks(context.Background(), s.DB, "subtitle", mediaIDs...); err != nil {
		return n, err
	}
	if err := taskalign.DeleteCurrentGenQueueTasks(context.Background(), s.DB, "subtitle_recognize", mediaIDs...); err != nil {
		return n, err
	}
	return n, nil
}

// CleanupSubtitleTasksBefore deletes done/failed tasks whose finished_at is older than days.
func (s *Service) CleanupSubtitleTasksBefore(days int) (int64, error) {
	if days <= 0 {
		days = 30
	}
	rows, err := s.DB.Query(`
		SELECT media_id FROM subtitle_task
		WHERE status IN ('done', 'failed')
		  AND finished_at IS NOT NULL
		  AND datetime(finished_at) < datetime('now', '-' || ? || ' days')
	`, days)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var mediaIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		mediaIDs = append(mediaIDs, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`
		DELETE FROM subtitle_task
		WHERE status IN ('done', 'failed')
		  AND finished_at IS NOT NULL
		  AND datetime(finished_at) < datetime('now', '-' || ? || ' days')
	`, days)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := taskalign.DeleteCurrentGenQueueTasks(context.Background(), s.DB, "subtitle", mediaIDs...); err != nil {
		return n, err
	}
	if err := taskalign.DeleteCurrentGenQueueTasks(context.Background(), s.DB, "subtitle_recognize", mediaIDs...); err != nil {
		return n, err
	}
	return n, nil
}
