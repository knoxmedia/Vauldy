package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"knox-media/internal/postingest"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
	"knox-media/pkg/ffprobe"
)

// applyScrapeLocalImages schedules the unified poster adapter when providers returned no poster.
// It waits briefly for compatibility with synchronous scrape responses, but never captures locally here.
func (h *Handler) applyScrapeLocalImages(ctx context.Context, mediaID, _ int64, fileType string, cfg scraper.Config, res *scraper.ScrapeResult, wait bool) error {
	if h == nil || res == nil || mediaID <= 0 || !strings.EqualFold(fileType, "video") || scraper.HasScrapePoster(res) {
		return nil
	}
	if !imageSourceEnabled(cfg, "embedded") && !imageSourceEnabled(cfg, "screen_grabber") {
		return nil
	}
	if err := h.enqueueScrapePosterFallback(ctx, mediaID); err != nil {
		return err
	}
	if !wait {
		return nil
	}
	posterURL, ok, err := h.waitPosterResult(ctx, mediaID, 2*time.Second)
	if err != nil {
		return err
	}
	if ok {
		res.Poster = posterURL
		if res.Extra == nil {
			res.Extra = map[string]any{}
		}
		if strings.TrimSpace(fmt.Sprint(res.Extra["poster"])) == "" {
			res.Extra["poster"] = posterURL
		}
	}
	return nil
}

func (h *Handler) applyManualMatchLocalImages(ctx context.Context, mediaID, libraryID int64, fileType string, cfg scraper.Config, res *scraper.ScrapeResult) error {
	return h.applyScrapeLocalImages(ctx, mediaID, libraryID, fileType, cfg, res, true)
}

func (h *Handler) enqueueScrapePosterFallback(ctx context.Context, mediaID int64) error {
	if h == nil || h.Queue == nil {
		return fmt.Errorf("poster fallback queue is not configured")
	}
	_, err := h.Queue.Enqueue(ctx, mediaID, nil, postingest.TaskPoster)
	return err
}

func (h *Handler) waitPosterResult(ctx context.Context, mediaID int64, maxWait time.Duration) (string, bool, error) {
	if h == nil || h.App == nil || h.App.DB == nil {
		return "", false, fmt.Errorf("poster result database is not configured")
	}
	if mediaID <= 0 {
		return "", false, fmt.Errorf("invalid media id")
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if maxWait <= 0 {
		return "", false, nil
	}
	if maxWait > 2*time.Second {
		maxWait = 2 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()
	lookup := func() (string, bool, error) {
		var poster string
		err := h.App.DB.QueryRowContext(waitCtx, `SELECT COALESCE(NULLIF(TRIM(json_extract(meta_json,'$.scrape.poster')),''),NULLIF(TRIM(json_extract(meta_json,'$.scrape.extra.poster')),''),'') FROM media WHERE id=?`, mediaID).Scan(&poster)
		if err != nil {
			return "", false, err
		}
		if poster = strings.TrimSpace(poster); poster != "" {
			return poster, true, nil
		}
		upload := ""
		if h.App.Config != nil {
			upload = strings.TrimSpace(h.App.Config.Data.Upload)
		}
		if upload != "" {
			plain := filepath.Join(upload, "posters", fmt.Sprintf("%d.jpg", mediaID))
			if exists, err := nonEmptyPosterFile(plain); err != nil {
				return "", false, err
			} else if exists {
				return storage.PlainPosterURL(mediaID), true, nil
			}
		}
		var encPath string
		err = h.App.DB.QueryRowContext(waitCtx, `SELECT enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind='poster' AND logical_name='poster.jpg'`, mediaID).Scan(&encPath)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
		if err == nil {
			if exists, statErr := nonEmptyPosterFile(encPath); statErr != nil {
				return "", false, statErr
			} else if exists {
				return storage.DerivedPosterAPIPath(mediaID), true, nil
			}
		}
		return "", false, nil
	}
	if poster, ok, err := lookup(); err != nil || ok {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return "", false, nil
		}
		return poster, ok, err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return "", false, err
			}
			return "", false, nil
		case <-ticker.C:
			if poster, ok, err := lookup(); err != nil || ok {
				if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
					return "", false, nil
				}
				return poster, ok, err
			}
		}
	}
}

func nonEmptyPosterFile(path string) (bool, error) {
	st, err := os.Stat(strings.TrimSpace(path))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !st.IsDir() && st.Size() > 0, nil
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

func (h *Handler) resolveMediaAbsolutePath(libraryID int64, filePath string) string {
	return storage.ResolveMediaAbsolutePath(h.App.DB, libraryID, filePath)
}

func (h *Handler) extractEmbeddedCover(ffprobePath, ffmpegPath string, mediaID int64, videoPath, outFile string) bool {
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
	}
	var out []byte
	var err error
	if storage.InputNeedsPipe(h.App.DB, mediaID, videoPath) {
		raw, cleanup, perr := storage.FFprobeOutput(h.App.DB, h.KeyVault, ffprobePath, mediaID, videoPath, 0, 0, args)
		if cleanup != nil {
			defer cleanup()
		}
		if perr != nil {
			return false
		}
		out = raw
	} else {
		out, err = ffprobe.Output(ffprobePath, append(args, videoPath), nil)
		if err != nil {
			return false
		}
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
		if h.runFFmpegPoster(ffmpegPath, mediaID, videoPath, outFile, []string{"-map", mapArg, "-frames:v", "1"}) {
			return true
		}
	}
	return false
}

func (h *Handler) runFFmpegPoster(ffmpegPath string, mediaID int64, videoPath, outFile string, extraArgs []string) bool {
	post := append(append([]string{}, extraArgs...), outFile)
	if _, err := storage.RunFFmpeg(context.Background(), h.App.DB, h.KeyVault, ffmpegPath, mediaID, videoPath, 0, 0, nil, post, ""); err != nil {
		log.Printf("ffmpeg poster %q: %v", videoPath, err)
		return false
	}
	info, err := os.Stat(outFile)
	return err == nil && info.Size() > 0
}
