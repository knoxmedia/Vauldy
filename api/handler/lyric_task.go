package handler

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	lyricWorkerInterval  = 8 * time.Second
	lyricWorkerBatchMax  = 5
)

// StartLyricTaskLoop drains pending lyric recognition tasks in the background.
func (h *Handler) StartLyricTaskLoop(ctx context.Context) {
	go h.runLyricWorkerOnce()
	tk := time.NewTicker(lyricWorkerInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runLyricWorkerOnce()
		}
	}
}

func (h *Handler) runLyricWorkerOnce() {
	if h == nil || h.LyricWorker == nil || h.App == nil || h.App.DB == nil {
		return
	}
	var n int
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM lyric_task WHERE status = 'pending'`).Scan(&n)
	if n == 0 {
		return
	}
	limit := n
	if limit > lyricWorkerBatchMax {
		limit = lyricWorkerBatchMax
	}
	done, failed := h.LyricWorker.RunBatch(context.Background(), limit)
	if done+failed > 0 {
		log.Printf("lyric worker: processed=%d ok=%d fail=%d", done+failed, done, failed)
	}
}

func (h *Handler) ListLyricTasks(c *gin.Context) {
	if h.LyricWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "lyric worker disabled"})
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
		       COALESCE(t.vtt_path,''), COALESCE(t.lrc_path,''),
		       COALESCE(t.created_at,''), COALESCE(t.started_at,''), COALESCE(t.finished_at,''), COALESCE(t.updated_at,'')
		FROM lyric_task t
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
		var id, mediaID int64
		var title, status, msg, vtt, lrc, createdAt, startedAt, finishedAt, updatedAt string
		if rows.Scan(&id, &mediaID, &title, &status, &msg, &vtt, &lrc, &createdAt, &startedAt, &finishedAt, &updatedAt) != nil {
			continue
		}
		items = append(items, gin.H{
			"id":          id,
			"media_id":    mediaID,
			"title":       title,
			"status":      status,
			"message":     msg,
			"vtt_path":    vtt,
			"lrc_path":    lrc,
			"created_at":  createdAt,
			"started_at":  startedAt,
			"finished_at": finishedAt,
			"updated_at":  updatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) EnqueueLyricRecognition(c *gin.Context) {
	if h.LyricWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "lyric worker disabled"})
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
	if strings.TrimSpace(fileType) != "audio" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not an audio track"})
		return
	}
	if err := h.LyricWorker.EnqueueRetry(mediaID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	go func() {
		_ = h.LyricWorker.Process(context.Background(), mediaID)
	}()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) RetryLyricTask(c *gin.Context) {
	if h.LyricWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "lyric worker disabled"})
		return
	}
	mediaID, err := strconv.ParseInt(c.Param("mediaId"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if err := h.LyricWorker.EnqueueRetry(mediaID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	go func() {
		_ = h.LyricWorker.Process(context.Background(), mediaID)
	}()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) CleanupLyricTasksFailed(c *gin.Context) {
	if h.LyricWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "lyric worker disabled"})
		return
	}
	n, err := h.LyricWorker.CleanupFailed()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

type cleanupLyricBeforeBody struct {
	Days int `json:"days"`
}

func (h *Handler) CleanupLyricTasksBefore(c *gin.Context) {
	if h.LyricWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "lyric worker disabled"})
		return
	}
	var body cleanupLyricBeforeBody
	_ = c.ShouldBindJSON(&body)
	days := body.Days
	if days <= 0 {
		days = 30
	}
	n, err := h.LyricWorker.CleanupBefore(days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": n, "days": days})
}
