package atrack

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"knox-media/internal/keystore"
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
		raw, cleanup, perr := storage.FFprobeOutput(w.DB, w.Vault, w.FFprobePath, mediaID, inputPath, 0, 0, args)
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

func (w *Worker) run(ctx context.Context, mediaID int64, inputPath string) error {
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
	if qerr := w.DB.QueryRow(`SELECT status FROM atrack_task WHERE media_id = ?`, mediaID).Scan(&taskStatus); qerr == nil && taskStatus == "failed" {
		return nil
	}

	outDir := filepath.Join(w.OutputDir, strconv.FormatInt(mediaID, 10))
	// Remove old output to enable re-extraction.
	_ = os.RemoveAll(outDir)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		w.markFailed(mediaID, err.Error())
		return err
	}

	_, _ = w.DB.Exec(
		`UPDATE atrack_task SET status='running', updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`,
		mediaID,
	)

	// Probe source for audio streams.
	streams, err := w.probeAudioStreams(ctx, mediaID, inputPath)
	if err != nil {
		w.markFailed(mediaID, err.Error())
		return err
	}
	if len(streams) == 0 {
		w.markFailed(mediaID, "no audio streams found")
		return fmt.Errorf("no audio streams")
	}

	var errs []string
	for _, st := range streams {
		lang := strings.TrimSpace(st.Tags.Language)
		if lang == "" {
			lang = "und"
		}
		streamDir := filepath.Join(outDir, strconv.Itoa(st.Index))
		if err := os.MkdirAll(streamDir, 0o755); err != nil {
			errs = append(errs, fmt.Sprintf("stream %d: %v", st.Index, err))
			continue
		}

		isAAC := strings.ToLower(st.CodecName) == "aac"
		if err := w.extractStream(ctx, mediaID, inputPath, st.Index, isAAC, streamDir, lang); err != nil {
			errs = append(errs, fmt.Sprintf("stream %d: %v", st.Index, err))
			continue
		}
	}

	if len(errs) > 0 {
		if len(errs) == len(streams) {
			w.markFailed(mediaID, strings.Join(errs, "; "))
			return fmt.Errorf("all streams failed: %s", strings.Join(errs, "; "))
		}
		// Partial success (some streams failed): mark done but log errors.
		msg := strings.Join(errs, "; ")
		_, _ = w.DB.Exec(`UPDATE atrack_task SET status='done', output_dir=?, error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`,
			outDir, msg, mediaID)
		return nil
	}

	_, _ = w.DB.Exec(
		`UPDATE atrack_task SET status='done', output_dir=?, updated_at=CURRENT_TIMESTAMP, error_message=NULL WHERE media_id = ?`,
		outDir, mediaID,
	)
	return nil
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
	if out, err := storage.RunFFmpeg(ctx, w.DB, w.Vault, w.FFmpegPath, mediaID, inputPath, 0, 0, nil, post, ""); err != nil {
		return fmt.Errorf("%w: %s", err, trimErr(string(out), err))
	}

	// Write a small metadata file so the handler can read language info later.
	meta := fmt.Sprintf(`{"language":"%s","codec":"%s"}`, lang, map[bool]string{true: "aac", false: "aac"}[isAAC])
	metaPath := filepath.Join(outDir, "meta.json")
	_ = os.WriteFile(metaPath, []byte(meta), 0o644)

	return w.encryptStreamOutputs(ctx, mediaID, streamIdx, outDir)
}

func (w *Worker) encryptStreamOutputs(ctx context.Context, mediaID int64, streamIdx int, outDir string) error {
	if w.Derived == nil || !storage.NeedsDerivedEncryption(w.DB, mediaID) {
		return nil
	}
	streamKey := strconv.Itoa(streamIdx)
	files, err := os.ReadDir(outDir)
	if err != nil {
		return err
	}
	for _, ent := range files {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		full := filepath.Join(outDir, name)
		kind := "atrack_segment"
		if strings.EqualFold(name, "index.m3u8") {
			kind = "atrack_playlist"
		} else if strings.EqualFold(name, "meta.json") {
			kind = "atrack_meta"
		}
		logical := streamKey + "/" + name
		if _, err := w.Derived.FinalizePath(ctx, mediaID, kind, logical, full); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) markFailed(mediaID int64, msg string) {
	_, _ = w.DB.Exec(
		`UPDATE atrack_task SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`,
		msg, mediaID,
	)
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
