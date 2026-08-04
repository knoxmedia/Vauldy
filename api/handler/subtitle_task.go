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

	"knox-media/internal/postingest"
	"knox-media/internal/taskalign"
)

const (
	subtitleWorkerInterval = 15 * time.Second
	subtitleWorkerBatchMax = 3
)

// StartSubtitleTaskLoop drains pending subtitle_task rows outside post_ingest.
// Deprecated: not started by the router. Long ASR is unreliable here (20m reset,
// overlapping RunBatch). Use post_ingest enqueue APIs instead.
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
		WITH current_queue AS (
			SELECT q.*
			FROM post_ingest_task q
			JOIN media m ON m.id = q.media_id
			WHERE q.task_type = 'subtitle'
			  AND q.generation = COALESCE(m.ingest_generation, 0)
			  AND NOT (q.status = 'cancelled' AND q.last_error = 'deleted by admin')
			  AND q.id = (
				SELECT q2.id
				FROM post_ingest_task q2
				WHERE q2.media_id = q.media_id
				  AND q2.task_type = 'subtitle'
				  AND q2.generation = COALESCE(m.ingest_generation, 0)
				ORDER BY q2.id DESC
				LIMIT 1
			  )
		), task_media AS (
			SELECT media_id FROM subtitle_task
			UNION
			SELECT media_id FROM current_queue
		)
		SELECT x.media_id, COALESCE(m.title, ''),
		       COALESCE(t.status, ''), COALESCE(t.message, ''),
		       COALESCE(t.created_at, ''), COALESCE(t.started_at, ''),
		       COALESCE(t.finished_at, ''), COALESCE(t.updated_at, ''),
		       COALESCE(q.id, 0), COALESCE(q.status, ''), COALESCE(q.last_error, ''),
		       COALESCE(q.created_at, ''), COALESCE(q.started_at, ''),
		       COALESCE(q.finished_at, ''), COALESCE(q.updated_at, '')
		FROM task_media x
		LEFT JOIN media m ON m.id = x.media_id
		LEFT JOIN subtitle_task t ON t.media_id = x.media_id
		LEFT JOIN current_queue q ON q.media_id = x.media_id
		ORDER BY CASE
			WHEN q.status = 'running' OR t.status = 'running' THEN 0
			WHEN q.status = 'waiting'
			  OR (t.status IN ('pending', 'waiting') AND COALESCE(q.status, '') NOT IN ('failed', 'cancelled')) THEN 1
			WHEN q.status = 'failed' OR t.status = 'failed' THEN 2
			ELSE 3
		END,
		COALESCE(q.updated_at, t.updated_at) DESC,
		x.media_id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var mediaID, queueID sql.NullInt64
		var title, status, msg, createdAt, startedAt, finishedAt, updatedAt, queueStatus, queueMsg, queueCreatedAt, queueStartedAt, queueFinishedAt, queueUpdatedAt sql.NullString
		if rows.Scan(&mediaID, &title, &status, &msg, &createdAt, &startedAt, &finishedAt, &updatedAt, &queueID, &queueStatus, &queueMsg, &queueCreatedAt, &queueStartedAt, &queueFinishedAt, &queueUpdatedAt) != nil {
			continue
		}
		display := taskalign.Synthesize(queueStatus.String, status.String, "subtitle")
		if display == "" {
			display = status.String
		}
		if msg.String == "" {
			msg = queueMsg
		}
		if createdAt.String == "" {
			createdAt = queueCreatedAt
		}
		if startedAt.String == "" {
			startedAt = queueStartedAt
		}
		if finishedAt.String == "" {
			finishedAt = queueFinishedAt
		}
		if updatedAt.String == "" {
			updatedAt = queueUpdatedAt
		}
		items = append(items, gin.H{
			"id":            mediaID.Int64,
			"media_id":      mediaID.Int64,
			"title":         title.String,
			"status":        display,
			"domain_status": status.String,
			"queue_task_id": queueID.Int64,
			"queue_status":  queueStatus.String,
			"message":       msg.String,
			"created_at":    createdAt.String,
			"started_at":    startedAt.String,
			"finished_at":   finishedAt.String,
			"updated_at":    updatedAt.String,
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
	reset := subtitleResetTx(mediaID)
	result, err := enqueueExplicitPostIngest(c.Request.Context(), h.App.DB, mediaID, postingest.TaskSubtitle, true, reset, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "queued": result.Queued(), "action": result})
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
	reset := subtitleResetTx(mediaID)
	result, err := enqueueExplicitPostIngest(c.Request.Context(), h.App.DB, mediaID, postingest.TaskSubtitle, true, reset, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "queued": result.Queued(), "action": result})
}

func (h *Handler) CancelSubtitleTask(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	if h.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "post-ingest queue not configured"})
		return
	}
	mediaID, err := strconv.ParseInt(c.Param("mediaId"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	taskID, status, err := h.Queue.FindCurrentTask(c.Request.Context(), mediaID, postingest.TaskSubtitle)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if status != postingest.StatusWaiting && status != postingest.StatusRunning {
		c.JSON(http.StatusConflict, gin.H{"error": "subtitle task is not waiting or running"})
		return
	}
	if h.Dispatcher != nil {
		h.Dispatcher.CancelTask(taskID)
	}
	if err := h.Queue.AdminCancelTask(c.Request.Context(), taskID); err != nil {
		if strings.Contains(err.Error(), "cannot be cancel") {
			var cur postingest.Status
			if qerr := h.App.DB.QueryRowContext(c.Request.Context(), `SELECT status FROM post_ingest_task WHERE id=?`, taskID).Scan(&cur); qerr == nil {
				if cur == postingest.StatusCancelled || cur == postingest.StatusFailed || cur == postingest.StatusDone {
					_, _ = h.App.DB.ExecContext(c.Request.Context(), `
UPDATE subtitle_task SET status='pending',started_at=NULL,finished_at=NULL,message='cancelled by admin',updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, mediaID)
					c.JSON(http.StatusOK, gin.H{"ok": true, "status": string(cur)})
					return
				}
			}
		}
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	_, _ = h.App.DB.ExecContext(c.Request.Context(), `
UPDATE subtitle_task SET status='pending',started_at=NULL,finished_at=NULL,message='cancelled by admin',updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, mediaID)
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "cancelled"})
}

func (h *Handler) RunNowSubtitleTask(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	if h.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "post-ingest queue not configured"})
		return
	}
	mediaID, err := strconv.ParseInt(c.Param("mediaId"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	taskID, status, err := h.Queue.FindCurrentTask(c.Request.Context(), mediaID, postingest.TaskSubtitle)
	if err != nil {
		// No queue row: ensure pending domain + enqueue
		reset := subtitleEnsureTx(mediaID)
		result, qerr := enqueueExplicitPostIngest(c.Request.Context(), h.App.DB, mediaID, postingest.TaskSubtitle, false, reset, nil)
		if qerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": qerr.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"ok": true, "queued": result.Queued(), "action": result})
		return
	}
	switch status {
	case postingest.StatusWaiting:
		if err := h.Queue.AdminBumpWaiting(c.Request.Context(), taskID); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		_, _ = h.App.DB.ExecContext(c.Request.Context(), `
UPDATE subtitle_task SET status='pending',message=NULL,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, mediaID)
		c.JSON(http.StatusOK, gin.H{"ok": true, "status": "waiting", "bumped": true})
	case postingest.StatusRunning:
		c.JSON(http.StatusConflict, gin.H{"error": "subtitle task is already running"})
	default:
		reset := subtitleResetTx(mediaID)
		result, qerr := enqueueExplicitPostIngest(c.Request.Context(), h.App.DB, mediaID, postingest.TaskSubtitle, true, reset, nil)
		if qerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": qerr.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"ok": true, "queued": result.Queued(), "action": result})
	}
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
	reset := subtitleResetTx(mediaID)
	result, err := enqueueExplicitPostIngest(c.Request.Context(), h.App.DB, mediaID, postingest.TaskSubtitle, true, reset, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "queued": result.Queued(), "action": result})
}
