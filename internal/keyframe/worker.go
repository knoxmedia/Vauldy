package keyframe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	jitkeyframes "knox-media/internal/jit/keyframes"
	"knox-media/internal/keystore"
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
	extract     func(context.Context, int64, string, string, float64) (*jitkeyframes.Meta, error)
}

// NewWorker creates a new keyframe extraction worker.
func NewWorker(db *sql.DB, vault *keystore.Vault, derived *storage.DerivedAssetStore, ffprobePath, outputDir string) *Worker {
	w := &Worker{
		DB:          db,
		Vault:       vault,
		Derived:     derived,
		FFprobePath: ffprobePath,
		OutputDir:   outputDir,
		running:     map[int64]bool{},
	}
	w.extract = w.extractReal
	return w
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
func (w *Worker) EnqueueRetry(mediaID int64) error {
	return w.EnqueueRetryContext(context.Background(), mediaID)
}

// EnqueueRetryContext resets a keyframe task while propagating database and cancellation errors.
func (w *Worker) EnqueueRetryContext(ctx context.Context, mediaID int64) error {
	if w == nil || w.DB == nil {
		return errors.New("keyframe worker: database is not configured")
	}
	result, err := w.DB.ExecContext(ctx, `INSERT INTO keyframe_task (media_id,status,updated_at) VALUES (?,'waiting',CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO UPDATE SET status='waiting',updated_at=CURRENT_TIMESTAMP,error_message=NULL,keyframe_count=0`, mediaID)
	if err != nil {
		return err
	}
	_, err = result.RowsAffected()
	return err
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

// RunOne synchronously processes the existing keyframe task for mediaID.
func (w *Worker) RunOne(ctx context.Context, mediaID int64) error {
	if w == nil || w.DB == nil {
		return errors.New("keyframe worker: database is not configured")
	}
	if err := ctx.Err(); err != nil {
		w.resetWaitingAfterCancel(ctx, mediaID, err)
		return err
	}
	var status string
	var outputDir, errorMessage sql.NullString
	var count int
	if err := w.DB.QueryRowContext(ctx, `SELECT status,output_dir,COALESCE(keyframe_count,0),error_message FROM keyframe_task WHERE media_id=?`, mediaID).Scan(&status, &outputDir, &count, &errorMessage); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("keyframe task for media %d not found", mediaID)
		}
		return err
	}
	if status == "failed" {
		if strings.TrimSpace(errorMessage.String) != "" {
			return errors.New(errorMessage.String)
		}
		return fmt.Errorf("keyframe task for media %d failed", mediaID)
	}
	var libraryID int64
	var fileID, filePath string
	var duration float64
	if err := w.DB.QueryRowContext(ctx, `SELECT library_id,COALESCE(file_id,''),COALESCE(file_path,''),COALESCE(duration,0) FROM media WHERE id=?`, mediaID).Scan(&libraryID, &fileID, &filePath, &duration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("media %d not found", mediaID)
		}
		return err
	}
	if status == "done" && count > 0 {
		exists, err := w.completedArtifactExists(ctx, mediaID, fileID, outputDir.String)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	filePath = storage.PreferredFFmpegPath(w.DB, mediaID, libraryID, filePath)
	if strings.TrimSpace(filePath) == "" {
		return fmt.Errorf("keyframe task for media %d: source file is unavailable", mediaID)
	}
	return w.run(ctx, mediaID, fileID, filePath, duration)
}

func (w *Worker) completedArtifactExists(ctx context.Context, mediaID int64, fileID, outputDir string) (bool, error) {
	logicalName := sanitizeKeyframeFileID(fileID) + ".json"
	if regularArtifact(filepath.Join(outputDir, logicalName)) {
		return true, nil
	}
	var encPath string
	err := w.DB.QueryRowContext(ctx, `SELECT enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind='keyframe_meta' AND logical_name=?`, mediaID, logicalName).Scan(&encPath)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return regularArtifact(encPath), nil
}

func regularArtifact(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}
func sanitizeKeyframeFileID(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, s)
}

func (w *Worker) extractReal(ctx context.Context, mediaID int64, fileID, filePath string, duration float64) (*jitkeyframes.Meta, error) {
	cache, err := jitkeyframes.NewCache(w.OutputDir, w.FFprobePath)
	if err != nil {
		return nil, err
	}
	return cache.ExtractForMedia(ctx, w.DB, w.Vault, mediaID, fileID, filePath, duration)
}

func (w *Worker) resetWaitingAfterCancel(ctx context.Context, mediaID int64, cause error) {
	cleanupCtx := context.WithoutCancel(ctx)
	_, _ = w.DB.ExecContext(cleanupCtx, `UPDATE keyframe_task SET status='waiting',error_message=?,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, trimErr("", cause), mediaID)
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

func (w *Worker) run(ctx context.Context, mediaID int64, fileID, filePath string, duration float64) (runErr error) {
	if err := ctx.Err(); err != nil {
		return w.handleRunError(ctx, mediaID, err)
	}
	w.mu.Lock()
	if w.running[mediaID] {
		w.mu.Unlock()
		return fmt.Errorf("already running for media %d", mediaID)
	}
	w.running[mediaID] = true
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.running, mediaID); w.mu.Unlock() }()
	var taskStatus string
	var saved sql.NullString
	if err := w.DB.QueryRowContext(ctx, `SELECT status,error_message FROM keyframe_task WHERE media_id=?`, mediaID).Scan(&taskStatus, &saved); err != nil {
		return err
	}
	if taskStatus == "failed" {
		return fmt.Errorf("keyframe task failed: %s", saved.String)
	}
	if err := validateCommitGuard(ctx); err != nil {
		return err
	}
	if _, err := w.DB.ExecContext(ctx, `UPDATE keyframe_task SET status='running',updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, mediaID); err != nil {
		return err
	}
	if strings.TrimSpace(fileID) == "" {
		var fid sql.NullString
		if err := w.DB.QueryRowContext(ctx, `SELECT file_id FROM media WHERE id=?`, mediaID).Scan(&fid); err != nil {
			return err
		}
		fileID = fid.String
	}
	cache, err := jitkeyframes.NewCache(w.OutputDir, w.FFprobePath)
	if err != nil {
		return w.failGuarded(ctx, mediaID, err)
	}
	extract := w.extract
	if extract == nil {
		extract = w.extractReal
	}
	meta, err := extract(ctx, mediaID, fileID, filePath, duration)
	if err != nil {
		return w.handleRunError(ctx, mediaID, err)
	}
	count := len(meta.PTS)
	if count == 0 {
		return w.failGuarded(ctx, mediaID, fmt.Errorf("no keyframes extracted"))
	}
	stage := filepath.Join(w.OutputDir, "."+sanitizeKeyframeFileID(fileID)+"."+uuid.NewString()+".tmp.json")
	defer os.Remove(stage)
	if err = jitkeyframes.SaveToPath(meta, stage); err != nil {
		return w.failGuarded(ctx, mediaID, err)
	}
	if err = validateCommitGuard(ctx); err != nil {
		return err
	}
	final := cache.FilePath(fileID)
	encrypted := w.Derived != nil && storage.NeedsDerivedEncryption(w.DB, mediaID)
	var backup string
	var staged *storage.StagedDerivedAsset
	stored := final
	if encrypted {
		staged, err = w.Derived.StagePath(ctx, mediaID, "keyframe_meta", filepath.Base(final), stage)
		if err != nil {
			return w.failGuarded(ctx, mediaID, err)
		}
		defer func() {
			if staged != nil {
				w.Derived.AbortStaged(staged)
			}
		}()
		stored = staged.EncPath()
	} else {
		backup, err = replaceKeyframeFile(stage, final)
		if err != nil {
			return w.failGuarded(ctx, mediaID, err)
		}
		defer func() {
			if backup != "!committed" {
				runErr = errors.Join(runErr, restoreKeyframeFile(final, backup))
			}
		}()
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateCommitGuardTx(ctx, tx); err != nil {
		return err
	}
	var old []string
	if encrypted {
		old, err = w.Derived.CommitStagedTx(ctx, tx, staged)
		if err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE keyframe_task SET status='done',output_dir=?,keyframe_count=?,updated_at=CURRENT_TIMESTAMP,error_message=NULL WHERE media_id=?`, w.OutputDir, count, mediaID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return fmt.Errorf("keyframe task for media %d was not persisted as done", mediaID)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_ = stored
	if !encrypted {
		if backup != "" {
			_ = os.Remove(backup)
		}
		backup = "!committed"
	} else {
		staged = nil
		w.Derived.CleanupReplaced(old)
	}
	return nil
}
func replaceKeyframeFile(stage, final string) (string, error) {
	backup := ""
	if _, err := os.Stat(final); err == nil {
		backup = final + ".bak-" + uuid.NewString()
		if err = os.Rename(final, backup); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(stage, final); err != nil {
		if backup != "" {
			_ = os.Rename(backup, final)
		}
		return "", err
	}
	return backup, nil
}
func restoreKeyframeFile(final, backup string) error {
	if err := os.Remove(final); err != nil && !os.IsNotExist(err) {
		return err
	}
	if backup != "" {
		return os.Rename(backup, final)
	}
	return nil
}
func (w *Worker) handleRunError(ctx context.Context, mediaID int64, runErr error) error {
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) || ctx.Err() != nil {
		cleanup := context.WithoutCancel(ctx)
		if err := validateCommitGuard(cleanup); err != nil {
			return errors.Join(runErr, err)
		}
		_, err := w.DB.ExecContext(cleanup, `UPDATE keyframe_task SET status='waiting',error_message=?,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, trimErr("", runErr), mediaID)
		return errors.Join(runErr, err)
	}
	return w.failGuarded(ctx, mediaID, runErr)
}
func (w *Worker) failGuarded(ctx context.Context, mediaID int64, runErr error) error {
	if err := validateCommitGuard(ctx); err != nil {
		return errors.Join(runErr, err)
	}
	_, err := w.DB.ExecContext(ctx, `UPDATE keyframe_task SET status='failed',error_message=?,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, trimErr("", runErr), mediaID)
	return errors.Join(runErr, err)
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
