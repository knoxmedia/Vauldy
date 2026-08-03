package handler

import (
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"

	"knox-media/internal/documenttask"
	"knox-media/internal/doctrans"
)

// ServeDocumentPreviewTask serves the committed preview PDF or idempotently
// enqueues a conversion task and returns 202 with a status URL.
func (h *Handler) ServeDocumentPreviewTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}

	store := documenttask.NewStore(h.App.DB)

	// Check if committed output exists.
	task, err := store.GetByMediaID(c.Request.Context(), id)
	if err == nil && task.Status == documenttask.StatusDone && task.OutputPath != "" {
		// Serve the committed PDF.
		if st, statErr := os.Stat(task.OutputPath); statErr == nil && !st.IsDir() && st.Size() > 0 {
			h.serveDerivedAsset(c, id, task.OutputPath, "application/pdf")
			return
		}
	}

	// Enqueue or return existing task.
	path, format, _, loadErr := h.loadDocumentSource(id)
	if loadErr != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": loadErr.Error()})
		return
	}
	if !doctrans.IsOfficeDocument(path, format) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "preview conversion not supported for this format"})
		return
	}

	var generation int64
	h.App.DB.QueryRow(`SELECT COALESCE(ingest_generation,0) FROM media WHERE id=?`, id).Scan(&generation)

	_, err = store.Enqueue(c.Request.Context(), id, path, generation)
	if err != nil {
		if _, isDup := err.(documenttask.DuplicateError); isDup {
			// Already done - try to serve.
			task, _ := store.GetByMediaID(c.Request.Context(), id)
			if task != nil && task.OutputPath != "" {
				if st, statErr := os.Stat(task.OutputPath); statErr == nil && !st.IsDir() && st.Size() > 0 {
					h.serveDerivedAsset(c, id, task.OutputPath, "application/pdf")
					return
				}
			}
		}
	}

	statusURL := fmt.Sprintf("/api/v1/media/%d/document/task/status", id)
	c.Header("Retry-After", "5")
	c.JSON(http.StatusAccepted, gin.H{
		"status":     "enqueued",
		"status_url": statusURL,
		"message":    "document conversion has been queued",
	})
}

// DocumentTaskStatus returns the current status of a document conversion task.
func (h *Handler) DocumentTaskStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}

	store := documenttask.NewStore(h.App.DB)
	task, err := store.GetByMediaID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "not_queued"})
		return
	}

	resp := gin.H{
		"status":     string(task.Status),
		"media_id":   task.MediaID,
		"created_at": task.CreatedAt,
	}
	if task.Status == documenttask.StatusDone {
		resp["preview_url"] = fmt.Sprintf("/api/v1/media/%d/document/preview.pdf", id)
	}
	if task.LastError != "" {
		resp["error"] = task.LastError
	}
	if task.OutputSize > 0 {
		resp["output_size"] = task.OutputSize
	}
	if task.PageCount > 0 {
		resp["page_count"] = task.PageCount
	}
	if task.StartedAt != nil {
		resp["started_at"] = task.StartedAt
	}
	if task.FinishedAt != nil {
		resp["finished_at"] = task.FinishedAt
	}

	c.JSON(http.StatusOK, resp)
}

// EnqueueDocumentConvert idempotently enqueues a document conversion task.
func (h *Handler) EnqueueDocumentConvert(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}

	store := documenttask.NewStore(h.App.DB)

	path, format, _, loadErr := h.loadDocumentSource(id)
	if loadErr != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": loadErr.Error()})
		return
	}
	if !doctrans.IsOfficeDocument(path, format) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversion not supported for this format"})
		return
	}

	var generation int64
	h.App.DB.QueryRow(`SELECT COALESCE(ingest_generation,0) FROM media WHERE id=?`, id).Scan(&generation)

	task, err := store.Enqueue(c.Request.Context(), id, path, generation)
	if err != nil {
		if _, isDup := err.(documenttask.DuplicateError); isDup {
			c.JSON(http.StatusOK, gin.H{
				"status":     "already_converted",
				"media_id":   id,
				"task_id":    task.ID,
				"status_url": fmt.Sprintf("/api/v1/media/%d/document/task/status", id),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	statusURL := fmt.Sprintf("/api/v1/media/%d/document/task/status", id)
	_ = task
	c.Header("Retry-After", "5")
	c.JSON(http.StatusAccepted, gin.H{
		"status":     string(task.Status),
		"task_id":    task.ID,
		"status_url": statusURL,
		"message":    "document conversion enqueued",
	})
}

// EnqueueDocumentFulltext idempotently enqueues a document full-text extraction task.
func (h *Handler) EnqueueDocumentFulltext(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}

	path, format, _, loadErr := h.loadDocumentSource(id)
	if loadErr != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": loadErr.Error()})
		return
	}

	var generation int64
	h.App.DB.QueryRow(`SELECT COALESCE(ingest_generation,0) FROM media WHERE id=?`, id).Scan(&generation)

	store := documenttask.NewStore(h.App.DB)
	store.EnsureFulltextSchema(c.Request.Context())

	input := documenttask.FulltextInput{
		MediaID:      id,
		SourcePath:   path,
		Generation:   generation,
		Language:     "eng",
		MaxPages:     100,
		MaxBytes:     50 * 1024 * 1024,
		DocumentKind: format,
	}
	_, err = store.EnqueueFulltext(c.Request.Context(), input)
	if err != nil {
		if _, isDup := err.(documenttask.DuplicateError); isDup {
			c.JSON(http.StatusOK, gin.H{
				"status":     "already_processed",
				"media_id":   id,
				"status_url": fmt.Sprintf("/api/v1/media/%d/document/task/status", id),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	statusURL := fmt.Sprintf("/api/v1/media/%d/document/task/status", id)
	c.Header("Retry-After", "5")
	c.JSON(http.StatusAccepted, gin.H{
		"status":     "enqueued",
		"status_url": statusURL,
		"message":    "document fulltext extraction queued",
	})
}
