package keyframe

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"knox-media/internal/keystore"
	jitkeyframes "knox-media/internal/jit/keyframes"
	"knox-media/internal/storage"
)

// Info represents the current state of a keyframe extraction task.
type Info struct {
	Status        string
	OutputDir     string
	KeyframeCount int
	ErrorMessage  string
}

// Worker extracts keyframe PTS lists from video files using ffprobe show_packets,
// producing JSON cache files compatible with the JIT transcode scheduler.
type Worker struct {
	DB          *sql.DB
	Vault       *keystore.Vault
	Derived     *storage.DerivedAssetStore
	FFprobePath string
	OutputDir   string
	mu          sync.Mutex
	running     map[int64]bool
}

// NewWorker creates a new keyframe extraction worker.
func NewWorker(db *sql.DB, vault *keystore.Vault, derived *storage.DerivedAssetStore, ffprobePath, outputDir string) *Worker {
	return &Worker{
		DB:          db,
		Vault:       vault,
		Derived:     derived,
		FFprobePath: ffprobePath,
		OutputDir:   outputDir,
		running:     map[int64]bool{},
	}
}

// Enqueue upserts a waiting keyframe_task; existing failed rows are left unchanged.
func (w *Worker) Enqueue(mediaID int64) {
	_, _ = w.DB.Exec(
		`INSERT INTO keyframe_task (media_id, status, updated_at) VALUES (?, 'waiting', CURRENT_TIMESTAMP)
		 ON CONFLICT(media_id) DO UPDATE SET
		   status = CASE WHEN keyframe_task.status = 'failed' THEN keyframe_task.status ELSE 'waiting' END,
		   updated_at = CURRENT_TIMESTAMP,
		   error_message = CASE WHEN keyframe_task.status = 'failed' THEN keyframe_task.error_message ELSE NULL END,
		   keyframe_count = CASE WHEN keyframe_task.status = 'failed' THEN keyframe_task.keyframe_count ELSE 0 END`,
		mediaID,
	)
}

// EnqueueRetry resets a keyframe task to waiting for manual retry.
func (w *Worker) EnqueueRetry(mediaID int64) {
	_, _ = w.DB.Exec(
		`INSERT INTO keyframe_task (media_id, status, updated_at) VALUES (?, 'waiting', CURRENT_TIMESTAMP)
		 ON CONFLICT(media_id) DO UPDATE SET status='waiting', updated_at=CURRENT_TIMESTAMP, error_message=NULL, keyframe_count=0`,
		mediaID,
	)
}

// Info returns the current keyframe_task info for a media item.
func (w *Worker) Info(mediaID int64) Info {
	var info Info
	var status, outputDir, errMsg sql.NullString
	var count sql.NullInt64
	if err := w.DB.QueryRow(
		`SELECT status, output_dir, keyframe_count, error_message FROM keyframe_task WHERE media_id = ? LIMIT 1`,
		mediaID,
	).Scan(&status, &outputDir, &count, &errMsg); err != nil {
		return info
	}
	info.Status = status.String
	info.OutputDir = outputDir.String
	info.KeyframeCount = int(count.Int64)
	info.ErrorMessage = errMsg.String
	return info
}

// RunBatch processes up to `limit` waiting keyframe tasks.
func (w *Worker) RunBatch(limit int) (done, failed int) {
	rows, err := w.DB.Query(
		`SELECT t.media_id, m.file_id, m.file_path, COALESCE(m.duration,0)
		 FROM keyframe_task t
		 JOIN media m ON m.id = t.media_id
		 WHERE t.status = 'waiting'
		 ORDER BY t.id
		 LIMIT ?`, limit,
	)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()

	type job struct {
		mediaID  int64
		fileID   string
		filePath string
		duration float64
	}
	var jobs []job
	for rows.Next() {
		var j job
		var dur sql.NullInt64
		if rows.Scan(&j.mediaID, &j.fileID, &j.filePath, &dur) == nil {
			j.duration = float64(dur.Int64)
			jobs = append(jobs, j)
		}
	}
	if len(jobs) == 0 {
		return 0, 0
	}

	for _, j := range jobs {
		if err := w.run(context.Background(), j.mediaID, j.fileID, j.filePath, j.duration); err != nil {
			failed++
		} else {
			done++
		}
	}
	return done, failed
}

// Run executes keyframe extraction for a single media item.
func (w *Worker) Run(ctx context.Context, mediaID int64, fileID, filePath string, duration float64) error {
	return w.run(ctx, mediaID, fileID, filePath, duration)
}

func (w *Worker) run(ctx context.Context, mediaID int64, fileID, filePath string, duration float64) error {
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
	if qerr := w.DB.QueryRow(`SELECT status FROM keyframe_task WHERE media_id = ?`, mediaID).Scan(&taskStatus); qerr == nil && taskStatus == "failed" {
		return nil
	}

	// Determine file_id from media row if not provided.
	if strings.TrimSpace(fileID) == "" {
		var fid sql.NullString
		if err := w.DB.QueryRow(`SELECT file_id FROM media WHERE id = ? LIMIT 1`, mediaID).Scan(&fid); err == nil {
			fileID = fid.String
		}
	}

	_, _ = w.DB.Exec(
		`UPDATE keyframe_task SET status='running', updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`,
		mediaID,
	)

	// Use the JIT keyframes cache to extract PTS timestamps via ffprobe show_packets.
	cache, err := jitkeyframes.NewCache(w.OutputDir, w.FFprobePath)
	if err != nil {
		w.markFailed(mediaID, err.Error())
		return err
	}

	meta, err := cache.ExtractForMedia(ctx, w.DB, w.Vault, mediaID, fileID, filePath, duration)
	if err != nil {
		w.markFailed(mediaID, trimErr("", err))
		return err
	}

	count := len(meta.PTS)
	if count == 0 {
		msg := "no keyframes extracted"
		if storage.InputNeedsPipe(w.DB, mediaID, filePath) {
			probePath := storage.ResolveKeyframeProbePath(w.DB, mediaID, filePath)
			if probePath == filePath {
				msg = "no keyframes from encrypted pipe; plaintext source missing (extract before deleting plain file)"
			} else {
				msg = "no keyframes from probe path"
			}
		}
		w.markFailed(mediaID, msg)
		return fmt.Errorf("%s", msg)
	}

	if err := cache.Save(meta); err != nil {
		w.markFailed(mediaID, err.Error())
		return err
	}
	if w.Derived != nil {
		jsonPath := cache.FilePath(fileID)
		if _, err := w.Derived.FinalizePath(ctx, mediaID, "keyframe_meta", filepath.Base(jsonPath), jsonPath); err != nil {
			w.markFailed(mediaID, err.Error())
			return err
		}
	}

	_, _ = w.DB.Exec(
		`UPDATE keyframe_task SET status='done', output_dir=?, keyframe_count=?, updated_at=CURRENT_TIMESTAMP, error_message=NULL WHERE media_id = ?`,
		w.OutputDir, count, mediaID,
	)
	return nil
}

func (w *Worker) markFailed(mediaID int64, msg string) {
	_, _ = w.DB.Exec(
		`UPDATE keyframe_task SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`,
		msg, mediaID,
	)
}

// EnsureCached checks if a valid keyframe JSON exists for the media item;
// if not, extracts and saves it synchronously.
func (w *Worker) EnsureCached(ctx context.Context, mediaID int64) (*jitkeyframes.Meta, error) {
	cache, err := jitkeyframes.NewCache(w.OutputDir, w.FFprobePath)
	if err != nil {
		return nil, err
	}

	var fileID, filePath sql.NullString
	var duration sql.NullInt64
	if err := w.DB.QueryRow(
		`SELECT file_id, file_path, COALESCE(duration,0) FROM media WHERE id = ? LIMIT 1`,
		mediaID,
	).Scan(&fileID, &filePath, &duration); err != nil {
		return nil, err
	}

	return cache.EnsureCachedForMedia(ctx, w.DB, w.Vault, mediaID, fileID.String, filePath.String, float64(duration.Int64))
}

func trimErr(out string, err error) string {
	msg := strings.TrimSpace(out)
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if len(msg) > 1500 {
		return msg[:1500]
	}
	return msg
}
