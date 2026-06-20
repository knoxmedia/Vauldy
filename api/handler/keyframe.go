package handler

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// EnqueueKeyframeExtraction creates or resets a keyframe_task for a media item.
func (h *Handler) EnqueueKeyframeExtraction(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if h.KeyframeWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "keyframe worker disabled"})
		return
	}

	var fileID, filePath sql.NullString
	var duration sql.NullInt64
	if err := h.App.DB.QueryRow(
		`SELECT file_id, file_path, COALESCE(duration,0) FROM media WHERE id = ? LIMIT 1`,
		mediaID,
	).Scan(&fileID, &filePath, &duration); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}

	h.KeyframeWorker.EnqueueRetry(mediaID)
	go func() {
		_ = h.KeyframeWorker.Run(context.Background(), mediaID, fileID.String, filePath.String, float64(duration.Int64))
	}()

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListKeyframeTasks returns all keyframe_task rows.
func (h *Handler) ListKeyframeTasks(c *gin.Context) {
	rows, err := h.App.DB.Query(`
		SELECT t.id, t.media_id, COALESCE(m.title,''), COALESCE(m.file_path,''), t.status, COALESCE(t.output_dir,''), t.keyframe_count, COALESCE(t.error_message,''), t.created_at, t.updated_at
		FROM keyframe_task t
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
		var id, mediaID, count sql.NullInt64
		var title, filePath, status, outputDir, errMsg, createdAt, updatedAt sql.NullString
		if rows.Scan(&id, &mediaID, &title, &filePath, &status, &outputDir, &count, &errMsg, &createdAt, &updatedAt) != nil {
			continue
		}
		items = append(items, gin.H{
			"id":             id.Int64,
			"media_id":       mediaID.Int64,
			"title":          title.String,
			"file_path":      filePath.String,
			"status":         status.String,
			"output_dir":     outputDir.String,
			"keyframe_count": count.Int64,
			"error_message":  errMsg.String,
			"created_at":     createdAt.String,
			"updated_at":     updatedAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// RetryKeyframeTask re-enqueues a failed or done keyframe_task.
func (h *Handler) RetryKeyframeTask(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("mediaId"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if h.KeyframeWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "keyframe worker disabled"})
		return
	}

	var fileID, filePath sql.NullString
	var duration sql.NullInt64
	if err := h.App.DB.QueryRow(
		`SELECT file_id, file_path, COALESCE(duration,0) FROM media WHERE id = ? LIMIT 1`,
		mediaID,
	).Scan(&fileID, &filePath, &duration); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}

	h.KeyframeWorker.EnqueueRetry(mediaID)
	go func() {
		_ = h.KeyframeWorker.Run(context.Background(), mediaID, fileID.String, filePath.String, float64(duration.Int64))
	}()

	c.JSON(http.StatusOK, gin.H{"ok": true})
}
