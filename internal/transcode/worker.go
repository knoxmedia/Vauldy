package transcode

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

)

type Rendition struct {
	Name      string
	Height    int
	Width     int
	VideoRate string
	AudioRate string
	Bandwidth int
}

type EncoderBackend string

const (
	EncoderQSV   EncoderBackend = "qsv"
	EncoderAMF   EncoderBackend = "amf"
	EncoderVAAPI EncoderBackend = "vaapi"
	EncoderNVENC EncoderBackend = "nvenc"
	EncoderX264  EncoderBackend = "libx264"
)

var allRenditions = []Rendition{
	{Name: "360p", Height: 360, Width: 640, VideoRate: "850k", AudioRate: "96k", Bandwidth: 1000000},
	{Name: "480p", Height: 480, Width: 854, VideoRate: "1400k", AudioRate: "128k", Bandwidth: 1700000},
	{Name: "720p", Height: 720, Width: 1280, VideoRate: "2800k", AudioRate: "128k", Bandwidth: 3300000},
	{Name: "1080p", Height: 1080, Width: 1920, VideoRate: "5000k", AudioRate: "160k", Bandwidth: 5800000},
}

type Worker struct {
	DB           *sql.DB
	FFmpegPath   string
	TranscodeDir string
	Encoder      EncoderBackend
	mu           sync.Mutex
	running      map[int64]context.CancelFunc
}

func NewWorker(db *sql.DB, ffmpegPath, transcodeDir string) *Worker {
	w := &Worker{
		DB:           db,
		FFmpegPath:   ffmpegPath,
		TranscodeDir: transcodeDir,
		running:      make(map[int64]context.CancelFunc),
	}
	w.Encoder = w.loadSettings().EffectiveEncoderBackend()
	log.Printf("transcode encoder selected: %s", w.Encoder)
	return w
}

func (w *Worker) loadSettings() Settings {
	if w == nil || w.DB == nil {
		return DefaultSettings()
	}
	var raw sql.NullString
	if err := w.DB.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw); err != nil {
		return DefaultSettings()
	}
	return SettingsFromOptionsJSON(raw.String)
}

func (w *Worker) RunTask(ctx context.Context, taskID int64, inputPath, quality string) error {
	if taskID <= 0 {
		return nil
	}
	w.mu.Lock()
	if _, ok := w.running[taskID]; ok {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	res, err := w.DB.Exec(`UPDATE transcode_task SET status='running', progress=1 WHERE id=? AND status='waiting'`, taskID)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil
	}

	var fileID string
	var sourceHeight int
	_ = w.DB.QueryRow(`SELECT file_id, COALESCE(height,0) FROM media WHERE file_path = ? ORDER BY id DESC LIMIT 1`, inputPath).Scan(&fileID, &sourceHeight)
	if fileID == "" {
		_ = w.DB.QueryRow(`
			SELECT t.file_id, COALESCE(m.height,0)
			FROM transcode_task t
			LEFT JOIN media m ON m.file_id = t.file_id
			WHERE t.id = ?
			LIMIT 1
		`, taskID).Scan(&fileID, &sourceHeight)
	}

	ladder := ladderFromTaskQuality(quality, sourceHeight)
	if len(ladder) == 0 {
		ladder = selectRenditions("720p", sourceHeight)
	}
	w.Encoder = w.loadSettings().EffectiveEncoderBackend()
	outDir := taskOutputDir(w.TranscodeDir, taskID, fileID, quality, ladder)
	return w.runHLS(ctx, taskID, inputPath, outDir, ladder)
}

// StartWaiting launches up to limit waiting transcode tasks in background goroutines.
func (w *Worker) StartWaiting(ctx context.Context, limit int) int {
	if w == nil || w.DB == nil || limit <= 0 {
		return 0
	}
	rows, err := w.DB.Query(`
		SELECT t.id, COALESCE(m.file_path,''), t.quality
		FROM transcode_task t
		LEFT JOIN media m ON m.file_id = t.file_id
		WHERE t.status = 'waiting' AND COALESCE(m.file_path,'') != ''
		ORDER BY t.id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return 0
	}
	defer rows.Close()

	started := 0
	for rows.Next() {
		if started >= limit {
			break
		}
		var taskID int64
		var filePath, quality string
		if rows.Scan(&taskID, &filePath, &quality) != nil {
			continue
		}
		w.mu.Lock()
		already := w.running[taskID] != nil
		w.mu.Unlock()
		if already {
			continue
		}
		started++
		id, fp, q := taskID, filePath, quality
		go func() {
			_ = w.RunTask(context.Background(), id, fp, q)
		}()
	}
	return started
}

func ladderFromTaskQuality(quality string, sourceHeight int) []Rendition {
	q := strings.TrimSpace(quality)
	if strings.HasPrefix(q, "abr:") {
		profile := strings.TrimPrefix(q, "abr:")
		allowed := make(map[string]struct{})
		for _, name := range strings.Split(profile, "+") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "" {
				allowed[name] = struct{}{}
			}
		}
		maxH := sourceHeight
		if maxH <= 0 {
			maxH = 1080
		}
		var out []Rendition
		for _, r := range allRenditions {
			if r.Height > maxH {
				continue
			}
			if _, ok := allowed[strings.ToLower(r.Name)]; ok {
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Height < out[j].Height })
		return out
	}
	return selectRenditions(quality, sourceHeight)
}

func taskOutputDir(transcodeDir string, taskID int64, fileID, quality string, ladder []Rendition) string {
	if strings.HasPrefix(strings.TrimSpace(quality), "abr:") && strings.TrimSpace(fileID) != "" {
		return filepath.Join(transcodeDir, fileID, ladderKey(ladder))
	}
	return filepath.Join(transcodeDir, strconv.FormatInt(taskID, 10))
}

// EnsureHLS starts or resumes full-ladder HLS transcoding in a background goroutine.
// Callers must not pass http.Request.Context(): it is canceled when the handler returns,
// which would kill ffmpeg immediately. Async transcode uses an independent root context.
func (w *Worker) EnsureHLS(fileID, inputPath string, sourceHeight, maxHeight int, requested []string) (playlist string, status string, taskID int64, err error) {
	ladder := chooseLadder(sourceHeight, maxHeight, requested)
	if len(ladder) == 0 {
		ladder = selectRenditions("360p", 360)
	}
	profileKey := ladderKey(ladder)
	cacheKey := "abr:" + profileKey

	var existedID int64
	var existedStatus, existedPath sql.NullString
	qerr := w.DB.QueryRow(`
		SELECT id, status, output_path
		FROM transcode_task
		WHERE file_id = ? AND quality = ?
		ORDER BY id DESC
		LIMIT 1
	`, fileID, cacheKey).Scan(&existedID, &existedStatus, &existedPath)
	if qerr == nil {
		if existedStatus.String == "done" && existedPath.Valid {
			if st, e := os.Stat(existedPath.String); e == nil && !st.IsDir() {
				return existedPath.String, "done", existedID, nil
			}
		}
		if existedStatus.String == "waiting" || existedStatus.String == "running" {
			return existedPath.String, existedStatus.String, existedID, nil
		}
	}
	if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return "", "", 0, qerr
	}

	res, ierr := w.DB.Exec(`INSERT INTO transcode_task (file_id, quality, status, progress) VALUES (?, ?, 'waiting', 0)`, fileID, cacheKey)
	if ierr != nil {
		return "", "", 0, ierr
	}
	tid, _ := res.LastInsertId()
	outDir := filepath.Join(w.TranscodeDir, fileID, profileKey)
	// Background worker loop starts waiting tasks respecting concurrency limits.
	return filepath.Join(outDir, "master.m3u8"), "waiting", tid, nil
}

func (w *Worker) runHLS(ctx context.Context, taskID int64, inputPath, outDir string, ladder []Rendition) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	outFile := filepath.Join(outDir, "master.m3u8")
	ctx2, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.running[taskID] = cancel
	w.mu.Unlock()
	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.running, taskID)
		w.mu.Unlock()
	}()

	// Write placeholder .m3u8 files for all renditions so the player doesn't get 404.
	for _, r := range ladder {
		_ = writePlaceholderM3U8(filepath.Join(outDir, r.Name+".m3u8"))
	}

	if len(ladder) > 0 {
		// Write master placeholder immediately, pointing to all (empty) renditions.
		_ = writeMasterPlaylist(outDir, ladder)
		_, _ = w.DB.Exec(`UPDATE transcode_task SET status = ?, progress = ?, output_path = ? WHERE id = ?`, "running", 2, outFile, taskID)

		// Transcode renditions in serial (lightest first), updating master after each.
		// Rendition ffmpeg overwrites the placeholder; after the first one finishes the
		// polling endpoint returns ready=true and playback can begin.
		for i := 0; i < len(ladder); i++ {
			if err := w.transcodeRendition(ctx2, taskID, inputPath, outDir, ladder[i], i, len(ladder)); err != nil {
				return err
			}
			_ = writeMasterPlaylistPartial(outDir, ladder, i)
		}
	} else {
		_, _ = w.DB.Exec(`UPDATE transcode_task SET status = ?, progress = ?, output_path = ? WHERE id = ?`, "running", 5, outFile, taskID)
	}

	// Final master with all renditions.
	_ = writeMasterPlaylist(outDir, ladder)
	_, _ = w.DB.Exec(`UPDATE transcode_task SET status = ?, progress = ?, error_message = NULL WHERE id = ?`, "done", 100, taskID)
	return nil
}

func (w *Worker) transcodeRendition(ctx context.Context, taskID int64, inputPath, outDir string, r Rendition, idx, total int) error {
	vf := fmt.Sprintf("scale=-2:%d", r.Height)
	prefix := []string{"-y", "-hide_banner", "-nostats", "-loglevel", "error", "-i", inputPath, "-map", "0:v:0", "-map", "0:a:0?"}
	suffix := []string{
		"-c:a", "aac", "-b:a", r.AudioRate,
		"-f", "hls",
		"-hls_time", "4",
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", filepath.Join(outDir, r.Name+"_%03d.ts"),
		filepath.Join(outDir, r.Name+".m3u8"),
	}
	x264Preset := w.loadSettings().EffectiveBackgroundPreset()
	try := func(enc EncoderBackend) (stderrOut string, err error) {
		args := append(append(append([]string{}, prefix...), w.encoderArgsFor(enc, vf, r.VideoRate, x264Preset)...), suffix...)
		cmd := exec.CommandContext(ctx, w.FFmpegPath, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err = cmd.Run()
		return stderr.String(), err
	}
	stderrStr, runErr := try(w.Encoder)
	if runErr != nil && w.Encoder != EncoderX264 {
		log.Printf("transcode task=%d rendition=%s encoder=%s failed, retry libx264: %v", taskID, r.Name, w.Encoder, runErr)
		stderrStr, runErr = try(EncoderX264)
	}
	if runErr != nil {
		failMsg := ffmpegFailureMessage(runErr, stderrStr)
		_, _ = w.DB.Exec(`UPDATE transcode_task SET status = ?, progress = ?, error_message = ? WHERE id = ?`, "failed", 0, failMsg, taskID)
		return runErr
	}
	progress := 10 + ((idx + 1) * 80 / total)
	_, _ = w.DB.Exec(`UPDATE transcode_task SET progress = ? WHERE id = ?`, progress, taskID)
	return nil
}

func (w *Worker) Cancel(taskID int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.running[taskID]; ok {
		c()
		return true
	}
	return false
}

func (w *Worker) encoderArgs(vf string, videoRate string) []string {
	return w.encoderArgsFor(w.Encoder, vf, videoRate, w.loadSettings().EffectiveBackgroundPreset())
}

func (w *Worker) encoderArgsFor(enc EncoderBackend, vf string, videoRate string, x264Preset string) []string {
	if strings.TrimSpace(x264Preset) == "" {
		x264Preset = "veryfast"
	}
	switch enc {
	case EncoderQSV:
		return []string{"-vf", vf, "-c:v", "h264_qsv", "-b:v", videoRate, "-maxrate", videoRate, "-bufsize", "2M"}
	case EncoderAMF:
		return []string{"-vf", vf, "-c:v", "h264_amf", "-quality", "balanced", "-rc", "vbr_peak", "-b:v", videoRate, "-maxrate", videoRate, "-bufsize", "2M"}
	case EncoderVAAPI:
		height := "720"
		if idx := strings.LastIndex(vf, ":"); idx >= 0 && idx+1 < len(vf) {
			height = vf[idx+1:]
		}
		return []string{"-vf", "format=nv12,hwupload,scale_vaapi=w=-2:h=" + height, "-c:v", "h264_vaapi", "-b:v", videoRate}
	case EncoderNVENC:
		return []string{"-vf", vf, "-c:v", "h264_nvenc", "-preset", "p4", "-b:v", videoRate, "-maxrate", videoRate, "-bufsize", "2M"}
	default:
		return []string{"-vf", vf, "-c:v", "libx264", "-preset", x264Preset, "-b:v", videoRate, "-maxrate", videoRate, "-bufsize", "2M"}
	}
}

// ffmpegFailureMessage formats stderr for DB/logs. FFmpeg prints a very long build
// configuration banner by default; combined with trimErrorMessage limits, that hides
// the real error unless we suppress the banner (-hide_banner) and prefer stderr tail.
func ffmpegFailureMessage(runErr error, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	const max = 1600
	if len(stderr) > max {
		stderr = "...[stderr truncated]\n" + stderr[len(stderr)-max:]
	}
	return trimErrorMessage(fmt.Sprintf("ffmpeg failed: %v; stderr: %s", runErr, stderr))
}

func chooseLadder(sourceHeight int, maxHeight int, requested []string) []Rendition {
	if maxHeight <= 0 {
		maxHeight = 1080
	}
	if sourceHeight > 0 && sourceHeight < maxHeight {
		maxHeight = sourceHeight
	}
	if len(requested) > 0 {
		allowed := make(map[string]struct{}, len(requested))
		for _, q := range requested {
			allowed[strings.ToLower(strings.TrimSpace(q))] = struct{}{}
		}
		var out []Rendition
		for _, r := range allRenditions {
			if r.Height <= maxHeight {
				if _, ok := allowed[strings.ToLower(r.Name)]; ok {
					out = append(out, r)
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Height < out[j].Height })
		return out
	}
	return selectRenditions("1080p", maxHeight)
}

func selectRenditions(quality string, maxHeight int) []Rendition {
	target := strings.TrimSpace(strings.ToLower(quality))
	if target == "" {
		target = "1080p"
	}
	if maxHeight <= 0 {
		maxHeight = 1080
	}
	var out []Rendition
	for _, r := range allRenditions {
		if r.Height > maxHeight {
			continue
		}
		switch target {
		case "abr", "auto", "1080p":
			out = append(out, r)
		case "720p":
			if r.Height <= 720 {
				out = append(out, r)
			}
		case "480p":
			if r.Height <= 480 {
				out = append(out, r)
			}
		case "360p":
			if r.Height <= 360 {
				out = append(out, r)
			}
		default:
			if r.Name == target {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Height < out[j].Height })
	return out
}

func ladderKey(ladder []Rendition) string {
	names := make([]string, 0, len(ladder))
	for _, r := range ladder {
		names = append(names, r.Name)
	}
	return strings.Join(names, "+")
}

func writeMasterPlaylist(outDir string, ladder []Rendition) error {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	for _, r := range ladder {
		sb.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d\n", r.Bandwidth, r.Width, r.Height))
		sb.WriteString(r.Name + ".m3u8\n")
	}
	return os.WriteFile(filepath.Join(outDir, "master.m3u8"), []byte(sb.String()), 0o644)
}

// writeMasterPlaylistPartial writes a master playlist referencing only the first
// N renditions (0-indexed: up to and including index maxIdx).
func writeMasterPlaylistPartial(outDir string, ladder []Rendition, maxIdx int) error {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	for i := 0; i <= maxIdx && i < len(ladder); i++ {
		r := ladder[i]
		sb.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d\n", r.Bandwidth, r.Width, r.Height))
		sb.WriteString(r.Name + ".m3u8\n")
	}
	return os.WriteFile(filepath.Join(outDir, "master.m3u8"), []byte(sb.String()), 0o644)
}

// writePlaceholderM3U8 creates an empty HLS playlist so the player doesn't get
// a 404 before ffmpeg has started encoding the rendition.
func writePlaceholderM3U8(path string) error {
	// A minimal EXT-X-STREAM-INF playlist that signals "no segments yet".
	return os.WriteFile(path, []byte("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:EVENT\n"), 0o644)
}

func trimErrorMessage(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		return s[:2000]
	}
	return s
}
