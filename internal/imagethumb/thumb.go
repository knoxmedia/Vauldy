package imagethumb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/keystore"
	"knox-media/internal/storage"
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
// When the catalog path is Knox .enc, ffmpeg reads decrypted bytes via pipe:0.
func Ensure(ctx context.Context, db *sql.DB, vault *keystore.Vault, ffmpegPath, srcPath, baseDir string, mediaID int64) (Paths, error) {
	out := ExpectedPaths(baseDir, mediaID)
	if ffmpegPath == "" {
		return out, fmt.Errorf("ffmpeg path empty")
	}
	srcPath = strings.TrimSpace(srcPath)
	if srcPath == "" {
		return out, fmt.Errorf("source path empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(filepath.Dir(out.Thumb), 0o755); err != nil {
		return out, err
	}
	if err := render(ctx, db, vault, ffmpegPath, srcPath, mediaID, out.Thumb, ThumbMaxEdge); err != nil {
		return out, err
	}
	if err := render(ctx, db, vault, ffmpegPath, srcPath, mediaID, out.Medium, MediumMaxEdge); err != nil {
		return out, err
	}
	return out, nil
}

func render(ctx context.Context, db *sql.DB, vault *keystore.Vault, ffmpegPath, srcPath string, mediaID int64, dstPath string, maxEdge int) error {
	if st, err := os.Stat(dstPath); err == nil && !st.IsDir() && st.Size() > 0 {
		return nil
	}
	if !storage.InputNeedsPipe(db, mediaID, srcPath) {
		if _, err := os.Stat(srcPath); err != nil {
			return fmt.Errorf("source missing: %w", err)
		}
	}
	scale := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", maxEdge, maxEdge)
	post := []string{
		"-hide_banner", "-loglevel", "error",
		"-vf", scale,
		"-q:v", "4",
		dstPath,
	}
	if _, err := storage.RunFFmpeg(ctx, db, vault, ffmpegPath, mediaID, srcPath, 0, 0, nil, post, ""); err != nil {
		return fmt.Errorf("ffmpeg thumb: %w", err)
	}
	return nil
}
