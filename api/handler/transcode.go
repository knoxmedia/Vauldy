package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type asyncTranscodeBody struct {
	MediaID int64  `json:"media_id" binding:"required"`
	Quality string `json:"quality"`
}

type cleanupFailedBody struct {
	Limit int `json:"limit"`
}

type cleanupFailedBeforeBody struct {
	Days int `json:"days"`
}

type repairDRMBody struct {
	Limit int  `json:"limit"`
	Retry bool `json:"retry"`
}

func (h *Handler) TranscodeAsync(c *gin.Context) {
	var body asyncTranscodeBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := body.Quality
	if q == "" {
		q = "720p"
	}
	var fileID, fpath string
	if err := h.App.DB.QueryRow(`SELECT file_id, file_path FROM media WHERE id = ?`, body.MediaID).Scan(&fileID, &fpath); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	res, err := h.App.DB.Exec(`INSERT INTO transcode_task (file_id, quality, status, progress) VALUES (?, ?, 'waiting', 0)`, fileID, q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	tid, _ := res.LastInsertId()
	h.kickTranscodeWorker()
	c.JSON(http.StatusAccepted, gin.H{"task_id": tid, "status": "waiting"})
}

func (h *Handler) ListTranscodeTasks(c *gin.Context) {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := h.App.DB.Query(`
		SELECT id, file_id, quality, status, progress, error_message, output_path, created_at, pipeline_type, drm_status, source_cleanup_status
		FROM (
			SELECT id, file_id, quality, status, progress, error_message, output_path, created_at,
			       'hls' AS pipeline_type, '' AS drm_status, '' AS source_cleanup_status
			FROM transcode_task
			UNION ALL
			SELECT p.id, m.file_id AS file_id, p.pipeline_type AS quality, p.status, p.progress, p.error_message, p.output_path, p.created_at,
			       p.pipeline_type, COALESCE(p.drm_status,''), COALESCE(p.source_cleanup_status,'')
			FROM package_task p
			LEFT JOIN media m ON m.id = p.media_id
		)
		ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var id int64
		var fileID, quality, status, errMsg, out, created, pipelineType, drmStatus, cleanupStatus sql.NullString
		var prog sql.NullInt64
		if err := rows.Scan(&id, &fileID, &quality, &status, &prog, &errMsg, &out, &created, &pipelineType, &drmStatus, &cleanupStatus); err != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id, "file_id": fileID.String, "quality": quality.String,
			"status": status.String, "progress": prog.Int64, "error_message": errMsg.String, "output_path": out.String, "created_at": created.String,
			"pipeline_type": pipelineType.String, "drm_status": drmStatus.String, "source_cleanup_status": cleanupStatus.String,
		})
	}
	if items == nil {
		items = []gin.H{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) CancelTranscodeTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	ok := false
	if h.Worker != nil {
		ok = h.Worker.Cancel(id)
	}
	if ok {
		_, _ = h.App.DB.Exec(`UPDATE transcode_task SET status = ? WHERE id = ?`, "cancelled", id)
		c.JSON(http.StatusOK, gin.H{"ok": true, "cancelled": true})
		return
	}
	res, err := h.App.DB.Exec(`UPDATE transcode_task SET status = ? WHERE id = ? AND status IN ('waiting','running')`, "cancelled", id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if h.PackageWorker != nil && h.PackageWorker.Cancel(id) {
			_, _ = h.App.DB.Exec(`UPDATE package_task SET status = ?, updated_at=CURRENT_TIMESTAMP WHERE id = ?`, "cancelled", id)
			c.JSON(http.StatusOK, gin.H{"ok": true, "cancelled": true})
			return
		}
		res2, err2 := h.App.DB.Exec(`UPDATE package_task SET status = ?, updated_at=CURRENT_TIMESTAMP WHERE id = ? AND status IN ('waiting','running')`, "cancelled", id)
		if err2 == nil {
			n2, _ := res2.RowsAffected()
			if n2 > 0 {
				c.JSON(http.StatusOK, gin.H{"ok": true, "cancelled": true})
				return
			}
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "task not cancellable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "cancelled": true})
}

func (h *Handler) RetryTranscodeTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	res, err := h.App.DB.Exec(`UPDATE transcode_task SET status='waiting', progress=0, error_message=NULL WHERE id=? AND status IN ('failed','cancelled')`, id)
	if err == nil {
		n, _ := res.RowsAffected()
		if n > 0 {
			h.kickTranscodeWorker()
			c.JSON(http.StatusAccepted, gin.H{"ok": true, "status": "waiting", "task_id": id})
			return
		}
	}

	res2, err2 := h.App.DB.Exec(`UPDATE package_task SET status='waiting', progress=0, drm_status='', source_cleanup_status='pending', error_message=NULL, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('failed','cancelled')`, id)
	if err2 != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err2.Error()})
		return
	}
	n2, _ := res2.RowsAffected()
	if n2 == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task not retryable"})
		return
	}
	if h.PackageWorker != nil {
		go func(taskID int64) {
			_ = h.PackageWorker.RunTask(context.Background(), taskID)
		}(id)
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "status": "waiting", "task_id": id})
}

func (h *Handler) CleanupFailedTranscodeTasks(c *gin.Context) {
	var body cleanupFailedBody
	_ = c.ShouldBindJSON(&body)
	if body.Limit > 0 {
		res, err := h.App.DB.Exec(`
			DELETE FROM transcode_task
			WHERE id IN (
				SELECT id FROM transcode_task
				WHERE status = 'failed'
				ORDER BY id DESC
				LIMIT ?
			)
		`, body.Limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		n, _ := res.RowsAffected()
		c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": n, "scope": "failed", "limit": body.Limit})
		return
	}
	res, err := h.App.DB.Exec(`DELETE FROM transcode_task WHERE status = 'failed'`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": n, "scope": "failed"})
}

func (h *Handler) CleanupFailedTranscodeTasksBefore(c *gin.Context) {
	var body cleanupFailedBeforeBody
	_ = c.ShouldBindJSON(&body)
	days := body.Days
	if days <= 0 {
		days = 7
	}
	res, err := h.App.DB.Exec(`
		DELETE FROM transcode_task
		WHERE status = 'failed'
		  AND datetime(created_at) < datetime('now', '-' || ? || ' days')
	`, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": n, "scope": "failed_before_days", "days": days})
}

func (h *Handler) RepairDRMOutputs(c *gin.Context) {
	if h == nil || h.PackageWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "drm package worker not available"})
		return
	}
	var body repairDRMBody
	_ = c.ShouldBindJSON(&body)
	scanned, broken, retried, err := h.PackageWorker.RepairBrokenTasks(context.Background(), body.Limit, body.Retry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"scanned": scanned,
		"broken":  broken,
		"retried": retried,
		"retry":   body.Retry,
		"limit":   body.Limit,
	})
}

func (h *Handler) GetTranscodeTaskStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var taskID int64
	var fileID, quality, status, out sql.NullString
	var prog sql.NullInt64
	fromPackage := false
	row := h.App.DB.QueryRow(`
		SELECT id, file_id, quality, status, progress, error_message, output_path
		FROM transcode_task
		WHERE id = ?
		LIMIT 1
	`, id)
	var errMsg sql.NullString
	if err := row.Scan(&taskID, &fileID, &quality, &status, &prog, &errMsg, &out); err != nil {
		if err != sql.ErrNoRows {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		row2 := h.App.DB.QueryRow(`
			SELECT p.id, COALESCE(m.file_id,''), p.pipeline_type, p.status, p.progress, p.error_message, p.output_path
			FROM package_task p
			LEFT JOIN media m ON m.id = p.media_id
			WHERE p.id = ?
			LIMIT 1
		`, id)
		if err := row2.Scan(&taskID, &fileID, &quality, &status, &prog, &errMsg, &out); err != nil {
			if err == sql.ErrNoRows {
				c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		fromPackage = true
	}

	base := "http://" + c.Request.Host
	if c.Request.TLS != nil {
		base = "https://" + c.Request.Host
	}

	var mediaID sql.NullInt64
	if fileID.Valid && fileID.String != "" {
		_ = h.App.DB.QueryRow(`SELECT id FROM media WHERE file_id = ? ORDER BY id DESC LIMIT 1`, fileID.String).Scan(&mediaID)
	}
	if !mediaID.Valid && fromPackage {
		_ = h.App.DB.QueryRow(`SELECT media_id FROM package_task WHERE id = ? LIMIT 1`, id).Scan(&mediaID)
	}
	if mediaID.Valid && mediaID.Int64 > 0 {
		if _, ok := h.requireMediaAccess(c, mediaID.Int64, false); !ok {
			return
		}
	}

	hlsMaster := ""
	ready := status.String == "done"
	// When running with at least one rendition complete, playback can start.
	if status.String == "running" && prog.Int64 >= 5 && out.Valid && strings.HasSuffix(strings.ToLower(out.String), ".m3u8") && mediaID.Valid {
		if st, err := os.Stat(out.String); err == nil && !st.IsDir() {
			ready = true
			hlsMaster = fmt.Sprintf("%s/api/v1/media/%d/hls/master.m3u8", base, mediaID.Int64)
		}
	}
	if status.String == "done" && out.Valid && strings.HasSuffix(strings.ToLower(out.String), ".m3u8") && mediaID.Valid {
		hlsMaster = fmt.Sprintf("%s/api/v1/media/%d/hls/master.m3u8", base, mediaID.Int64)
	}
	failed := status.String == "failed" || status.String == "cancelled"
	c.JSON(http.StatusOK, gin.H{
		"task_id":       taskID,
		"file_id":       fileID.String,
		"media_id":      mediaID.Int64,
		"quality":       quality.String,
		"status":        status.String,
		"progress":      prog.Int64,
		"error_message": errMsg.String,
		"ready":         ready,
		"failed":        failed,
		"hls_master":    hlsMaster,
		"output_path":   out.String,
		"poll_after_ms": func() int {
			if ready || failed {
				return 0
			}
			if prog.Int64 >= 80 {
				return 1200
			}
			return 2000
		}(),
	})
}
