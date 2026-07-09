package handler

import (
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/gin-gonic/gin"

	"knox-media/internal/coreiface"
	"knox-media/internal/jit/hwenc"
	"knox-media/internal/license"
	"knox-media/internal/pretranscode"
)

// pretranscodeModule returns the commercial pretranscode module bound via
// coreiface.PretranscodeMod. Returns nil in the community build (no routes
// should be registered in that case).
func pretranscodeModule() *pretranscode.Module {
	if coreiface.PretranscodeMod == nil {
		return nil
	}
	// The playback service is owned by the module; recover the module via the
	// global init() registration in internal/pretranscode/module.go. We cast
	// through a package-level accessor.
	return pretranscode.ActiveModule()
}

// PretranscodeLicenseMiddleware exposes the license gate so router and tests
// in the handler package can apply it consistently.
func PretranscodeLicenseMiddleware() gin.HandlerFunc {
	return license.RequireFeature("pretranscode")
}

// --- Preset handlers (SRS 5.1) ---

func (h *Handler) ListPresets(c *gin.Context) {
	mod := pretranscodeModule()
	if mod == nil || mod.Preset == nil {
		c.JSON(503, gin.H{"error": "pretranscode module not available"})
		return
	}
	presets, err := mod.Preset.ListPresets()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"items": presets})
}

func (h *Handler) GetPreset(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	p, err := mod.Preset.GetPreset(id)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, p)
}

func (h *Handler) CreatePreset(c *gin.Context) {
	var in pretranscode.CreatePresetInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	p, err := mod.Preset.CreatePreset(in)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, p)
}

func (h *Handler) UpdatePreset(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var in pretranscode.CreatePresetInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	p, err := mod.Preset.UpdatePreset(id, in)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, p)
}

func (h *Handler) DeletePreset(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Preset.DeletePreset(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) ClonePreset(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var body struct{ Name string `json:"name"` }
	_ = c.ShouldBindJSON(&body)
	mod := pretranscodeModule()
	p, err := mod.Preset.ClonePreset(id, body.Name)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, p)
}

func (h *Handler) TogglePreset(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	enabled, err := mod.Preset.TogglePreset(id)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "is_enabled": enabled})
}

func (h *Handler) ListRenditions(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	renditions, err := mod.Preset.ListRenditions(id)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"items": renditions})
}

func (h *Handler) AddRendition(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var r pretranscode.Rendition
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	out, err := mod.Preset.AddRendition(id, r)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, out)
}

func (h *Handler) UpdateRendition(c *gin.Context) {
	pid, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	rid, _ := strconv.ParseInt(c.Param("rid"), 10, 64)
	var r pretranscode.Rendition
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	out, err := mod.Preset.UpdateRendition(pid, rid, r)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, out)
}

func (h *Handler) DeleteRendition(c *gin.Context) {
	pid, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	rid, _ := strconv.ParseInt(c.Param("rid"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Preset.DeleteRendition(pid, rid); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) SortRenditions(c *gin.Context) {
	pid, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var body struct{ OrderedIDs []int64 `json:"ordered_ids"` }
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	if err := mod.Preset.SortRenditions(pid, body.OrderedIDs); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// --- Task handlers (SRS 5.2) ---

func (h *Handler) CreatePretranscodeTask(c *gin.Context) {
	var body struct {
		MediaIDs []int64 `json:"media_ids"`
		PresetID int64   `json:"preset_id"`
		Priority string  `json:"priority"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	ids, err := mod.Task.CreateTask(body.MediaIDs, body.PresetID, body.Priority)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, gin.H{"task_ids": ids})
}

func (h *Handler) CreateBatchPretranscodeTask(c *gin.Context) {
	var body struct {
		LibraryID int64  `json:"library_id"`
		PresetID  int64  `json:"preset_id"`
		Filter    string `json:"filter"`
		Priority  string `json:"priority"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	n, err := mod.Task.CreateBatchTask(body.LibraryID, body.PresetID, body.Filter, body.Priority)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, gin.H{"created": n})
}

func (h *Handler) ListUnifiedTasks(c *gin.Context) {
	taskType := c.DefaultQuery("type", "all")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	mod := pretranscodeModule()
	tasks, err := mod.Task.ListTasks(taskType, limit)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"items": tasks})
}

func (h *Handler) GetPretranscodeTask(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	task, jobs, err := mod.Task.GetTask(id)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"task": task, "renditions": jobs})
}

func (h *Handler) CancelPretranscodeTask(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Task.CancelTask(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) RetryPretranscodeTask(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Task.RetryTask(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) DeletePretranscodeTask(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Task.DeleteTask(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) PausePretranscodeTask(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Task.PauseTask(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) ResumePretranscodeTask(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Task.ResumeTask(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) ListRenditionJobs(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	jobs, err := mod.Task.ListRenditionJobs(id)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"items": jobs})
}

func (h *Handler) CancelRenditionJob(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	mod := pretranscodeModule()
	// Best-effort signal running worker.
	mod.Worker.CancelRendition(id)
	if err := mod.Task.CancelRenditionJob(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) RetryRenditionJob(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Task.RetryRenditionJob(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) CleanupFailedPretranscodeTasks(c *gin.Context) {
	var body struct{ Days int `json:"days"` }
	_ = c.ShouldBindJSON(&body)
	mod := pretranscodeModule()
	n, err := mod.Task.CleanupFailedTasks(body.Days)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"deleted": n})
}

func (h *Handler) GetPretranscodeStorage(c *gin.Context) {
	mod := pretranscodeModule()
	stats, err := mod.Task.GetStorageStats()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, stats)
}

func (h *Handler) CleanupPretranscodeOutputs(c *gin.Context) {
	var body struct{ FileID string `json:"file_id"` }
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	if err := mod.Task.CleanupOutputs(body.FileID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// --- Webhook handlers (SRS 5.3) ---

func (h *Handler) ListWebhooks(c *gin.Context) {
	mod := pretranscodeModule()
	whs, err := mod.Webhook.ListWebhooks()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"items": whs})
}

func (h *Handler) CreateWebhook(c *gin.Context) {
	var w pretranscode.Webhook
	if err := c.ShouldBindJSON(&w); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	if err := mod.Webhook.CreateWebhook(&w); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, w)
}

func (h *Handler) UpdateWebhook(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var w pretranscode.Webhook
	if err := c.ShouldBindJSON(&w); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	if err := mod.Webhook.UpdateWebhook(id, &w); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, w)
}

func (h *Handler) DeleteWebhook(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Webhook.DeleteWebhook(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) TestWebhook(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	if err := mod.Webhook.TestWebhook(id); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) ListWebhookLogs(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	mod := pretranscodeModule()
	logs, err := mod.Webhook.ListLogs(id, limit)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"items": logs})
}

// --- Cluster stubs (SRS 5.4, single-node in this build) ---

func (h *Handler) ListClusterNodes(c *gin.Context) {
	// Standalone mode: a single self node.
	c.JSON(200, gin.H{
		"nodes": []gin.H{{
			"id":        "self",
			"host":      "localhost",
			"status":    "online",
			"hardware_encoders": h.App.AvailableHardwareAcceleration,
			"current_tasks": 0,
			"max_concurrent": 4,
		}},
		"queue_depth": 0,
		"total_active_tasks": 0,
	})
}

func (h *Handler) GetClusterStats(c *gin.Context) {
	c.JSON(200, gin.H{
		"nodes":          1,
		"online":         1,
		"queue_depth":    0,
		"active_tasks":   0,
		"mode":           "standalone",
	})
}

// --- Encoder list (for admin UI codec dropdown) ---

func (h *Handler) ListAvailableEncoders(c *gin.Context) {
	encoders := hwenc.ListAvailableEncoders(h.App.Config.FFmpeg.FFmpegPath)
	c.JSON(200, gin.H{"encoders": encoders})
}

// --- HLS Info enhancement (SRS 5.5, PLAY-05) ---

// GetPretranscodeHLSInfo returns the pretranscode status for a media file.
// Mounted under /api/v1/media/:id/pretranscode/info.
func (h *Handler) GetPretranscodeHLSInfo(c *gin.Context) {
	mediaID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var fileID string
	err := h.App.DB.QueryRow(`SELECT file_id FROM media WHERE id = ?`, mediaID).Scan(&fileID)
	if err == sql.ErrNoRows {
		c.JSON(404, gin.H{"error": "media not found"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if coreiface.PretranscodeMod == nil {
		c.JSON(200, gin.H{"available": false})
		return
	}
	status, err := coreiface.PretranscodeMod.GetPretranscodeStatus(c.Request.Context(), fileID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	out, _ := json.Marshal(status)
	c.Data(200, "application/json", out)
}

// --- Media Optimization handlers (SRS 5.6) ---

// GetMediaOptimization returns the optimization status for a media item.
// Mounted under /api/v1/media/:id/optimization.
func (h *Handler) GetMediaOptimization(c *gin.Context) {
	mediaID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	mod := pretranscodeModule()
	if mod == nil {
		c.JSON(503, gin.H{"error": "pretranscode module not available"})
		return
	}
	status, err := mod.Task.GetMediaOptimizationStatus(mediaID)
	if err != nil {
		if err == pretranscode.ErrMediaNotFound {
			c.JSON(404, gin.H{"error": err.Error()})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, status)
}

// CreateMediaOptimization creates a new optimization task for a media item.
// Mounted under /api/v1/media/:id/optimization.
func (h *Handler) CreateMediaOptimization(c *gin.Context) {
	mediaID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var body struct {
		PresetID      int64 `json:"preset_id"`
		ExcludeExisting bool  `json:"exclude_existing"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	if mod == nil {
		c.JSON(503, gin.H{"error": "pretranscode module not available"})
		return
	}
	if taskID, err := mod.Task.RetryLatestFailedTaskForMedia(mediaID); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	} else if taskID > 0 {
		c.JSON(201, gin.H{"task_ids": []int64{taskID}, "retried": true})
		return
	}
	ids, err := mod.Task.CreateTask([]int64{mediaID}, body.PresetID, "normal")
	if err != nil {
		if err == pretranscode.ErrPlaintextSourceUnavailable {
			c.JSON(400, gin.H{"error": "plaintext source unavailable"})
			return
		}
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, gin.H{"task_ids": ids})
}

// RemoveOptimizationRendition removes a single optimized rendition.
// Mounted under /api/v1/media/:id/optimization/renditions/:rid.
func (h *Handler) RemoveOptimizationRendition(c *gin.Context) {
	renditionJobID, _ := strconv.ParseInt(c.Param("rid"), 10, 64)
	mod := pretranscodeModule()
	if mod == nil {
		c.JSON(503, gin.H{"error": "pretranscode module not available"})
		return
	}
	if err := mod.Task.RemoveRenditionJob(renditionJobID); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// BatchRemoveOptimizationRenditions removes multiple optimized renditions.
// Mounted under /api/v1/media/:id/optimization/renditions.
func (h *Handler) BatchRemoveOptimizationRenditions(c *gin.Context) {
	var body struct {
		RenditionJobIDs []int64 `json:"rendition_job_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	mod := pretranscodeModule()
	if mod == nil {
		c.JSON(503, gin.H{"error": "pretranscode module not available"})
		return
	}
	if err := mod.Task.BatchRemoveRenditionJobs(body.RenditionJobIDs); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
