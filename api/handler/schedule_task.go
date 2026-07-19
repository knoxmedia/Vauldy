package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"knox-media/internal/scancoord"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/postingest"
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
			h.runDueScheduledTasks(ctx)
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
	msg, runErr := h.runOneScheduledTask(c.Request.Context(), id)
	if runErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": runErr.Error(), "message": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": msg})
}

func (h *Handler) runDueScheduledTasks(ctx context.Context) {
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
		_, _ = h.runOneScheduledTask(ctx, id)
	}
}

func (h *Handler) runOneScheduledTask(ctx context.Context, id int64) (string, error) {
	var taskType, payloadJSON sql.NullString
	if err := h.App.DB.QueryRow(`SELECT task_type, payload_json FROM scheduled_task WHERE id = ? LIMIT 1`, id).Scan(&taskType, &payloadJSON); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("task not found")
		}
		return "", err
	}
	payload := map[string]any{}
	_ = json.Unmarshal([]byte(payloadJSON.String), &payload)
	msg, runErr := h.executeScheduledTask(ctx, taskType.String, payload)
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

func (h *Handler) executeScheduledTask(ctx context.Context, taskType string, payload map[string]any) (string, error) {
	switch taskType {
	case "library_scan":
		libraryID := int64(anyToInt(payload["library_id"]))
		if libraryID <= 0 {
			return "", fmt.Errorf("payload.library_id required")
		}
		taskID, runningTaskID, err := h.startLibraryScanTask(libraryID, string(scancoord.SourceScheduled))
		if err != nil {
			return "", err
		}
		if runningTaskID > 0 {
			return "", fmt.Errorf("library scan already running (task #%d)", runningTaskID)
		}
		return fmt.Sprintf("已启动扫描任务 #%d", taskID), nil
	case "scrape_run":
		if !h.isScrapeEnabled(ctx) {
			return "刮削已禁用，跳过执行", nil
		}
		limit := anyToInt(payload["limit"])
		if limit <= 0 {
			limit = scrapeWorkerBatchMax
		}
		done, failed := h.runScrapeTasksWithLimit(ctx, nil, limit)
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
		n, err := h.enqueueScheduledPostIngest(ctx, postingest.TaskSubtitle, libID, limit)
		return fmt.Sprintf("字幕任务已入队：%d", n), err
	case "atrack_process":
		if h.AtrackWorker == nil {
			return "", fmt.Errorf("atrack worker disabled")
		}
		limit := anyToInt(payload["limit"])
		if limit <= 0 {
			limit = 10
		}
		libID := int64(anyToInt(payload["library_id"]))
		n, err := h.enqueueScheduledPostIngest(ctx, postingest.TaskAtrack, libID, limit)
		return fmt.Sprintf("音轨任务已入队：%d", n), err
	case "keyframe_process":
		limit := anyToInt(payload["limit"])
		if limit <= 0 {
			limit = 10
		}
		libID := int64(anyToInt(payload["library_id"]))
		n, err := h.enqueueScheduledPostIngest(ctx, postingest.TaskKeyframe, libID, limit)
		return fmt.Sprintf("?????????%d", n), err
	case "lyric_process":
		if h.LyricWorker == nil {
			return "", fmt.Errorf("lyric worker disabled")
		}
		limit := anyToInt(payload["limit"])
		if limit <= 0 {
			limit = 20
		}
		done, failed := h.LyricWorker.RunBatch(ctx, limit)
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

func (h *Handler) enqueueScheduledPostIngest(ctx context.Context, typ postingest.TaskType, libraryID int64, limit int) (int, error) {
	query := `SELECT m.id FROM media m LEFT JOIN post_ingest_task p ON p.media_id=m.id AND p.task_type=? WHERE m.file_type='video' AND COALESCE(m.status,'active')='active' AND (p.id IS NULL OR p.status IN ('failed','cancelled'))`
	args := []any{typ}
	if libraryID > 0 {
		query += ` AND m.library_id=?`
		args = append(args, libraryID)
	}
	query += ` ORDER BY m.id LIMIT ?`
	args = append(args, limit)
	rows, err := h.App.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	queued := 0
	for _, id := range ids {
		var reset explicitResetTx
		switch typ {
		case postingest.TaskSubtitle:
			reset = subtitleEnsureTx(id)
		case postingest.TaskAtrack:
			reset = func(c context.Context, tx *sql.Tx) error {
				_, e := tx.ExecContext(c, `INSERT INTO atrack_task(media_id,status,updated_at) VALUES(?,'waiting',CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO UPDATE SET status='waiting',error_message=NULL,updated_at=CURRENT_TIMESTAMP`, id)
				return e
			}
		case postingest.TaskKeyframe:
			reset = func(c context.Context, tx *sql.Tx) error {
				_, e := tx.ExecContext(c, `INSERT INTO keyframe_task(media_id,status,output_dir,keyframe_count,error_message,updated_at) VALUES(?,'waiting',NULL,0,NULL,CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO UPDATE SET status='waiting',output_dir=NULL,keyframe_count=0,error_message=NULL,updated_at=CURRENT_TIMESTAMP`, id)
				return e
			}
		default:
			return queued, fmt.Errorf("unsupported scheduled post-ingest type: %s", typ)
		}
		r, e := enqueueExplicitPostIngest(ctx, h.App.DB, id, typ, false, reset, nil)
		if e != nil {
			return queued, e
		}
		if r.Queued() {
			queued++
		}
	}
	return queued, nil
}
