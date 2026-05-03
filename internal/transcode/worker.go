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

	"knox-media/internal/jit/hwenc"
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
	w.Encoder = w.detectEncoderBackend()
	log.Printf("transcode encoder selected: %s", w.Encoder)
	return w
}

func (w *Worker) RunTask(ctx context.Context, taskID int64, inputPath, quality string) error {
	ladder := selectRenditions(quality, 1080)
	if len(ladder) == 0 {
		ladder = selectRenditions("720p", 1080)
	}
	outDir := filepath.Join(w.TranscodeDir, strconv.FormatInt(taskID, 10))
	return w.runHLS(ctx, taskID, inputPath, outDir, ladder)
}

func (w *Worker) EnsureHLS(ctx context.Context, fileID, inputPath string, sourceHeight, maxHeight int, requested []string) (playlist string, status string, taskID int64, err error) {
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
	go func(taskID int64, ladder []Rendition) {
		_ = w.runHLS(ctx, taskID, inputPath, outDir, ladder)
	}(tid, ladder)

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

	_, _ = w.DB.Exec(`UPDATE transcode_task SET status = ?, progress = ? WHERE id = ?`, "running", 5, taskID)
	for i, r := range ladder {
		vf := fmt.Sprintf("scale=-2:%d", r.Height)
		args := []string{"-y", "-i", inputPath, "-map", "0:v:0", "-map", "0:a:0?"}
		args = append(args, w.encoderArgs(vf, r.VideoRate)...)
		args = append(args,
			"-c:a", "aac", "-b:a", r.AudioRate,
			"-f", "hls",
			"-hls_time", "4",
			"-hls_playlist_type", "vod",
			"-hls_segment_filename", filepath.Join(outDir, r.Name+"_%03d.ts"),
			filepath.Join(outDir, r.Name+".m3u8"),
		)
		cmd := exec.CommandContext(ctx2, w.FFmpegPath, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			failMsg := trimErrorMessage(fmt.Sprintf("ffmpeg failed: %v; stderr: %s", err, stderr.String()))
			_, _ = w.DB.Exec(`UPDATE transcode_task SET status = ?, progress = ?, error_message = ? WHERE id = ?`, "failed", 0, failMsg, taskID)
			return err
		}
		progress := 10 + ((i + 1) * 80 / len(ladder))
		_, _ = w.DB.Exec(`UPDATE transcode_task SET progress = ? WHERE id = ?`, progress, taskID)
	}
	if err := writeMasterPlaylist(outDir, ladder); err != nil {
		_, _ = w.DB.Exec(`UPDATE transcode_task SET status = ?, progress = ?, error_message = ? WHERE id = ?`, "failed", 0, trimErrorMessage(err.Error()), taskID)
		return err
	}
	_, _ = w.DB.Exec(`UPDATE transcode_task SET status = ?, progress = ?, output_path = ?, error_message = NULL WHERE id = ?`, "done", 100, outFile, taskID)
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

func (w *Worker) detectEncoderBackend() EncoderBackend {
	switch hwenc.DetectH264Encoder(w.FFmpegPath) {
	case hwenc.H264QSV:
		return EncoderQSV
	case hwenc.H264AMF:
		return EncoderAMF
	case hwenc.H264NVENC:
		return EncoderNVENC
	case hwenc.H264VAAPI:
		return EncoderVAAPI
	default:
		return EncoderX264
	}
}

func (w *Worker) encoderArgs(vf string, videoRate string) []string {
	switch w.Encoder {
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
		return []string{"-vf", vf, "-c:v", "libx264", "-preset", "veryfast", "-b:v", videoRate, "-maxrate", videoRate, "-bufsize", "2M"}
	}
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

func trimErrorMessage(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		return s[:2000]
	}
	return s
}
