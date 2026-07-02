package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/store"
)

type scheduleTaskBody struct {
	Name        string         `json:"name" binding:"required"`
	Category    string         `json:"category"`
	TaskType    string         `json:"task_type" binding:"required"`
	IntervalMin int            `json:"interval_min"`
	Enabled     *int           `json:"enabled"`
	Payload     map[string]any `json:"payload"`
}

func (h *Handler) StartScheduleLoop(ctx context.Context) {
	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runDueScheduledTasks()
		}
	}
}

func (h *Handler) ListScheduledTasks(c *gin.Context) {
	rows, err := h.App.DB.Query(`
		SELECT id, name, category, task_type, interval_min, payload_json, enabled, COALESCE(last_run_at,''), COALESCE(last_status,''), COALESCE(last_message,''), created_at, updated_at
		FROM scheduled_task
		ORDER BY id DESC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, 64)
	for rows.Next() {
		var id, intervalMin, enabled sql.NullInt64
		var name, category, taskType, payloadJSON, lastRunAt, lastStatus, lastMsg, createdAt, updatedAt sql.NullString
		if rows.Scan(&id, &name, &category, &taskType, &intervalMin, &payloadJSON, &enabled, &lastRunAt, &lastStatus, &lastMsg, &createdAt, &updatedAt) != nil {
			continue
		}
		payload := map[string]any{}
		_ = json.Unmarshal([]byte(payloadJSON.String), &payload)
		items = append(items, gin.H{
			"id":           id.Int64,
			"name":         name.String,
			"category":     category.String,
			"task_type":    taskType.String,
			"interval_min": intervalMin.Int64,
			"enabled":      enabled.Int64,
			"payload":      payload,
			"last_run_at":  lastRunAt.String,
			"last_status":  lastStatus.String,
			"last_message": lastMsg.String,
			"created_at":   createdAt.String,
			"updated_at":   updatedAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) CreateScheduledTask(c *gin.Context) {
	var body scheduleTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	category := strings.TrimSpace(body.Category)
	if category == "" {
		category = "media"
	}
	intervalMin := body.IntervalMin
	if intervalMin <= 0 {
		intervalMin = 60
	}
	enabled := 1
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	js, _ := json.Marshal(body.Payload)
	res, err := h.App.DB.Exec(`
		INSERT INTO scheduled_task (name, category, task_type, interval_min, payload_json, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, body.Name, category, body.TaskType, intervalMin, string(js), enabled)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

func (h *Handler) UpdateScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body scheduleTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	category := strings.TrimSpace(body.Category)
	if category == "" {
		category = "media"
	}
	intervalMin := body.IntervalMin
	if intervalMin <= 0 {
		intervalMin = 60
	}
	enabled := 1
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	js, _ := json.Marshal(body.Payload)
	_, err = h.App.DB.Exec(`
		UPDATE scheduled_task
		SET name = ?, category = ?, task_type = ?, interval_min = ?, payload_json = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, body.Name, category, body.TaskType, intervalMin, string(js), enabled, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) DeleteScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, err := h.App.DB.Exec(`DELETE FROM scheduled_task WHERE id = ?`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) CleanupDuplicateScheduledTasks(c *gin.Context) {
	n, err := store.DedupeScheduledTasks(h.App.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": n})
}

func (h *Handler) RunScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	msg, runErr := h.runOneScheduledTask(id)
	if runErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": runErr.Error(), "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": msg})
}

func (h *Handler) runDueScheduledTasks() {
	rows, err := h.App.DB.Query(`
		SELECT id FROM scheduled_task
		WHERE enabled = 1
		  AND (last_run_at IS NULL OR datetime(last_run_at) <= datetime('now', '-' || interval_min || ' minutes'))
		ORDER BY id
		LIMIT 20
	`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil {
			continue
		}
		_, _ = h.runOneScheduledTask(id)
	}
}

func (h *Handler) runOneScheduledTask(id int64) (string, error) {
	var taskType, payloadJSON sql.NullString
	if err := h.App.DB.QueryRow(`SELECT task_type, payload_json FROM scheduled_task WHERE id = ? LIMIT 1`, id).Scan(&taskType, &payloadJSON); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("task not found")
		}
		return "", err
	}
	payload := map[string]any{}
	_ = json.Unmarshal([]byte(payloadJSON.String), &payload)
	msg, runErr := h.executeScheduledTask(taskType.String, payload)
	status := "done"
	if runErr != nil {
		status = "failed"
		msg = strings.TrimSpace(msg + "; " + runErr.Error())
	}
	_, _ = h.App.DB.Exec(`
		UPDATE scheduled_task
		SET last_run_at = CURRENT_TIMESTAMP, last_status = ?, last_message = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, status, msg, id)
	return msg, runErr
}

func (h *Handler) executeScheduledTask(taskType string, payload map[string]any) (string, error) {
	switch taskType {
	case "library_scan":
		libraryID := int64(anyToInt(payload["library_id"]))
		if libraryID <= 0 {
			return "", fmt.Errorf("payload.library_id required")
		}
		taskID, runningTaskID, err := h.startLibraryScanTask(libraryID, "schedule")
		if err != nil {
			return "", err
		}
		if runningTaskID > 0 {
			return "", fmt.Errorf("library scan already running (task #%d)", runningTaskID)
		}
		return fmt.Sprintf("已启动扫描任务 #%d", taskID), nil
	case "scrape_run":
		if !h.isScrapeEnabled() {
			return "刮削已禁用，跳过执行", nil
		}
		limit := anyToInt(payload["limit"])
		if limit <= 0 {
			limit = scrapeWorkerBatchMax
		}
		done, failed := h.runScrapeTasksWithLimit(nil, limit)
		return fmt.Sprintf("刮削执行完成：成功 %d，失败 %d", done, failed), nil
	case "transcode_cleanup_failed_before":
		days := anyToInt(payload["days"])
		if days <= 0 {
			days = 7
		}
		res, err := h.App.DB.Exec(`
			DELETE FROM transcode_task
			WHERE status = 'failed'
			  AND datetime(created_at) < datetime('now', '-' || ? || ' days')
		`, days)
		if err != nil {
			return "", err
		}
		n, _ := res.RowsAffected()
		return fmt.Sprintf("已清理 %d 条转码失败任务（早于 %d 天）", n, days), nil
	case "activity_cleanup":
		days := anyToInt(payload["days"])
		if days <= 0 {
			days = 30
		}
		res, err := h.App.DB.Exec(`
			DELETE FROM activity_log
			WHERE datetime(created_at) < datetime('now', '-' || ? || ' days')
		`, days)
		if err != nil {
			return "", err
		}
		n, _ := res.RowsAffected()
		return fmt.Sprintf("已清理 %d 条活动日志（早于 %d 天）", n, days), nil
	case "db_optimize":
		if _, err := h.App.DB.Exec(`VACUUM`); err != nil {
			return "", err
		}
		return "数据库优化完成", nil
	case "subtitle_process":
		if h.Subtitle == nil {
			return "", fmt.Errorf("subtitle service disabled")
		}
		limit := anyToInt(payload["limit"])
		if limit <= 0 {
			limit = 50
		}
		libID := int64(anyToInt(payload["library_id"]))
		done, failed := h.Subtitle.RunBatch(context.Background(), libID, limit)
		return fmt.Sprintf("字幕处理完成：成功 %d，失败 %d", done, failed), nil
	case "atrack_process":
		if h.AtrackWorker == nil {
			return "", fmt.Errorf("atrack worker disabled")
		}
		limit := anyToInt(payload["limit"])
		if limit <= 0 {
			limit = 10
		}
		done, failed := h.AtrackWorker.RunBatch(limit)
		return fmt.Sprintf("音轨提取完成：成功 %d，失败 %d", done, failed), nil
	case "keyframe_process":
		if h.KeyframeWorker == nil {
			return "", fmt.Errorf("keyframe worker disabled")
		}
		limit := anyToInt(payload["limit"])
		if limit <= 0 {
			limit = 10
		}
		done, failed := h.KeyframeWorker.RunBatch(limit)
		return fmt.Sprintf("关键帧提取完成：成功 %d，失败 %d", done, failed), nil
	case "lyric_process":
		if h.LyricWorker == nil {
			return "", fmt.Errorf("lyric worker disabled")
		}
		limit := anyToInt(payload["limit"])
		if limit <= 0 {
			limit = 20
		}
		done, failed := h.LyricWorker.RunBatch(context.Background(), limit)
		return fmt.Sprintf("歌词识别完成：成功 %d，失败 %d", done, failed), nil
	default:
		return "", fmt.Errorf("unsupported task_type: %s", taskType)
	}
}

func anyToInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case float32:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	default:
		return 0
	}
}
