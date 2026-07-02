package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	kcrypto "knox-media/internal/crypto"
)

// ResolveMediaAbsolutePath maps a catalog file_path to an absolute filesystem path.
func ResolveMediaAbsolutePath(db *sql.DB, libraryID int64, filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return ""
	}
	if filepath.IsAbs(filePath) {
		return filepath.Clean(filePath)
	}
	if libraryID <= 0 || db == nil {
		return filepath.Clean(filePath)
	}
	var libPath string
	if err := db.QueryRow(`SELECT path FROM library WHERE id = ?`, libraryID).Scan(&libPath); err != nil {
		return filepath.Clean(filePath)
	}
	libPath = strings.TrimSpace(libPath)
	if libPath == "" {
		return filepath.Clean(filePath)
	}
	return filepath.Clean(filepath.Join(libPath, filepath.FromSlash(filePath)))
}

// catalogUsesEncInput reports whether ffmpeg should treat the path as Knox encrypted input.
func catalogUsesEncInput(db *sql.DB, mediaID int64, abs string) bool {
	abs = strings.TrimSpace(abs)
	if abs == "" {
		return false
	}
	if kcrypto.IsEncFile(abs) {
		return true
	}
	if !strings.HasSuffix(strings.ToLower(filepath.Clean(abs)), ".enc") {
		return false
	}
	if db == nil || mediaID <= 0 {
		return false
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(1) FROM media_encrypted_assets WHERE media_id = ? AND status = 'encrypted'`, mediaID).Scan(&n)
	return n > 0
}

// PreferredFFmpegPath picks the best readable source for ffmpeg/ffprobe.
// When the catalog points at Knox .enc but the original plaintext still exists, prefer plaintext.
func PreferredFFmpegPath(db *sql.DB, mediaID, libraryID int64, catalogPath string) string {
	abs := ResolveMediaAbsolutePath(db, libraryID, catalogPath)
	if abs == "" {
		return ""
	}
	if !catalogUsesEncInput(db, mediaID, abs) {
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
		return ""
	}
	if db != nil && mediaID > 0 {
		var plainPath sql.NullString
		_ = db.QueryRow(`
			SELECT plain_path FROM media_encrypted_assets
			WHERE media_id = ? AND status = 'encrypted'
		`, mediaID).Scan(&plainPath)
		if p := strings.TrimSpace(plainPath.String); p != "" {
			if _, err := os.Stat(p); err == nil {
				return filepath.Clean(p)
			}
		}
	}
	if _, err := os.Stat(abs); err == nil {
		return abs
	}
	return ""
}

// PosterSeekPreInput returns ffmpeg args placed before -i for poster frame capture.
// Knox encrypted inputs cannot use -ss before pipe decrypt.
func PosterSeekPreInput(snapSec int, path string) []string {
	if snapSec <= 0 || catalogUsesEncInput(nil, 0, path) || strings.HasSuffix(strings.ToLower(filepath.Clean(strings.TrimSpace(path))), ".enc") {
		return nil
	}
	return []string{"-ss", strconv.Itoa(snapSec)}
}
