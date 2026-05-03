package handler

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func (h *Handler) ListSubtitleTasks(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	limit := 200
	if v := c.Query("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := h.App.DB.Query(`
		SELECT t.id, t.media_id, COALESCE(m.title,''), t.status, COALESCE(t.message,''),
		       COALESCE(t.created_at,''), COALESCE(t.started_at,''), COALESCE(t.finished_at,''), COALESCE(t.updated_at,'')
		FROM subtitle_task t
		LEFT JOIN media m ON m.id = t.media_id
		ORDER BY t.updated_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var id, mediaID sql.NullInt64
		var title, status, msg, createdAt, startedAt, finishedAt, updatedAt sql.NullString
		if rows.Scan(&id, &mediaID, &title, &status, &msg, &createdAt, &startedAt, &finishedAt, &updatedAt) != nil {
			continue
		}
		items = append(items, gin.H{
			"id":          id.Int64,
			"media_id":    mediaID.Int64,
			"title":       title.String,
			"status":      status.String,
			"message":     msg.String,
			"created_at":  createdAt.String,
			"started_at":  startedAt.String,
			"finished_at": finishedAt.String,
			"updated_at":  updatedAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) ResetSubtitleTask(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	mediaID, err := strconv.ParseInt(c.Param("mediaId"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if err := h.Subtitle.ResetSubtitleJob(mediaID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) RetrySubtitleTask(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	mediaID, err := strconv.ParseInt(c.Param("mediaId"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	go func() {
		ctx := context.Background()
		if err := h.Subtitle.ProcessMedia(ctx, mediaID); err != nil {
			return
		}
	}()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) CleanupSubtitleTasksFailed(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	n, err := h.Subtitle.CleanupSubtitleTasksFailed()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

type cleanupSubtitleBeforeBody struct {
	Days int `json:"days"`
}

func (h *Handler) CleanupSubtitleTasksBefore(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	var body cleanupSubtitleBeforeBody
	_ = c.ShouldBindJSON(&body)
	days := body.Days
	if days <= 0 {
		days = 30
	}
	n, err := h.Subtitle.CleanupSubtitleTasksBefore(days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": n, "days": days})
}
