package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"knox-media/internal/scraper"
)

// applyScrapeLocalImages fills poster from embedded cover or ffmpeg frame capture when configured
// and scrape providers did not already return a poster. screen_grabber is skipped once any poster exists.
func (h *Handler) applyScrapeLocalImages(mediaID, libraryID int64, fileType string, cfg scraper.Config, res *scraper.ScrapeResult) {
	if h == nil || res == nil || mediaID <= 0 || !strings.EqualFold(fileType, "video") {
		return
	}
	if scraper.HasScrapePoster(res) {
		return
	}
	if !imageSourceEnabled(cfg, "embedded") && !imageSourceEnabled(cfg, "screen_grabber") {
		return
	}
	posterURL, source := h.captureLocalPoster(mediaID, libraryID, cfg, false)
	if posterURL == "" {
		log.Printf("scrape local poster media=%d: capture failed (check ffmpeg/ffprobe and file_path)", mediaID)
		return
	}
	h.applyCapturedPoster(res, posterURL, source)
}

func (h *Handler) applyCapturedPoster(res *scraper.ScrapeResult, posterURL, source string) {
	res.Poster = posterURL
	if res.Extra == nil {
		res.Extra = map[string]any{}
	}
	if strings.TrimSpace(fmt.Sprint(res.Extra["poster"])) == "" {
		res.Extra["poster"] = posterURL
	}
	res.Extra["local_poster_source"] = source
}

func imageSourceEnabled(cfg scraper.Config, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, s := range cfg.ImageSources {
		if strings.ToLower(strings.TrimSpace(s)) == name {
			return true
		}
	}
	return false
}

// captureLocalPoster tries embedded cover first, then screen_grabber unless skipScreenGrabber is set.
func (h *Handler) captureLocalPoster(mediaID, libraryID int64, cfg scraper.Config, skipScreenGrabber bool) (posterURL, source string) {
	absPath, duration, err := h.mediaVideoPath(mediaID, libraryID)
	if err != nil || absPath == "" {
		return "", ""
	}
	ffmpegPath := strings.TrimSpace(h.App.Config.FFmpeg.FFmpegPath)
	ffprobePath := strings.TrimSpace(h.App.Config.FFmpeg.FFprobePath)
	uploadDir := strings.TrimSpace(h.App.Config.Data.Upload)
	if ffmpegPath == "" || uploadDir == "" {
		return "", ""
	}
	posterDir := filepath.Join(uploadDir, "posters")
	if err := os.MkdirAll(posterDir, 0o755); err != nil {
		return "", ""
	}
	posterFile := filepath.Join(posterDir, fmt.Sprintf("%d.jpg", mediaID))
	posterURL = "/uploads/posters/" + fmt.Sprintf("%d.jpg", mediaID)

	if imageSourceEnabled(cfg, "embedded") {
		if ffprobePath != "" && extractEmbeddedCover(ffprobePath, ffmpegPath, absPath, posterFile) {
			return posterURL, "embedded"
		}
	}
	if !skipScreenGrabber && imageSourceEnabled(cfg, "screen_grabber") {
		if extractFramePoster(ffmpegPath, absPath, posterFile, duration) {
			return posterURL, "screen_grabber"
		}
	}
	_ = os.Remove(posterFile)
	return "", ""
}

func (h *Handler) mediaVideoPath(mediaID, libraryID int64) (absPath string, durationSec int64, err error) {
	var filePath sql.NullString
	var duration sql.NullInt64
	if err = h.App.DB.QueryRow(
		`SELECT file_path, COALESCE(duration,0) FROM media WHERE id = ? LIMIT 1`,
		mediaID,
	).Scan(&filePath, &duration); err != nil {
		return "", 0, err
	}
	absPath = h.resolveMediaAbsolutePath(libraryID, filePath.String)
	if absPath == "" {
		return "", 0, fmt.Errorf("empty file path")
	}
	if _, statErr := os.Stat(absPath); statErr != nil {
		log.Printf("poster capture media=%d: file not accessible %q: %v", mediaID, absPath, statErr)
		return "", 0, statErr
	}
	return absPath, duration.Int64, nil
}

func (h *Handler) resolveMediaAbsolutePath(libraryID int64, filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return ""
	}
	if filepath.IsAbs(filePath) {
		return filepath.Clean(filePath)
	}
	if libraryID <= 0 {
		return filepath.Clean(filePath)
	}
	var libPath string
	if err := h.App.DB.QueryRow(`SELECT path FROM library WHERE id = ?`, libraryID).Scan(&libPath); err != nil {
		return filepath.Clean(filePath)
	}
	libPath = strings.TrimSpace(libPath)
	if libPath == "" {
		return filepath.Clean(filePath)
	}
	return filepath.Clean(filepath.Join(libPath, filepath.FromSlash(filePath)))
}

func extractEmbeddedCover(ffprobePath, ffmpegPath, videoPath, outFile string) bool {
	type disposition struct {
		AttachedPic int `json:"attached_pic"`
	}
	type stream struct {
		CodecType   string       `json:"codec_type"`
		Index       int          `json:"index"`
		Disposition *disposition `json:"disposition"`
	}
	type probeOut struct {
		Streams []stream `json:"streams"`
	}
	args := []string{
		"-v", "error",
		"-select_streams", "v",
		"-show_entries", "stream=index,codec_type:stream_disposition=attached_pic",
		"-of", "json",
		videoPath,
	}
	out, err := exec.Command(ffprobePath, args...).Output()
	if err != nil {
		return false
	}
	var pr probeOut
	if json.Unmarshal(out, &pr) != nil {
		return false
	}
	for _, st := range pr.Streams {
		if st.CodecType != "video" || st.Disposition == nil || st.Disposition.AttachedPic != 1 {
			continue
		}
		mapArg := fmt.Sprintf("0:%d", st.Index)
		if runFFmpegPoster(ffmpegPath, videoPath, outFile, []string{"-map", mapArg, "-frames:v", "1"}) {
			return true
		}
	}
	return false
}

func extractFramePoster(ffmpegPath, videoPath, outFile string, durationSec int64) bool {
	snapSec := posterSnapSecond(durationSec)
	args := []string{
		"-ss", strconv.Itoa(snapSec),
		"-i", videoPath,
		"-frames:v", "1",
		"-q:v", "3",
	}
	return runFFmpegPoster(ffmpegPath, videoPath, outFile, args)
}

func posterSnapSecond(durationSec int64) int {
	snapSec := 10
	if durationSec > 0 {
		sec := int(durationSec / 5)
		if sec < 10 {
			sec = 10
		}
		if sec > 180 {
			sec = 180
		}
		snapSec = sec
	}
	return snapSec
}

func runFFmpegPoster(ffmpegPath, videoPath, outFile string, extraArgs []string) bool {
	args := []string{"-y"}
	args = append(args, extraArgs...)
	args = append(args, outFile)
	cmd := exec.Command(ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Printf("ffmpeg poster %q: %v (%s)", videoPath, err, strings.TrimSpace(stderr.String()))
		return false
	}
	info, err := os.Stat(outFile)
	return err == nil && info.Size() > 0
}

// capturePosterFromVideo extracts a local poster and stores it in meta_json (scan/upload ingest).
func (h *Handler) capturePosterFromVideo(mediaID int64, fileType string) {
	if h == nil || mediaID <= 0 || fileType != "video" {
		return
	}
	var libraryID int64
	var metaRaw sql.NullString
	_ = h.App.DB.QueryRow(`SELECT library_id, COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&libraryID, &metaRaw)
	cfg := h.readLibraryScrapeConfig(libraryID)
	skipGrab := scraper.HasScrapePosterFromMeta(metaRaw.String)
	posterURL, source := h.captureLocalPoster(mediaID, libraryID, cfg, skipGrab)
	if posterURL == "" {
		return
	}
	var root map[string]any
	if strings.TrimSpace(metaRaw.String) != "" {
		_ = json.Unmarshal([]byte(metaRaw.String), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	scrape, _ := root["scrape"].(map[string]any)
	if scrape == nil {
		scrape = map[string]any{}
	}
	if strings.TrimSpace(fmt.Sprint(scrape["poster"])) == "" {
		scrape["poster"] = posterURL
	}
	extra, _ := scrape["extra"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
	}
	if strings.TrimSpace(fmt.Sprint(extra["poster"])) == "" {
		extra["poster"] = posterURL
	}
	extra["local_poster_source"] = source
	scrape["extra"] = extra
	root["scrape"] = scrape
	merged, _ := json.Marshal(root)
	_, _ = h.App.DB.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, string(merged), mediaID)
}
