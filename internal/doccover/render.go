package doccover

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/doctrans"
	"knox-media/internal/storage"
)

func renderPageCover(ctx context.Context, opts Options, mediaID int64, srcPath, dstPath string) error {
	if err := renderJPEG(ctx, opts, mediaID, srcPath, dstPath); err == nil {
		return nil
	}
	if !docTransEnabled(opts.DocTrans) {
		return fmt.Errorf("pdf cover: ffmpeg failed and document conversion disabled")
	}
	if err := doctrans.ExportPageJPEG(ctx, opts.MediaRoot, opts.DocTrans, srcPath, dstPath); err != nil {
		return err
	}
	if st, err := os.Stat(dstPath); err != nil || st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("pdf cover: empty output")
	}
	// Normalize dimensions when ffmpeg can read JPEG but not PDF.
	if opts.FFmpegPath != "" {
		tmp := dstPath + ".scaled.jpg"
		if err := renderJPEG(ctx, opts, mediaID, dstPath, tmp); err == nil {
			if st, err := os.Stat(tmp); err == nil && st.Size() > 0 {
				_ = os.Rename(tmp, dstPath)
				return nil
			}
			_ = os.Remove(tmp)
		}
	}
	return nil
}

func renderJPEG(ctx context.Context, opts Options, mediaID int64, srcPath, dstPath string) error {
	ffmpegPath := strings.TrimSpace(opts.FFmpegPath)
	if ffmpegPath == "" {
		return fmt.Errorf("ffmpeg path empty")
	}
	scale := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", CoverMaxEdge, CoverMaxEdge)
	post := []string{
		"-hide_banner", "-loglevel", "error",
		"-vf", scale,
		"-frames:v", "1",
		"-q:v", "4",
		dstPath,
	}
	if !storage.InputNeedsPipe(opts.DB, mediaID, srcPath) {
		if _, err := os.Stat(srcPath); err != nil {
			return fmt.Errorf("source missing: %w", err)
		}
	}
	var pre []string
	if strings.EqualFold(filepath.Ext(srcPath), ".pdf") {
		pre = []string{"-f", "pdf"}
	}
	if _, err := storage.RunFFmpeg(ctx, opts.DB, opts.Vault, ffmpegPath, mediaID, srcPath, 0, 0, pre, post, ""); err != nil {
		return fmt.Errorf("ffmpeg cover: %w: %s", err, strings.TrimSpace(err.Error()))
	}
	if st, err := os.Stat(dstPath); err != nil || st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("ffmpeg cover: empty output")
	}
	return nil
}
