package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	models "knox-media/internal/model"
	"knox-media/internal/storage"
)

// EnqueueAudioTrackExtraction creates or resets an atrack_task for a media item.
func (h *Handler) EnqueueAudioTrackExtraction(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if h.AtrackWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "atrack worker disabled"})
		return
	}

	var fileID, filePath sql.NullString
	if err := h.App.DB.QueryRow(
		`SELECT COALESCE(file_id,''), file_path FROM media WHERE id = ? LIMIT 1`, mediaID,
	).Scan(&fileID, &filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}

	h.AtrackWorker.EnqueueRetry(mediaID)
	go func() {
		err := h.AtrackWorker.Run(context.Background(), mediaID, filePath.String)
		if err == nil && h.Instant != nil && fileID.String != "" && h.App.Config.HLSMultiAudioEnabled() {
			setAudioPlaylistsFromDir(h, mediaID, fileID.String)
		}
	}()

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListAudioTrackTasks returns all atrack_task rows.
func (h *Handler) ListAudioTrackTasks(c *gin.Context) {
	rows, err := h.App.DB.Query(`
		SELECT t.id, t.media_id, COALESCE(m.title,''), COALESCE(m.file_path,''), t.status, COALESCE(t.output_dir,''), COALESCE(t.error_message,''), t.created_at, t.updated_at
		FROM atrack_task t
		LEFT JOIN media m ON m.id = t.media_id
		ORDER BY t.id DESC
		LIMIT 200
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	items := make([]gin.H, 0)
	for rows.Next() {
		var id, mediaID sql.NullInt64
		var title, filePath, status, outputDir, errMsg, createdAt, updatedAt sql.NullString
		if rows.Scan(&id, &mediaID, &title, &filePath, &status, &outputDir, &errMsg, &createdAt, &updatedAt) != nil {
			continue
		}
		items = append(items, gin.H{
			"id":            id.Int64,
			"media_id":      mediaID.Int64,
			"title":         title.String,
			"file_path":     filePath.String,
			"status":        status.String,
			"output_dir":    outputDir.String,
			"error_message": errMsg.String,
			"created_at":    createdAt.String,
			"updated_at":    updatedAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// RetryAudioTrackTask re-enqueues a failed or done atrack_task.
func (h *Handler) RetryAudioTrackTask(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("mediaId"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if h.AtrackWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "atrack worker disabled"})
		return
	}

	var fileID, filePath sql.NullString
	if err := h.App.DB.QueryRow(
		`SELECT COALESCE(file_id,''), file_path FROM media WHERE id = ? LIMIT 1`, mediaID,
	).Scan(&fileID, &filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}

	h.AtrackWorker.EnqueueRetry(mediaID)
	go func() {
		err := h.AtrackWorker.Run(context.Background(), mediaID, filePath.String)
		if err == nil && h.Instant != nil && fileID.String != "" && h.App.Config.HLSMultiAudioEnabled() {
			setAudioPlaylistsFromDir(h, mediaID, fileID.String)
		}
	}()

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// setAudioPlaylistsFromDir scans the atrack output directory for per-stream HLS playlists
// and publishes them to Redis so the scheduler can emit EXT-X-MEDIA groups.
func setAudioPlaylistsFromDir(h *Handler, mediaID int64, fileID string) {
	outDir := filepath.Join(h.App.Config.Data.ATracks, strconv.FormatInt(mediaID, 10))
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return
	}

	var playlists []models.AudioPlaylistInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		streamIdx, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		playlistFile := h.atrackAssetPath(mediaID, e.Name(), "index.m3u8", "atrack_playlist")
		if _, err := os.Stat(playlistFile); err != nil {
			continue
		}
		// Try to read language from a metadata file, default to stream index.
		lang := fmt.Sprintf("Track %d", streamIdx)
		metaFile := h.atrackAssetPath(mediaID, e.Name(), "meta.json", "atrack_meta")
		if data, err := h.readAtrackMeta(mediaID, metaFile); err == nil {
			var m struct {
				Language string `json:"language"`
				Codec    string `json:"codec"`
			}
			if json.Unmarshal(data, &m) == nil {
				if m.Language != "" {
					lang = m.Language
				}
			}
		}
		url := atrackPlaylistURL(h, mediaID, e.Name())
		playlists = append(playlists, models.AudioPlaylistInfo{
			Index:    streamIdx,
			Language: lang,
			URL:      url,
		})
	}

	if len(playlists) > 0 {
		h.Instant.SetAudioPlaylists(fileID, playlists)
	}
}

func atrackPlaylistURL(h *Handler, mediaID int64, stream string) string {
	if storage.NeedsDerivedEncryption(h.App.DB, mediaID) {
		return fmt.Sprintf("/api/v1/media/%d/atrack/%s/index.m3u8", mediaID, stream)
	}
	return fmt.Sprintf("/atracks/%d/%s/index.m3u8", mediaID, stream)
}

func (h *Handler) atrackAssetPath(mediaID int64, stream, name, kind string) string {
	logical := stream + "/" + name
	if enc, ok := storage.LookupEncPath(h.App.DB, mediaID, kind, logical); ok {
		return enc
	}
	return filepath.Join(h.App.Config.Data.ATracks, strconv.FormatInt(mediaID, 10), stream, name)
}

// ServeAtrackPlaylist serves decrypted HLS audio playlist for one extracted stream.
func (h *Handler) ServeAtrackPlaylist(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mediaID, true); !ok {
		return
	}
	stream := strings.TrimSpace(c.Param("stream"))
	if stream == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid stream"})
		return
	}
	path := h.atrackAssetPath(mediaID, stream, "index.m3u8", "atrack_playlist")
	seeker, err := storage.OpenDerivedSeeker(h.App.DB, h.KeyVault, mediaID, path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "playlist missing"})
		return
	}
	defer seeker.Close()
	body, err := io.ReadAll(seeker)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rewritten := rewriteAtrackPlaylist(body, mediaID, stream)
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.Header("Cache-Control", "private, no-store")
	c.String(http.StatusOK, rewritten)
}

// ServeAtrackSegment serves one decrypted HLS audio segment.
func (h *Handler) ServeAtrackSegment(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mediaID, true); !ok {
		return
	}
	stream := strings.TrimSpace(c.Param("stream"))
	seg := strings.TrimSpace(c.Param("seg"))
	if stream == "" || seg == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid segment"})
		return
	}
	path := h.atrackAssetPath(mediaID, stream, seg, "atrack_segment")
	h.serveDerivedAsset(c, mediaID, path, "video/mp2t")
}

func rewriteAtrackPlaylist(body []byte, mediaID int64, stream string) string {
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		base := filepath.Base(trim)
		lines[i] = fmt.Sprintf("/api/v1/media/%d/atrack/%s/seg/%s", mediaID, stream, base)
	}
	return strings.Join(lines, "\n")
}

func (h *Handler) readAtrackMeta(mediaID int64, path string) ([]byte, error) {
	seeker, err := storage.OpenDerivedSeeker(h.App.DB, h.KeyVault, mediaID, path)
	if err != nil {
		return nil, err
	}
	defer seeker.Close()
	return io.ReadAll(seeker)
}
