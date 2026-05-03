package subtitle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnsurePendingSubtitleTask inserts a pending row when none exists for media_id (INSERT OR IGNORE).
func (s *Service) EnsurePendingSubtitleTask(mediaID int64) error {
	_, err := s.DB.Exec(`
		INSERT OR IGNORE INTO subtitle_task (media_id, status, message, created_at, started_at, finished_at, updated_at)
		VALUES (?, 'pending', NULL, CURRENT_TIMESTAMP, NULL, NULL, CURRENT_TIMESTAMP)
	`, mediaID)
	return err
}

func (s *Service) upsertTaskRunning(mediaID int64) {
	_, _ = s.DB.Exec(`
		INSERT INTO subtitle_task (media_id, status, started_at, updated_at)
		VALUES (?, 'running', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
			status = 'running',
			message = NULL,
			started_at = CURRENT_TIMESTAMP,
			finished_at = NULL,
			updated_at = CURRENT_TIMESTAMP
	`, mediaID)
}

func (s *Service) upsertTaskDone(mediaID int64) {
	_, _ = s.DB.Exec(`
		UPDATE subtitle_task SET status = 'done', finished_at = CURRENT_TIMESTAMP, message = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE media_id = ?
	`, mediaID)
}

func (s *Service) upsertTaskFailed(mediaID int64, msg string) {
	msg = strings.TrimSpace(msg)
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	_, _ = s.DB.Exec(`
		UPDATE subtitle_task SET status = 'failed', finished_at = CURRENT_TIMESTAMP, message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE media_id = ?
	`, msg, mediaID)
}

// ResetSubtitleJob removes generated subtitle rows and files, then marks task as pending.
func (s *Service) ResetSubtitleJob(mediaID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.DB.Exec(`DELETE FROM media_subtitle WHERE media_id = ?`, mediaID); err != nil {
		return err
	}
	dir := filepath.Join(s.SubtitleDir, strconv.FormatInt(mediaID, 10))
	_ = os.RemoveAll(dir)
	_, err := s.DB.Exec(`
		INSERT INTO subtitle_task (media_id, status, message, created_at, started_at, finished_at, updated_at)
		VALUES (?, 'pending', NULL, CURRENT_TIMESTAMP, NULL, NULL, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
			status = 'pending',
			message = NULL,
			started_at = NULL,
			finished_at = NULL,
			updated_at = CURRENT_TIMESTAMP
	`, mediaID)
	return err
}

// CleanupSubtitleTasksFailed removes failed task rows (optional: keep media_subtitle).
func (s *Service) CleanupSubtitleTasksFailed() (int64, error) {
	res, err := s.DB.Exec(`DELETE FROM subtitle_task WHERE status = 'failed'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CleanupSubtitleTasksBefore deletes done/failed tasks whose finished_at is older than days.
func (s *Service) CleanupSubtitleTasksBefore(days int) (int64, error) {
	if days <= 0 {
		days = 30
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
	return res.RowsAffected()
}
