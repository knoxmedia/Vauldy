package doccover

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"knox-media/internal/doctrans"
)

func renderPageCover(ctx context.Context, opts Options, srcPath, dstPath string) error {
	if err := renderJPEG(ctx, opts.FFmpegPath, srcPath, dstPath); err == nil {
		return nil
	}
	if !docTransEnabled(opts.DocTrans) {
		return fmt.Errorf("pdf cover: ffmpeg failed and document conversion disabled")
	}
	if err := doctrans.ExportDrawJPEG(ctx, opts.MediaRoot, opts.DocTrans, srcPath, dstPath); err != nil {
		return err
	}
	if st, err := os.Stat(dstPath); err != nil || st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("pdf cover: empty output")
	}
	// Normalize dimensions when ffmpeg can read JPEG but not PDF.
	if opts.FFmpegPath != "" {
		tmp := dstPath + ".scaled.jpg"
		if err := renderJPEG(ctx, opts.FFmpegPath, dstPath, tmp); err == nil {
			if st, err := os.Stat(tmp); err == nil && st.Size() > 0 {
				_ = os.Rename(tmp, dstPath)
				return nil
			}
			_ = os.Remove(tmp)
		}
	}
	return nil
}

func renderJPEG(ctx context.Context, ffmpegPath, srcPath, dstPath string) error {
	ffmpegPath = strings.TrimSpace(ffmpegPath)
	if ffmpegPath == "" {
		return fmt.Errorf("ffmpeg path empty")
	}
	scale := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", CoverMaxEdge, CoverMaxEdge)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", srcPath,
		"-vf", scale,
		"-frames:v", "1",
		"-q:v", "4",
		dstPath,
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg cover: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if st, err := os.Stat(dstPath); err != nil || st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("ffmpeg cover: empty output")
	}
	return nil
}
