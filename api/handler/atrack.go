package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"

	models "knox-media/internal/model"
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

	h.AtrackWorker.Enqueue(mediaID)
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

	h.AtrackWorker.Enqueue(mediaID)
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
		playlistFile := filepath.Join(outDir, e.Name(), "index.m3u8")
		if _, err := os.Stat(playlistFile); err != nil {
			continue
		}
		// Try to read language from a metadata file, default to stream index.
		lang := fmt.Sprintf("Track %d", streamIdx)
		metaFile := filepath.Join(outDir, e.Name(), "meta.json")
		if data, err := os.ReadFile(metaFile); err == nil {
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
		url := fmt.Sprintf("/atracks/%d/%s/index.m3u8", mediaID, e.Name())
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
