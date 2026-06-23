package handler

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/metadatalib"
	"knox-media/internal/musicstore"
	"knox-media/internal/storage"
)

func (h *Handler) resolveAlbumArtworkPath(albumID, libID int64, stored sql.NullString) (path string, serveMediaID int64) {
	if h == nil || albumID <= 0 {
		return "", 0
	}
	if p := h.existingArtworkFile(stored.String); p != "" {
		return p, 0
	}
	rows, err := h.App.DB.Query(`
		SELECT mt.media_id, COALESCE(m.file_path, '')
		FROM music_track mt
		JOIN media m ON m.id = mt.media_id AND m.status = 'active'
		WHERE mt.album_id = ?
		ORDER BY mt.sort_order ASC, mt.track_number ASC, mt.media_id ASC
	`, albumID)
	if err != nil {
		return "", 0
	}
	defer rows.Close()

	uploadDir := ""
	if h.App != nil && h.App.Config != nil {
		uploadDir = strings.TrimSpace(h.App.Config.Data.Upload)
	}
	for rows.Next() {
		var mediaID int64
		var filePath string
		if rows.Scan(&mediaID, &filePath) != nil || mediaID <= 0 {
			continue
		}
		for _, candidate := range albumArtworkCandidatePaths(h.App.DB, mediaID, libID, filePath) {
			if artworkFileReady(candidate) {
				return candidate, 0
			}
		}
		if poster := storage.ResolvePosterServePath(h.App.DB, uploadDir, mediaID); poster != "" {
			return poster, mediaID
		}
		inputPath := storage.PreferredFFmpegPath(h.App.DB, mediaID, libID, filePath)
		if inputPath == "" {
			inputPath = strings.TrimSpace(filePath)
		}
		if cached := h.extractAndCacheAlbumArtwork(albumID, mediaID, inputPath); cached != "" {
			return cached, 0
		}
	}
	return "", 0
}

func albumArtworkCandidatePaths(db *sql.DB, mediaID, libID int64, catalogPath string) []string {
	bases := make([]string, 0, 2)
	if abs := storage.PreferredFFmpegPath(db, mediaID, libID, catalogPath); abs != "" {
		bases = append(bases, abs)
	}
	var plainPath sql.NullString
	_ = db.QueryRow(`
		SELECT plain_path FROM media_encrypted_assets
		WHERE media_id = ? AND status = 'encrypted'
	`, mediaID).Scan(&plainPath)
	if p := strings.TrimSpace(plainPath.String); p != "" {
		bases = append(bases, p)
	}
	if len(bases) == 0 {
		if p := strings.TrimSpace(catalogPath); p != "" {
			bases = append(bases, p)
		}
	}
	seen := make(map[string]struct{}, 8)
	var out []string
	for _, base := range bases {
		for _, candidate := range musicstore.AlbumArtworkCandidates(base) {
			candidate = filepath.Clean(candidate)
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out
}

func (h *Handler) existingArtworkFile(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if metadatalib.IsLocalMetadataURL(raw) {
		if h.App == nil || h.App.Config == nil {
			return ""
		}
		if id, ok := metadatalib.ParseMediaIDFromPublicURL(raw); ok {
			trim := strings.TrimPrefix(raw, metadatalib.PublicURLPrefix+"/")
			parts := strings.Split(trim, "/")
			if len(parts) >= 4 {
				name := parts[len(parts)-1]
				candidate := filepath.Join(metadatalib.MediaDir(h.App.Config.Data.MetadataLibrary, id), name)
				if artworkFileReady(candidate) {
					return candidate
				}
			}
		}
	}
	if artworkFileReady(raw) {
		return raw
	}
	return ""
}

func artworkFileReady(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func (h *Handler) albumArtworkCacheFile(albumID int64) string {
	if h == nil || h.App == nil || h.App.Config == nil || albumID <= 0 {
		return ""
	}
	preview := strings.TrimSpace(h.App.Config.Data.Preview)
	if preview == "" {
		return ""
	}
	return filepath.Join(preview, "music", strconv.FormatInt(albumID, 10), "artwork.jpg")
}

func (h *Handler) extractAndCacheAlbumArtwork(albumID, mediaID int64, inputPath string) string {
	inputPath = strings.TrimSpace(inputPath)
	if inputPath == "" || h.App == nil || h.App.Config == nil {
		return ""
	}
	outFile := h.albumArtworkCacheFile(albumID)
	if outFile == "" {
		return ""
	}
	if artworkFileReady(outFile) {
		return outFile
	}
	ffmpegPath := strings.TrimSpace(h.App.Config.FFmpeg.FFmpegPath)
	ffprobePath := strings.TrimSpace(h.App.Config.FFmpeg.FFprobePath)
	if ffmpegPath == "" || ffprobePath == "" {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return ""
	}
	if !h.extractEmbeddedCover(ffprobePath, ffmpegPath, mediaID, inputPath, outFile) {
		return ""
	}
	_, _ = h.App.DB.Exec(`UPDATE music_album SET artwork_path = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, outFile, albumID)
	return outFile
}

func (h *Handler) deliverAlbumArtwork(c *gin.Context, path string, serveMediaID int64) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		c.Status(http.StatusNotFound)
		return
	}
	if serveMediaID > 0 && (kcrypto.IsEncFile(path) || storage.NeedsDerivedEncryption(h.App.DB, serveMediaID)) {
		h.serveDerivedAsset(c, serveMediaID, path, "image/jpeg")
		return
	}
	if !artworkFileReady(path) {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", "image/jpeg")
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(path)
}
