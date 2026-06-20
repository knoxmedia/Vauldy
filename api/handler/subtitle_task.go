package handler

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	subtitleWorkerInterval = 15 * time.Second
	subtitleWorkerBatchMax = 3
)

// StartSubtitleTaskLoop drains pending subtitle tasks in the background.
func (h *Handler) StartSubtitleTaskLoop(ctx context.Context) {
	go h.runSubtitleWorkerOnce()
	tk := time.NewTicker(subtitleWorkerInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runSubtitleWorkerOnce()
		}
	}
}

func (h *Handler) runSubtitleWorkerOnce() {
	if h == nil || h.Subtitle == nil || h.App == nil || h.App.DB == nil {
		return
	}
	_, _ = h.App.DB.Exec(`
		UPDATE subtitle_task SET status = 'pending', started_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'
		  AND started_at IS NOT NULL
		  AND started_at < datetime('now', '-20 minutes')
	`)
	var n int
	_ = h.App.DB.QueryRow(`
		SELECT COUNT(1) FROM subtitle_task
		WHERE status = 'pending'
	`).Scan(&n)
	if n == 0 {
		return
	}
	limit := n
	if limit > subtitleWorkerBatchMax {
		limit = subtitleWorkerBatchMax
	}
	done, failed := h.Subtitle.RunBatch(context.Background(), 0, limit)
	if done+failed > 0 {
		log.Printf("subtitle worker: processed=%d ok=%d fail=%d pending=%d",
			done+failed, done, failed, n-done-failed)
	}
}

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
		if err := h.Subtitle.ResetSubtitleJob(mediaID); err != nil {
			return
		}
		if err := h.Subtitle.ProcessMedia(ctx, mediaID); err != nil {
			return
		}
	}()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) DeleteSubtitleTask(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	mediaID, err := strconv.ParseInt(c.Param("mediaId"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if err := h.Subtitle.DeleteSubtitleTask(mediaID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if strings.Contains(err.Error(), "running") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
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

// EnqueueSubtitleProcessing clears prior subtitle output and re-runs subtitle processing (sidecar, embedded, ASR/OCR).
func (h *Handler) EnqueueSubtitleProcessing(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	var fileType string
	if err := h.App.DB.QueryRow(`SELECT file_type FROM media WHERE id = ? LIMIT 1`, mediaID).Scan(&fileType); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}
	if strings.TrimSpace(fileType) != "video" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not a video"})
		return
	}
	go func(id int64) {
		if err := h.Subtitle.ResetSubtitleJob(id); err != nil {
			log.Printf("subtitle enqueue media=%d reset err=%v", id, err)
			return
		}
		if err := h.Subtitle.ProcessMedia(context.Background(), id); err != nil {
			log.Printf("subtitle enqueue media=%d process err=%v", id, err)
		}
	}(mediaID)
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "queued": true})
}
