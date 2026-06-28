package handler

import (
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	if strings.HasPrefix(raw, "/uploads/") && h.App != nil && h.App.Config != nil {
		uploadDir := strings.TrimSpace(h.App.Config.Data.Upload)
		if uploadDir != "" {
			candidate := filepath.Join(uploadDir, filepath.FromSlash(strings.TrimPrefix(raw, "/uploads/")))
			if artworkFileReady(candidate) {
				return candidate
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
	var libraryID sql.NullInt64
	_ = h.App.DB.QueryRow(`SELECT library_id FROM music_album WHERE id = ?`, albumID).Scan(&libraryID)
	if libraryID.Int64 > 0 {
		h.scheduleLibraryPreviewRefresh(libraryID.Int64)
	}
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

// materializeAlbumArtwork resolves user-provided artwork (local path, /uploads URL, or http URL)
// into a local cache file when possible so ServeAlbumArtwork can serve it reliably.
func (h *Handler) materializeAlbumArtwork(albumID int64, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || albumID <= 0 {
		return ""
	}
	if local := h.existingArtworkFile(raw); local != "" {
		if local != raw && artworkFileReady(local) {
			if cached := h.copyToAlbumArtworkCache(albumID, local); cached != "" {
				return cached
			}
		}
		return local
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		if cached := h.downloadAlbumArtworkURL(albumID, raw); cached != "" {
			return cached
		}
		return raw
	}
	return raw
}

func (h *Handler) copyToAlbumArtworkCache(albumID int64, src string) string {
	outFile := h.albumArtworkCacheFile(albumID)
	if outFile == "" || !artworkFileReady(src) {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return ""
	}
	in, err := os.Open(src)
	if err != nil {
		return ""
	}
	defer in.Close()
	out, err := os.Create(outFile)
	if err != nil {
		return ""
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return ""
	}
	return outFile
}

func (h *Handler) downloadAlbumArtworkURL(albumID int64, u string) string {
	outFile := h.albumArtworkCacheFile(albumID)
	if outFile == "" {
		return ""
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return ""
	}
	out, err := os.Create(outFile)
	if err != nil {
		return ""
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return ""
	}
	return outFile
}

func (h *Handler) artistArtworkCacheFile(artistID int64) string {
	if h == nil || h.App == nil || h.App.Config == nil || artistID <= 0 {
		return ""
	}
	preview := strings.TrimSpace(h.App.Config.Data.Preview)
	if preview == "" {
		return ""
	}
	return filepath.Join(preview, "music-artist", strconv.FormatInt(artistID, 10), "artwork.jpg")
}

func (h *Handler) materializeArtistArtwork(artistID int64, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || artistID <= 0 {
		return ""
	}
	if local := h.existingArtworkFile(raw); local != "" {
		if local != raw && artworkFileReady(local) {
			if cached := h.copyToArtistArtworkCache(artistID, local); cached != "" {
				return cached
			}
		}
		return local
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		if cached := h.downloadArtistArtworkURL(artistID, raw); cached != "" {
			return cached
		}
		return raw
	}
	return raw
}

func (h *Handler) copyToArtistArtworkCache(artistID int64, src string) string {
	outFile := h.artistArtworkCacheFile(artistID)
	if outFile == "" || !artworkFileReady(src) {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return ""
	}
	in, err := os.Open(src)
	if err != nil {
		return ""
	}
	defer in.Close()
	out, err := os.Create(outFile)
	if err != nil {
		return ""
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return ""
	}
	return outFile
}

func (h *Handler) downloadArtistArtworkURL(artistID int64, u string) string {
	outFile := h.artistArtworkCacheFile(artistID)
	if outFile == "" {
		return ""
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return ""
	}
	out, err := os.Create(outFile)
	if err != nil {
		return ""
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return ""
	}
	return outFile
}

// ServeArtistArtwork serves artist portrait from cache path or stored artwork_path.
func (h *Handler) ServeArtistArtwork(c *gin.Context) {
	artistID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || artistID <= 0 {
		c.Status(http.StatusBadRequest)
		return
	}
	var libID int64
	var artworkPath sql.NullString
	if err := h.App.DB.QueryRow(`SELECT library_id, artwork_path FROM music_artist WHERE id = ?`, artistID).Scan(&libID, &artworkPath); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	stored := strings.TrimSpace(artworkPath.String)
	if strings.HasPrefix(stored, "http://") || strings.HasPrefix(stored, "https://") {
		c.Redirect(http.StatusFound, stored)
		return
	}
	path := h.existingArtworkFile(stored)
	if path == "" {
		path = h.artistArtworkCacheFile(artistID)
		if !artworkFileReady(path) {
			c.Status(http.StatusNotFound)
			return
		}
	}
	h.deliverAlbumArtwork(c, path, 0)
}
