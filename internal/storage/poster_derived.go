package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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

// ImmutablePlainPosterURL identifies one generation-scoped staged poster.
func ImmutablePlainPosterURL(generation int64, stageID string) string {
	if generation <= 0 || strings.TrimSpace(stageID) == "" {
		return ""
	}
	return fmt.Sprintf("/uploads/posters/generation-%d/%s/poster.jpg", generation, stageID)
}

func PosterObjectPath(uploadDir, hash, ext string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) != 64 {
		return ""
	}
	for _, c := range hash {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return ""
		}
	}
	if ext != ".jpg" && ext != ".enc" {
		return ""
	}
	return filepath.Join(uploadDir, "posters", "objects", "sha256", hash[:2], hash+ext)
}
func PosterObjectURL(hash string) string {
	p := PosterObjectPath("", hash, ".jpg")
	if p == "" {
		return ""
	}
	return "/uploads/" + filepath.ToSlash(strings.TrimPrefix(p, string(filepath.Separator)))
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
		var raw string
		if db != nil && db.QueryRow(`SELECT COALESCE(meta_json,'') FROM media WHERE id=?`, mediaID).Scan(&raw) == nil {
			var meta struct {
				Scrape struct {
					Poster string `json:"poster"`
					Extra  struct {
						Poster string `json:"poster"`
					} `json:"extra"`
				} `json:"scrape"`
			}
			if json.Unmarshal([]byte(raw), &meta) == nil {
				url := strings.TrimSpace(meta.Scrape.Poster)
				if url == "" {
					url = strings.TrimSpace(meta.Scrape.Extra.Poster)
				}
				const prefix = "/uploads/"
				if strings.HasPrefix(url, prefix) {
					rel := filepath.FromSlash(strings.TrimPrefix(url, prefix))
					root, _ := filepath.Abs(uploadDir)
					candidate, _ := filepath.Abs(filepath.Join(root, rel))
					if withinRoot(root, candidate) {
						if st, err := os.Stat(candidate); err == nil && !st.IsDir() && st.Size() > 0 {
							return candidate
						}
					}
				}
			}
		}
		plain := filepath.Join(uploadDir, "posters", fmt.Sprintf("%d.jpg", mediaID))
		if st, err := os.Stat(plain); err == nil && !st.IsDir() && st.Size() > 0 {
			return plain
		}
	}
	return ""
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
