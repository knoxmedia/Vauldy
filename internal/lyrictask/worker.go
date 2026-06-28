package lyrictask

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"knox-media/internal/musiclyrics"
	"knox-media/internal/musicparse"
	"knox-media/internal/storage"
	"knox-media/internal/subtitle"
)

// Worker runs ASR-based lyric recognition for audio media (VTT → sidecar LRC).
type Worker struct {
	DB          *sql.DB
	Derived     *storage.DerivedAssetStore
	WorkDir     string
	FFprobePath string
	Subtitle    *subtitle.Service
	mu          sync.Mutex
	running     map[int64]bool
}

func NewWorker(db *sql.DB, derived *storage.DerivedAssetStore, workDir, ffprobePath string, sub *subtitle.Service) *Worker {
	return &Worker{
		DB:          db,
		Derived:     derived,
		WorkDir:     workDir,
		FFprobePath: ffprobePath,
		Subtitle:    sub,
		running:     map[int64]bool{},
	}
}

// Enqueue upserts a pending lyric_task; existing failed rows are left unchanged.
func (w *Worker) Enqueue(mediaID int64) error {
	_, err := w.DB.Exec(`
		INSERT INTO lyric_task (media_id, status, created_at, updated_at)
		VALUES (?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
			status = CASE WHEN lyric_task.status = 'failed' THEN lyric_task.status ELSE 'pending' END,
			message = CASE WHEN lyric_task.status = 'failed' THEN lyric_task.message ELSE NULL END,
			vtt_path = CASE WHEN lyric_task.status = 'failed' THEN lyric_task.vtt_path ELSE NULL END,
			lrc_path = CASE WHEN lyric_task.status = 'failed' THEN lyric_task.lrc_path ELSE NULL END,
			started_at = CASE WHEN lyric_task.status = 'failed' THEN lyric_task.started_at ELSE NULL END,
			finished_at = CASE WHEN lyric_task.status = 'failed' THEN lyric_task.finished_at ELSE NULL END,
			updated_at = CURRENT_TIMESTAMP
	`, mediaID)
	return err
}

// EnqueueRetry resets a lyric task to pending for manual retry.
func (w *Worker) EnqueueRetry(mediaID int64) error {
	_, err := w.DB.Exec(`
		INSERT INTO lyric_task (media_id, status, created_at, updated_at)
		VALUES (?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
			status = 'pending',
			message = NULL,
			vtt_path = NULL,
			lrc_path = NULL,
			started_at = NULL,
			finished_at = NULL,
			updated_at = CURRENT_TIMESTAMP
	`, mediaID)
	return err
}

// EnsurePendingLyricTask inserts a pending row when none exists for media_id (INSERT OR IGNORE).
func (w *Worker) EnsurePendingLyricTask(mediaID int64) error {
	_, err := w.DB.Exec(`
		INSERT OR IGNORE INTO lyric_task (media_id, status, created_at, updated_at)
		VALUES (?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, mediaID)
	return err
}

// EnsurePendingIfNoLyrics enqueues a lyric task when media is audio in a music library and has no lyrics yet.
func (w *Worker) EnsurePendingIfNoLyrics(mediaID int64, fileType string) error {
	if w == nil || w.DB == nil || mediaID <= 0 || strings.TrimSpace(fileType) != "audio" {
		return nil
	}
	var libraryType, filePath, metaJSON sql.NullString
	if err := w.DB.QueryRow(`
		SELECT l.type, m.file_path, COALESCE(m.meta_json,'')
		FROM media m
		JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&libraryType, &filePath, &metaJSON); err != nil {
		return err
	}
	if !musicparse.IsMusicLibraryType(libraryType.String) {
		return nil
	}
	audioPath := strings.TrimSpace(filePath.String)
	if audioPath == "" {
		return nil
	}
	if content, _, ok := musiclyrics.Load(audioPath, metaJSON.String, w.FFprobePath); ok && strings.TrimSpace(content) != "" {
		return nil
	}
	return w.EnsurePendingLyricTask(mediaID)
}

// RunBatch processes up to limit pending lyric tasks.
func (w *Worker) RunBatch(ctx context.Context, limit int) (done, failed int) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := w.DB.Query(`
		SELECT media_id FROM lyric_task
		WHERE status = 'pending'
		ORDER BY updated_at ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return done, failed
		default:
		}
		if err := w.Process(ctx, id); err != nil {
			failed++
		} else {
			done++
		}
	}
	return done, failed
}

// Process runs lyric recognition for one media item.
func (w *Worker) Process(ctx context.Context, mediaID int64) (err error) {
	w.mu.Lock()
	if w.running[mediaID] {
		w.mu.Unlock()
		return fmt.Errorf("already running for media %d", mediaID)
	}
	w.running[mediaID] = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.running, mediaID)
		w.mu.Unlock()
	}()

	var taskStatus string
	if qerr := w.DB.QueryRow(`SELECT status FROM lyric_task WHERE media_id = ?`, mediaID).Scan(&taskStatus); qerr == nil && taskStatus == "failed" {
		return nil
	}

	var fileType, filePath, metaJSON sql.NullString
	if err = w.DB.QueryRow(`
		SELECT file_type, file_path, COALESCE(meta_json,'')
		FROM media WHERE id = ? LIMIT 1
	`, mediaID).Scan(&fileType, &filePath, &metaJSON); err != nil {
		return err
	}
	if strings.TrimSpace(fileType.String) != "audio" {
		w.markFailed(mediaID, "不是音频文件")
		return fmt.Errorf("not audio")
	}
	audioPath := strings.TrimSpace(filePath.String)
	if audioPath == "" {
		w.markFailed(mediaID, "文件路径为空")
		return fmt.Errorf("empty path")
	}
	if fi, statErr := os.Stat(audioPath); statErr != nil || fi.IsDir() {
		w.markFailed(mediaID, "音频文件不存在")
		return fmt.Errorf("file missing")
	}

	if content, _, ok := musiclyrics.Load(audioPath, metaJSON.String, w.FFprobePath); ok && strings.TrimSpace(content) != "" {
		lrcPath := sidecarLRCPath(audioPath)
		w.markDone(mediaID, "", lrcPath, "已有歌词，跳过 ASR")
		return nil
	}

	w.markRunning(mediaID)

	if w.Subtitle == nil {
		w.markFailed(mediaID, "字幕/ASR 服务未启用")
		return fmt.Errorf("subtitle service nil")
	}

	outDir := filepath.Join(w.WorkDir, strconv.FormatInt(mediaID, 10))
	_ = os.RemoveAll(outDir)
	if err = os.MkdirAll(outDir, 0o755); err != nil {
		w.markFailed(mediaID, err.Error())
		return err
	}
	vttPath := filepath.Join(outDir, "asr.vtt")

	if err = w.Subtitle.TranscribeToVTT(ctx, mediaID, audioPath, vttPath); err != nil {
		w.markFailed(mediaID, err.Error())
		return err
	}

	lrcPath := filepath.Join(outDir, "asr.lrc")
	if err = musiclyrics.ConvertVTTFile(vttPath, lrcPath); err != nil {
		w.markFailed(mediaID, err.Error())
		return err
	}

	// AI-proofread the final LRC lyric text (preserves [mm:ss.xx] timestamps and metadata).
	if w.Subtitle.AIProofreadEnabled() {
		if perr := w.Subtitle.ProofreadFileInPlace(ctx, lrcPath, "und"); perr != nil {
			log.Printf("lyric ai-proofread media=%d err=%v", mediaID, perr)
		}
	}

	finalVTT, finalLRC := vttPath, lrcPath
	if w.Derived != nil {
		var encErr error
		finalVTT, encErr = w.Derived.FinalizePath(ctx, mediaID, "lyric_vtt", "asr.vtt", vttPath)
		if encErr != nil {
			w.markFailed(mediaID, encErr.Error())
			return encErr
		}
		finalLRC, encErr = w.Derived.FinalizePath(ctx, mediaID, "lyric_lrc", "asr.lrc", lrcPath)
		if encErr != nil {
			w.markFailed(mediaID, encErr.Error())
			return encErr
		}
	}

	w.markDone(mediaID, finalVTT, finalLRC, "")
	return nil
}

func sidecarLRCPath(audioPath string) string {
	dir := filepath.Dir(audioPath)
	base := strings.TrimSuffix(filepath.Base(audioPath), filepath.Ext(audioPath))
	return filepath.Join(dir, base+".lrc")
}

func (w *Worker) markRunning(mediaID int64) {
	_, _ = w.DB.Exec(`
		UPDATE lyric_task SET status = 'running', started_at = CURRENT_TIMESTAMP, message = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE media_id = ?
	`, mediaID)
}

func (w *Worker) markDone(mediaID int64, vttPath, lrcPath, message string) {
	_, _ = w.DB.Exec(`
		UPDATE lyric_task SET status = 'done', finished_at = CURRENT_TIMESTAMP,
			vtt_path = ?, lrc_path = ?, message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE media_id = ?
	`, nullIfEmpty(vttPath), nullIfEmpty(lrcPath), nullIfEmpty(message), mediaID)
}

func (w *Worker) markFailed(mediaID int64, msg string) {
	msg = strings.TrimSpace(msg)
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	_, _ = w.DB.Exec(`
		UPDATE lyric_task SET status = 'failed', finished_at = CURRENT_TIMESTAMP, message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE media_id = ?
	`, msg, mediaID)
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// CleanupFailed removes failed lyric task rows.
func (w *Worker) CleanupFailed() (int64, error) {
	res, err := w.DB.Exec(`DELETE FROM lyric_task WHERE status = 'failed'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CleanupBefore deletes done/failed tasks older than days.
func (w *Worker) CleanupBefore(days int) (int64, error) {
	if days <= 0 {
		days = 30
	}
	res, err := w.DB.Exec(`
		DELETE FROM lyric_task
		WHERE status IN ('done', 'failed')
		  AND finished_at IS NOT NULL
		  AND datetime(finished_at) < datetime('now', '-' || ? || ' days')
	`, days)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
