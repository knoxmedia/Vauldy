package imagethumb

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	ThumbMaxEdge  = 480
	MediumMaxEdge = 1920
)

// Paths holds generated cache file paths for a media item.
type Paths struct {
	Thumb  string
	Medium string
}

// DirForMedia returns the cache directory for one media id.
func DirForMedia(baseDir string, mediaID int64) string {
	return filepath.Join(baseDir, fmt.Sprintf("%d", mediaID))
}

// ExpectedPaths returns thumb/medium paths without creating files.
func ExpectedPaths(baseDir string, mediaID int64) Paths {
	dir := DirForMedia(baseDir, mediaID)
	return Paths{
		Thumb:  filepath.Join(dir, "thumb.jpg"),
		Medium: filepath.Join(dir, "medium.jpg"),
	}
}

// Ensure generates thumb (480px) and medium (1920px) JPEG variants via ffmpeg.
func Ensure(ffmpegPath, srcPath, baseDir string, mediaID int64) (Paths, error) {
	out := ExpectedPaths(baseDir, mediaID)
	if ffmpegPath == "" {
		return out, fmt.Errorf("ffmpeg path empty")
	}
	srcPath = strings.TrimSpace(srcPath)
	if srcPath == "" {
		return out, fmt.Errorf("source path empty")
	}
	if err := os.MkdirAll(filepath.Dir(out.Thumb), 0o755); err != nil {
		return out, err
	}
	if err := render(ffmpegPath, srcPath, out.Thumb, ThumbMaxEdge); err != nil {
		return out, err
	}
	if err := render(ffmpegPath, srcPath, out.Medium, MediumMaxEdge); err != nil {
		return out, err
	}
	return out, nil
}

func render(ffmpegPath, srcPath, dstPath string, maxEdge int) error {
	if st, err := os.Stat(dstPath); err == nil && !st.IsDir() && st.Size() > 0 {
		return nil
	}
	// Avoid min(iw,N) expressions — commas break ffmpeg filter parsing on Windows.
	scale := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", maxEdge, maxEdge)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", srcPath,
		"-vf", scale,
		"-q:v", "4",
		dstPath,
	}
	cmd := exec.Command(ffmpegPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg thumb: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
