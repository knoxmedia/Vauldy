package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/postingest"
)

type batchTaskBody struct {
	Action   string  `json:"action"`
	IDs      []int64 `json:"ids"`
	MediaIDs []int64 `json:"media_ids"`
}

type batchTaskResult struct {
	ID      int64  `json:"id,omitempty"`
	MediaID int64  `json:"media_id,omitempty"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

func normalizeBatchIDs(ids []int64) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (h *Handler) BatchSubtitleTasks(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	var body batchTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	ids := normalizeBatchIDs(body.MediaIDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_ids required"})
		return
	}
	if len(ids) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many media_ids (max 200)"})
		return
	}
	results := make([]batchTaskResult, 0, len(ids))
	okCount := 0
	for _, mediaID := range ids {
		r := batchTaskResult{MediaID: mediaID}
		var err error
		switch action {
		case "retry":
			err = h.batchSubtitleRetry(c.Request.Context(), mediaID)
		case "delete":
			err = h.batchSubtitleDelete(mediaID)
		case "cancel", "stop":
			err = h.batchSubtitleCancel(c.Request.Context(), mediaID)
		case "run_now", "run-now":
			err = h.batchSubtitleRunNow(c.Request.Context(), mediaID)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "action must be retry, delete, cancel, or run_now"})
			return
		}
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			okCount++
		}
		results = append(results, r)
	}
	c.JSON(http.StatusOK, gin.H{"ok": okCount, "failed": len(ids) - okCount, "results": results})
}

func (h *Handler) batchSubtitleRetry(ctx context.Context, mediaID int64) error {
	reset := subtitleResetTx(mediaID)
	_, err := enqueueExplicitPostIngest(ctx, h.App.DB, mediaID, postingest.TaskSubtitle, true, reset, nil)
	return err
}

func (h *Handler) batchSubtitleDelete(mediaID int64) error {
	return h.Subtitle.DeleteSubtitleTask(mediaID)
}

func (h *Handler) batchSubtitleCancel(ctx context.Context, mediaID int64) error {
	if h.Queue == nil {
		return fmt.Errorf("post-ingest queue not configured")
	}
	taskID, status, err := h.Queue.FindCurrentTask(ctx, mediaID, postingest.TaskSubtitle)
	if err != nil {
		return err
	}
	if status != postingest.StatusWaiting && status != postingest.StatusRunning {
		return fmt.Errorf("subtitle task is not waiting or running")
	}
	if h.Dispatcher != nil {
		h.Dispatcher.CancelTask(taskID)
	}
	if err := h.Queue.AdminCancelTask(ctx, taskID); err != nil {
		if strings.Contains(err.Error(), "cannot be cancel") {
			var cur postingest.Status
			if qerr := h.App.DB.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=?`, taskID).Scan(&cur); qerr == nil {
				if cur == postingest.StatusCancelled || cur == postingest.StatusFailed || cur == postingest.StatusDone {
					_, _ = h.App.DB.ExecContext(ctx, `
UPDATE subtitle_task SET status='pending',extract_status='pending',recognize_status='pending',extract_message=NULL,recognize_message=NULL,started_at=NULL,finished_at=NULL,message='cancelled by admin',updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, mediaID)
					return nil
				}
			}
		}
		return err
	}
	_, _ = h.App.DB.ExecContext(ctx, `
UPDATE subtitle_task SET status='pending',extract_status='pending',recognize_status='pending',extract_message=NULL,recognize_message=NULL,started_at=NULL,finished_at=NULL,message='cancelled by admin',updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, mediaID)
	return nil
}

func (h *Handler) batchSubtitleRunNow(ctx context.Context, mediaID int64) error {
	if h.Queue == nil {
		return fmt.Errorf("post-ingest queue not configured")
	}
	taskID, status, err := h.Queue.FindCurrentTask(ctx, mediaID, postingest.TaskSubtitle)
	if err != nil {
		reset := subtitleEnsureTx(mediaID)
		_, qerr := enqueueExplicitPostIngest(ctx, h.App.DB, mediaID, postingest.TaskSubtitle, false, reset, nil)
		return qerr
	}
	switch status {
	case postingest.StatusWaiting:
		if err := h.Queue.AdminBumpWaiting(ctx, taskID); err != nil {
			return err
		}
		_, _ = h.App.DB.ExecContext(ctx, `
UPDATE subtitle_task SET status='pending',extract_status='pending',recognize_status='pending',extract_message=NULL,recognize_message=NULL,message=NULL,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, mediaID)
		return nil
	case postingest.StatusRunning:
		return fmt.Errorf("subtitle task is already running")
	default:
		reset := subtitleResetTx(mediaID)
		_, qerr := enqueueExplicitPostIngest(ctx, h.App.DB, mediaID, postingest.TaskSubtitle, true, reset, nil)
		if qerr != nil {
			return qerr
		}
		taskID, status, err = h.Queue.FindCurrentTask(ctx, mediaID, postingest.TaskSubtitle)
		if err == nil && status == postingest.StatusWaiting {
			_ = h.Queue.AdminBumpWaiting(ctx, taskID)
		}
		return nil
	}
}

func (h *Handler) BatchEncryptTasks(c *gin.Context) {
	if h.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "post-ingest queue not configured"})
		return
	}
	var body batchTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	ids := normalizeBatchIDs(body.IDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids required"})
		return
	}
	if len(ids) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many ids (max 200)"})
		return
	}
	results := make([]batchTaskResult, 0, len(ids))
	okCount := 0
	for _, id := range ids {
		r := batchTaskResult{ID: id}
		var err error
		switch action {
		case "retry":
			err = h.Queue.AdminResetEncrypt(c.Request.Context(), id, middleware.UserID(c))
			if err == nil {
				_ = h.Queue.AdminBumpWaiting(c.Request.Context(), id)
			}
		case "delete":
			err = h.Queue.AdminRemoveEncrypt(c.Request.Context(), id, middleware.UserID(c))
		case "cancel", "stop":
			if h.Dispatcher != nil {
				h.Dispatcher.CancelTask(id)
			}
			err = h.Queue.AdminCancelEncrypt(c.Request.Context(), id)
			if err != nil && strings.Contains(err.Error(), "cannot be cancel") {
				var status postingest.Status
				if qerr := h.App.DB.QueryRowContext(c.Request.Context(), `SELECT status FROM post_ingest_task WHERE id=? AND task_type='encrypt'`, id).Scan(&status); qerr == nil {
					if status == postingest.StatusCancelled || status == postingest.StatusFailed || status == postingest.StatusDone {
						err = nil
					}
				}
			}
		case "run_now", "run-now":
			err = h.batchEncryptRunNow(c.Request.Context(), id, middleware.UserID(c))
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "action must be retry, delete, cancel, or run_now"})
			return
		}
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			okCount++
		}
		results = append(results, r)
	}
	c.JSON(http.StatusOK, gin.H{"ok": okCount, "failed": len(ids) - okCount, "results": results})
}

func (h *Handler) batchEncryptRunNow(ctx context.Context, id, actorID int64) error {
	var status postingest.Status
	err := h.App.DB.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=? AND task_type='encrypt'`, id).Scan(&status)
	if err != nil {
		return fmt.Errorf("encrypt task not found")
	}
	switch status {
	case postingest.StatusWaiting:
		return h.Queue.AdminBumpWaiting(ctx, id)
	case postingest.StatusRunning:
		return fmt.Errorf("encrypt task is already running")
	default:
		if err := h.Queue.AdminResetEncrypt(ctx, id, actorID); err != nil {
			return err
		}
		return h.Queue.AdminBumpWaiting(ctx, id)
	}
}

func (h *Handler) BatchTranscodeTasks(c *gin.Context) {
	var body batchTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	ids := normalizeBatchIDs(body.IDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids required"})
		return
	}
	if len(ids) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many ids (max 200)"})
		return
	}
	results := make([]batchTaskResult, 0, len(ids))
	okCount := 0
	for _, id := range ids {
		r := batchTaskResult{ID: id}
		var err error
		switch action {
		case "retry":
			err = h.batchTranscodeRetry(id)
		case "delete":
			_, err = h.App.DB.Exec(`DELETE FROM transcode_task WHERE id=? AND status IN ('failed','cancelled','done','waiting')`, id)
		case "cancel", "stop":
			err = h.batchTranscodeCancel(id)
		case "run_now", "run-now":
			err = h.batchTranscodeRunNow(id)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "action must be retry, delete, cancel, or run_now"})
			return
		}
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			okCount++
		}
		results = append(results, r)
	}
	c.JSON(http.StatusOK, gin.H{"ok": okCount, "failed": len(ids) - okCount, "results": results})
}

func (h *Handler) batchTranscodeCancel(id int64) error {
	if h.Worker != nil && h.Worker.Cancel(id) {
		_, _ = h.App.DB.Exec(`UPDATE transcode_task SET status=? WHERE id=?`, "cancelled", id)
		return nil
	}
	res, err := h.App.DB.Exec(`UPDATE transcode_task SET status=? WHERE id=? AND status IN ('waiting','running')`, "cancelled", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}
	return fmt.Errorf("task not cancellable")
}

func (h *Handler) batchTranscodeRetry(id int64) error {
	var taskType string
	if err := h.App.DB.QueryRow(`SELECT COALESCE(task_type,'batch') FROM transcode_task WHERE id=?`, id).Scan(&taskType); err == nil && taskType == "pretranscode" {
		mod := pretranscodeModule()
		if mod == nil {
			return fmt.Errorf("pretranscode module not available")
		}
		return mod.Task.RetryTask(id)
	}
	var next int64
	_ = h.App.DB.QueryRow(`SELECT COALESCE(MAX(priority),0)+1 FROM transcode_task`).Scan(&next)
	res, err := h.App.DB.Exec(`UPDATE transcode_task SET status='waiting', progress=0, error_message=NULL, priority=? WHERE id=? AND status IN ('failed','cancelled') AND COALESCE(task_type,'batch')='batch'`, next, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task not retryable")
	}
	h.kickTranscodeWorker()
	return nil
}

func (h *Handler) batchTranscodeRunNow(id int64) error {
	var status string
	if err := h.App.DB.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, id).Scan(&status); err != nil {
		return fmt.Errorf("task not found")
	}
	switch status {
	case "waiting":
		var next int64
		_ = h.App.DB.QueryRow(`SELECT COALESCE(MAX(priority),0)+1 FROM transcode_task`).Scan(&next)
		_, err := h.App.DB.Exec(`UPDATE transcode_task SET priority=? WHERE id=? AND status='waiting'`, next, id)
		return err
	case "running":
		return fmt.Errorf("task is already running")
	case "failed", "cancelled":
		return h.batchTranscodeRetry(id)
	default:
		return fmt.Errorf("task status %s cannot run now", status)
	}
}

func (h *Handler) BatchLyricTasks(c *gin.Context) {
	if h.LyricWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "lyric worker disabled"})
		return
	}
	var body batchTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	ids := normalizeBatchIDs(body.MediaIDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_ids required"})
		return
	}
	if len(ids) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many media_ids (max 200)"})
		return
	}
	results := make([]batchTaskResult, 0, len(ids))
	okCount := 0
	for _, mediaID := range ids {
		r := batchTaskResult{MediaID: mediaID}
		var err error
		switch action {
		case "retry":
			err = h.LyricWorker.EnqueueRetry(mediaID)
		case "delete":
			_, err = h.App.DB.Exec(`DELETE FROM lyric_task WHERE media_id=? AND status IN ('failed','pending','done')`, mediaID)
		case "cancel", "stop":
			_, err = h.App.DB.Exec(`UPDATE lyric_task SET status='failed', message='cancelled by admin', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND status IN ('pending','running')`, mediaID)
		case "run_now", "run-now":
			var next int64
			_ = h.App.DB.QueryRow(`SELECT COALESCE(MAX(priority),0)+1 FROM lyric_task`).Scan(&next)
			_, err = h.App.DB.Exec(`UPDATE lyric_task SET status='pending', priority=?, message=NULL, updated_at=CURRENT_TIMESTAMP WHERE media_id=? AND status IN ('pending','failed')`, next, mediaID)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "action must be retry, delete, cancel, or run_now"})
			return
		}
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			okCount++
		}
		results = append(results, r)
	}
	c.JSON(http.StatusOK, gin.H{"ok": okCount, "failed": len(ids) - okCount, "results": results})
}

func (h *Handler) BatchPreviewTasks(c *gin.Context) {
	h.batchPostIngestByMedia(c, postingest.TaskPreview)
}

func (h *Handler) BatchAtrackTasks(c *gin.Context) {
	h.batchPostIngestByMedia(c, postingest.TaskAtrack)
}

func (h *Handler) BatchKeyframeTasks(c *gin.Context) {
	h.batchPostIngestByMedia(c, postingest.TaskKeyframe)
}

func (h *Handler) batchPostIngestByMedia(c *gin.Context, typ postingest.TaskType) {
	if h.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "post-ingest queue not configured"})
		return
	}
	var body batchTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	ids := normalizeBatchIDs(body.MediaIDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_ids required"})
		return
	}
	if len(ids) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many media_ids (max 200)"})
		return
	}
	results := make([]batchTaskResult, 0, len(ids))
	okCount := 0
	for _, mediaID := range ids {
		r := batchTaskResult{MediaID: mediaID}
		var err error
		switch action {
		case "retry":
			_, err = enqueueExplicitPostIngest(c.Request.Context(), h.App.DB, mediaID, typ, true, nil, nil)
		case "delete":
			taskID, status, ferr := h.Queue.FindCurrentTask(c.Request.Context(), mediaID, typ)
			if ferr != nil {
				err = ferr
				break
			}
			if status == postingest.StatusRunning {
				err = fmt.Errorf("cannot delete running task")
				break
			}
			_, err = h.App.DB.ExecContext(c.Request.Context(), `DELETE FROM post_ingest_task WHERE id=?`, taskID)
		case "cancel", "stop":
			taskID, status, ferr := h.Queue.FindCurrentTask(c.Request.Context(), mediaID, typ)
			if ferr != nil {
				err = ferr
				break
			}
			if status != postingest.StatusWaiting && status != postingest.StatusRunning {
				err = fmt.Errorf("task is not waiting or running")
				break
			}
			if h.Dispatcher != nil {
				h.Dispatcher.CancelTask(taskID)
			}
			err = h.Queue.AdminCancelTask(c.Request.Context(), taskID)
		case "run_now", "run-now":
			taskID, status, ferr := h.Queue.FindCurrentTask(c.Request.Context(), mediaID, typ)
			if ferr != nil {
				_, err = enqueueExplicitPostIngest(c.Request.Context(), h.App.DB, mediaID, typ, false, nil, nil)
				break
			}
			if status == postingest.StatusWaiting {
				err = h.Queue.AdminBumpWaiting(c.Request.Context(), taskID)
			} else if status == postingest.StatusRunning {
				err = fmt.Errorf("task is already running")
			} else {
				_, err = enqueueExplicitPostIngest(c.Request.Context(), h.App.DB, mediaID, typ, true, nil, nil)
				if err == nil {
					if tid, st, e2 := h.Queue.FindCurrentTask(c.Request.Context(), mediaID, typ); e2 == nil && st == postingest.StatusWaiting {
						_ = h.Queue.AdminBumpWaiting(c.Request.Context(), tid)
					}
				}
			}
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "action must be retry, delete, cancel, or run_now"})
			return
		}
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			okCount++
		}
		results = append(results, r)
	}
	c.JSON(http.StatusOK, gin.H{"ok": okCount, "failed": len(ids) - okCount, "results": results})
}
