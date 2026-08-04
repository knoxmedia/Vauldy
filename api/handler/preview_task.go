package handler

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func (h *Handler) ListPreviewTasks(c *gin.Context) {
	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := h.App.DB.Query(`
		SELECT p.media_id, COALESCE(m.title,''), p.status, p.interval_sec, p.thumb_count, p.thumb_width, p.thumb_height,
		       COALESCE(p.error_message,''), p.updated_at
		FROM preview_task p
		LEFT JOIN media m ON m.id = p.media_id
		ORDER BY p.updated_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var mediaID, intervalSec, thumbCount, width, height sql.NullInt64
		var title, status, errMsg, updatedAt sql.NullString
		if rows.Scan(&mediaID, &title, &status, &intervalSec, &thumbCount, &width, &height, &errMsg, &updatedAt) != nil {
			continue
		}
		items = append(items, gin.H{
			"media_id":      mediaID.Int64,
			"title":         title.String,
			"status":        status.String,
			"interval_sec":  intervalSec.Int64,
			"thumb_count":   thumbCount.Int64,
			"thumb_width":   width.Int64,
			"thumb_height":  height.Int64,
			"error_message": errMsg.String,
			"updated_at":    updatedAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) RetryPreviewTask(c *gin.Context) {
	if h.PreviewWorker == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "preview worker unavailable"})
		return
	}
	mediaID, err := strconv.ParseInt(c.Param("mediaId"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	var filePath sql.NullString
	var duration sql.NullInt64
	var enabled sql.NullInt64
	if err := h.App.DB.QueryRow(`
		SELECT m.file_path, m.duration, COALESCE(l.preview_extract,0)
		FROM media m LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ? LIMIT 1
	`, mediaID).Scan(&filePath, &duration, &enabled); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if enabled.Int64 != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "preview extract is disabled for this library"})
		return
	}
	_, _ = h.App.DB.Exec(`UPDATE preview_task SET status='waiting', error_message=NULL, updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`, mediaID)
	info, err := h.PreviewWorker.Ensure(c.Request.Context(), mediaID, filePath.String, duration.Int64)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": info.Status})
}
