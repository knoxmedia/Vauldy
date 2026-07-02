package doccover

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultCoverFailCooldown = 7 * 24 * time.Hour

func coverSkipPath(previewDir string, mediaID int64) string {
	return filepath.Join(previewDir, "documents", fmt.Sprintf("%d", mediaID), ".cover-failed")
}

// MarkCoverFailed records a cover generation failure to avoid immediate retries.
func MarkCoverFailed(previewDir string, mediaID int64, err error) {
	if mediaID <= 0 || err == nil {
		return
	}
	p := coverSkipPath(previewDir, mediaID)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_ = os.WriteFile(p, []byte(msg), 0o644)
}

// CoverRetryBlocked reports whether cover generation should be skipped due to a recent failure.
func CoverRetryBlocked(previewDir string, mediaID int64) bool {
	if mediaID <= 0 {
		return false
	}
	p := coverSkipPath(previewDir, mediaID)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	return time.Since(st.ModTime()) < defaultCoverFailCooldown
}

// NeedsCoverWork reports whether enqueue/generation should run for a document.
func NeedsCoverWork(db *sql.DB, previewDir, derivedBaseDir string, mediaID int64, sourceMtime int64) bool {
	if mediaID <= 0 {
		return false
	}
	if CoverRetryBlocked(previewDir, mediaID) {
		return false
	}
	return !CachedCover(db, previewDir, derivedBaseDir, mediaID, sourceMtime)
}
