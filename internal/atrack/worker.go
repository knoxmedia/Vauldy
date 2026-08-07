package atrack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"knox-media/internal/keystore"
	"knox-media/internal/progressctx"
	"knox-media/internal/storage"
)

// Info represents the current state of an audio track extraction task.
type Info struct {
	Status       string
	OutputDir    string
	ErrorMessage string
}

// AudioTrackInfo describes one extracted audio track.
type AudioTrackInfo struct {
	Index    int    `json:"index"`
	Language string `json:"language"`
	Codec    string `json:"codec"`
	URL      string `json:"url"`
}

// Worker extracts audio tracks from video files as HLS/AAC.
type Worker struct {
	DB          *sql.DB
	Vault       *keystore.Vault
	Derived     *storage.DerivedAssetStore
	FFmpegPath  string
	FFprobePath string
	OutputDir   string
	mu          sync.Mutex
	running     map[int64]bool
	restoreDir  func(string, string) error
}

// NewWorker creates a new audio track extraction worker.
func NewWorker(db *sql.DB, vault *keystore.Vault, derived *storage.DerivedAssetStore, ffmpegPath, ffprobePath, outputDir string) *Worker {
	return &Worker{
		DB:          db,
		Vault:       vault,
		Derived:     derived,
		FFmpegPath:  ffmpegPath,
		FFprobePath: ffprobePath,
		OutputDir:   outputDir,
		running:     map[int64]bool{},
		restoreDir:  restoreAtrackDirectory,
	}
}

// Enqueue upserts a waiting atrack_task; existing failed rows are left unchanged.
func (w *Worker) Enqueue(mediaID int64) {
	_, _ = w.DB.Exec(
		`INSERT INTO atrack_task (media_id, status, updated_at) VALUES (?, 'waiting', CURRENT_TIMESTAMP)
		 ON CONFLICT(media_id) DO UPDATE SET
		   status = CASE WHEN atrack_task.status = 'failed' THEN atrack_task.status ELSE 'waiting' END,
		   updated_at = CURRENT_TIMESTAMP,
		   error_message = CASE WHEN atrack_task.status = 'failed' THEN atrack_task.error_message ELSE NULL END`,
		mediaID,
	)
}

// EnqueueRetry resets an atrack task to waiting for manual retry.
func (w *Worker) EnqueueRetry(mediaID int64) {
	_, _ = w.DB.Exec(
		`INSERT INTO atrack_task (media_id, status, updated_at) VALUES (?, 'waiting', CURRENT_TIMESTAMP)
		 ON CONFLICT(media_id) DO UPDATE SET status='waiting', updated_at=CURRENT_TIMESTAMP, error_message=NULL`,
		mediaID,
	)
}

// Info returns the current atrack_task info for a media item.
func (w *Worker) Info(mediaID int64) Info {
	var info Info
	var status, outputDir, errMsg sql.NullString
	if err := w.DB.QueryRow(
		`SELECT status, output_dir, error_message FROM atrack_task WHERE media_id = ? LIMIT 1`,
		mediaID,
	).Scan(&status, &outputDir, &errMsg); err != nil {
		return info
	}
	info.Status = status.String
	info.OutputDir = outputDir.String
	info.ErrorMessage = errMsg.String
	return info
}

// RunBatch processes up to `limit` waiting atrack tasks.
func (w *Worker) RunBatch(limit int) (done, failed int) {
	rows, err := w.DB.Query(
		`SELECT t.media_id, m.file_path
		 FROM atrack_task t
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
		filePath string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if rows.Scan(&j.mediaID, &j.filePath) == nil {
			jobs = append(jobs, j)
		}
	}
	if len(jobs) == 0 {
		return 0, 0
	}

	for _, j := range jobs {
		if err := w.run(context.Background(), j.mediaID, j.filePath); err != nil {
			failed++
		} else {
			done++
		}
	}
	return done, failed
}

// Run executes audio extraction for a single media item (synchronous, for manual trigger).
type commitGuardKey struct{}
type CommitGuard func(context.Context) error
type CommitGuardTx func(context.Context, *sql.Tx) error

// WithCommitGuard fences filesystem and domain-state commits for a claimed post-ingest generation.
func WithCommitGuardTx(ctx context.Context, guard func(context.Context, *sql.Tx) error) context.Context {
	if guard == nil {
		return ctx
	}
	return context.WithValue(ctx, commitGuardTxKey{}, CommitGuardTx(guard))
}

type commitGuardTxKey struct{}

func WithCommitGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	if guard == nil {
		return ctx
	}
	return context.WithValue(ctx, commitGuardKey{}, CommitGuard(guard))
}

func validateCommitGuardTx(ctx context.Context, tx *sql.Tx) error {
	guard, _ := ctx.Value(commitGuardTxKey{}).(CommitGuardTx)
	if guard != nil {
		return guard(ctx, tx)
	}
	return validateCommitGuard(ctx)
}

func validateCommitGuard(ctx context.Context) error {
	guard, _ := ctx.Value(commitGuardKey{}).(CommitGuard)
	if guard == nil {
		return nil
	}
	return guard(ctx)
}

// Run executes audio extraction for a single media item (synchronous, for manual trigger).
func (w *Worker) Run(ctx context.Context, mediaID int64, inputPath string) error {
	return w.run(ctx, mediaID, inputPath)
}

// rawStream is a minimal probe result for an audio stream.
type rawStream struct {
	Index     int    `json:"index"`
	CodecName string `json:"codec_name"`
	CodecType string `json:"codec_type"`
	Tags      struct {
		Language string `json:"language"`
	} `json:"tags"`
}

type rawProbe struct {
	Streams []rawStream `json:"streams"`
}

// probeAudioStreams returns all audio streams from the source file.
func (w *Worker) probeAudioStreams(ctx context.Context, mediaID int64, inputPath string) ([]rawStream, error) {
	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "a",
	}
	var out []byte
	var err error
	if storage.InputNeedsPipe(w.DB, mediaID, inputPath) {
		raw, cleanup, perr := storage.FFprobeOutputContext(ctx, w.DB, w.Vault, w.FFprobePath, mediaID, inputPath, 0, 0, args)
		if cleanup != nil {
			defer cleanup()
		}
		if perr != nil {
			return nil, fmt.Errorf("ffprobe audio streams: %w", perr)
		}
		out = raw
	} else {
		cmd := exec.CommandContext(ctx, w.FFprobePath, append(args, inputPath)...)
		out, err = cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("ffprobe audio streams: %w", err)
		}
	}
	var probe rawProbe
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, fmt.Errorf("ffprobe json: %w", err)
	}
	var streams []rawStream
	for _, s := range probe.Streams {
		if s.CodecType == "audio" {
			streams = append(streams, s)
		}
	}
	return streams, nil
}

func (w *Worker) run(ctx context.Context, mediaID int64, inputPath string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
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
	var savedError sql.NullString
	qerr := w.DB.QueryRowContext(ctx, `SELECT status,error_message FROM atrack_task WHERE media_id=?`, mediaID).Scan(&taskStatus, &savedError)
	if qerr != nil {
		return qerr
	}
	if taskStatus == "failed" {
		return fmt.Errorf("atrack task failed: %s", savedError.String)
	}
	if err := validateCommitGuard(ctx); err != nil {
		return err
	}

	finalDir := filepath.Join(w.OutputDir, strconv.FormatInt(mediaID, 10))
	stagingDir := filepath.Join(w.OutputDir, fmt.Sprintf("%d.tmp-%s", mediaID, uuid.NewString()))
	defer os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return w.failGuarded(ctx, mediaID, err)
	}
	if _, err := w.DB.ExecContext(ctx, `UPDATE atrack_task SET status='running',updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, mediaID); err != nil {
		return err
	}

	streams, err := w.probeAudioStreams(ctx, mediaID, inputPath)
	if err != nil {
		return w.handleRunError(ctx, mediaID, err)
	}
	if len(streams) == 0 {
		return w.failGuarded(ctx, mediaID, fmt.Errorf("no audio streams"))
	}
	var errs []string
	for _, st := range streams {
		if err := ctx.Err(); err != nil {
			return w.handleRunError(ctx, mediaID, err)
		}
		lang := strings.TrimSpace(st.Tags.Language)
		if lang == "" {
			lang = "und"
		}
		streamDir := filepath.Join(stagingDir, strconv.Itoa(st.Index))
		if err := os.MkdirAll(streamDir, 0o755); err != nil {
			errs = append(errs, fmt.Sprintf("stream %d: %v", st.Index, err))
			continue
		}
		if err := w.extractStream(ctx, mediaID, inputPath, st.Index, strings.EqualFold(st.CodecName, "aac"), streamDir, lang); err != nil {
			errs = append(errs, fmt.Sprintf("stream %d: %v", st.Index, err))
		} else {
			progressctx.Report(ctx)
		}
	}
	if err := ctx.Err(); err != nil {
		return w.handleRunError(ctx, mediaID, err)
	}
	if len(errs) == len(streams) {
		return w.failGuarded(ctx, mediaID, fmt.Errorf("all streams failed: %s", strings.Join(errs, "; ")))
	}
	if err := validateCommitGuard(ctx); err != nil {
		return err
	}

	var staged []*storage.StagedDerivedAsset
	if w.Derived != nil && storage.NeedsDerivedEncryption(w.DB, mediaID) {
		staged, err = w.stageEncryptedOutputs(ctx, mediaID, stagingDir)
		if err != nil {
			return w.failGuarded(ctx, mediaID, err)
		}
		defer func() {
			if staged != nil {
				w.Derived.AbortStaged(staged...)
			}
		}()
	}
	backupDir, err := replaceAtrackDirectory(finalDir, stagingDir)
	if err != nil {
		return w.failGuarded(ctx, mediaID, err)
	}
	committed := false
	defer func() {
		if !committed {
			restore := w.restoreDir
			if restore == nil {
				restore = restoreAtrackDirectory
			}
			err = errors.Join(err, restore(finalDir, backupDir))
		}
	}()
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateCommitGuardTx(ctx, tx); err != nil {
		return err
	}
	var oldPaths []string
	if len(staged) > 0 {
		oldPaths, err = w.Derived.CommitStagedTx(ctx, tx, staged...)
		if err != nil {
			return err
		}
	}
	msg := strings.Join(errs, "; ")
	if _, err = tx.ExecContext(ctx, `UPDATE atrack_task SET status='done',output_dir=?,error_message=?,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, finalDir, nullString(msg), mediaID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	staged = nil
	if backupDir != "" {
		_ = os.RemoveAll(backupDir)
	}
	if w.Derived != nil {
		w.Derived.CleanupReplaced(oldPaths)
	}
	return nil
}

func (w *Worker) handleRunError(ctx context.Context, mediaID int64, runErr error) error {
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := validateCommitGuard(cleanupCtx); err != nil {
			return errors.Join(runErr, err)
		}
		if err := w.markWaiting(cleanupCtx, mediaID, runErr.Error()); err != nil {
			return errors.Join(runErr, err)
		}
		return runErr
	}
	return w.failGuarded(ctx, mediaID, runErr)
}
func (w *Worker) failGuarded(ctx context.Context, mediaID int64, runErr error) error {
	if err := validateCommitGuard(ctx); err != nil {
		return errors.Join(runErr, err)
	}
	if err := w.markFailed(ctx, mediaID, runErr.Error()); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}
func replaceAtrackDirectory(finalDir, stagingDir string) (string, error) {
	backup := ""
	if _, err := os.Stat(finalDir); err == nil {
		backup = finalDir + ".bak-" + uuid.NewString()
		if err := os.Rename(finalDir, backup); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(stagingDir, finalDir); err != nil {
		if backup != "" {
			_ = os.Rename(backup, finalDir)
		}
		return "", err
	}
	return backup, nil
}
func restoreAtrackDirectory(finalDir, backup string) error {
	if err := os.RemoveAll(finalDir); err != nil {
		return err
	}
	if backup != "" {
		return os.Rename(backup, finalDir)
	}
	return nil
}
func nullString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// extractStream runs ffmpeg to extract one audio stream as HLS in MPEG-TS container.
func (w *Worker) extractStream(ctx context.Context, mediaID int64, inputPath string, streamIdx int, isAAC bool, outDir string, lang string) error {
	playlistPath := filepath.Join(outDir, "index.m3u8")
	segPattern := filepath.Join(outDir, "seg_%03d.ts")

	post := []string{
		"-map", fmt.Sprintf("0:%d", streamIdx),
		"-vn",
		"-sn",
	}
	if isAAC {
		post = append(post, "-c:a", "copy")
	} else {
		post = append(post, "-c:a", "aac", "-b:a", "128k", "-ac", "2")
	}
	post = append(post,
		"-hls_time", "6",
		"-hls_list_size", "0",
		"-hls_segment_filename", segPattern,
		playlistPath,
	)
	if out, err := storage.RunFFmpegWithLiveness(ctx, w.DB, w.Vault, w.FFmpegPath, mediaID, inputPath, 0, 0, nil, post, "", func() { progressctx.Report(ctx) }); err != nil {
		return fmt.Errorf("%w: %s", err, trimErr(string(out), err))
	}

	// Write a small metadata file so the handler can read language info later.
	meta := fmt.Sprintf(`{"language":"%s","codec":"%s"}`, lang, map[bool]string{true: "aac", false: "aac"}[isAAC])
	metaPath := filepath.Join(outDir, "meta.json")
	_ = os.WriteFile(metaPath, []byte(meta), 0o644)

	return nil
}

func (w *Worker) stageEncryptedOutputs(ctx context.Context, mediaID int64, root string) ([]*storage.StagedDerivedAsset, error) {
	var staged []*storage.StagedDerivedAsset
	abort := func(err error) ([]*storage.StagedDerivedAsset, error) {
		w.Derived.AbortStaged(staged...)
		return nil, err
	}
	streams, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, stream := range streams {
		if !stream.IsDir() {
			continue
		}
		dir := filepath.Join(root, stream.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return abort(err)
		}
		for _, ent := range files {
			if ent.IsDir() {
				continue
			}
			name := ent.Name()
			kind := "atrack_segment"
			if strings.EqualFold(name, "index.m3u8") {
				kind = "atrack_playlist"
			} else if strings.EqualFold(name, "meta.json") {
				kind = "atrack_meta"
			}
			a, err := w.Derived.StagePath(ctx, mediaID, kind, stream.Name()+"/"+name, filepath.Join(dir, name))
			if err != nil {
				return abort(err)
			}
			staged = append(staged, a)
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return staged, nil
}
func (w *Worker) markFailed(ctx context.Context, mediaID int64, msg string) error {
	_, err := w.DB.ExecContext(ctx,
		`UPDATE atrack_task SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`,
		msg, mediaID,
	)
	return err
}

func (w *Worker) markWaiting(ctx context.Context, mediaID int64, msg string) error {
	_, err := w.DB.ExecContext(ctx, `UPDATE atrack_task SET status='waiting', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, msg, mediaID)
	return err
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
