package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	derivedPosterKind        = "poster"
	derivedPosterLogicalName = "poster.jpg"
)

// DerivedPosterAPIPath is the authenticated URL stored in meta_json for encrypted local posters.
func DerivedPosterAPIPath(mediaID int64) string {
	return fmt.Sprintf("/api/v1/media/%d/poster.jpg", mediaID)
}

// PlainPosterURL is the legacy static URL for non-encrypted libraries.
func PlainPosterURL(mediaID int64) string {
	return fmt.Sprintf("/uploads/posters/%d.jpg", mediaID)
}

// FinalizeLocalPoster persists a captured JPEG poster, encrypting when the library requires it.
func FinalizeLocalPoster(ctx context.Context, derived *DerivedAssetStore, db *sql.DB, mediaID int64, plainPosterFile string) (posterURL string, err error) {
	if mediaID <= 0 {
		return "", fmt.Errorf("invalid media id")
	}
	plainPosterFile = filepath.Clean(strings.TrimSpace(plainPosterFile))
	st, err := os.Stat(plainPosterFile)
	if err != nil || st.IsDir() || st.Size() == 0 {
		return "", fmt.Errorf("poster file missing")
	}
	if derived != nil && NeedsDerivedEncryption(db, mediaID) {
		if _, err := derived.FinalizePath(ctx, mediaID, derivedPosterKind, derivedPosterLogicalName, plainPosterFile); err != nil {
			return "", err
		}
		_ = os.Remove(plainPosterFile)
		return DerivedPosterAPIPath(mediaID), nil
	}
	return PlainPosterURL(mediaID), nil
}

// ResolvePosterServePath returns the filesystem path for poster delivery (encrypted or plaintext).
func ResolvePosterServePath(db *sql.DB, uploadDir string, mediaID int64) string {
	if enc, ok := LookupEncPath(db, mediaID, derivedPosterKind, derivedPosterLogicalName); ok {
		return enc
	}
	if uploadDir != "" {
		plain := filepath.Join(uploadDir, "posters", fmt.Sprintf("%d.jpg", mediaID))
		if st, err := os.Stat(plain); err == nil && !st.IsDir() && st.Size() > 0 {
			return plain
		}
	}
	return ""
}
