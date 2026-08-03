package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/postingest"
	"knox-media/internal/storage"
)

type encryptTaskJSON struct {
	ID          int64  `json:"id"`
	MediaID     int64  `json:"media_id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts"`
	LastError   string `json:"last_error"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	AvailableAt string `json:"available_at,omitempty"`
	LeaseOwner  string `json:"lease_owner,omitempty"`
	LeaseUntil  string `json:"lease_until,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

func (h *Handler) ListEncryptTasks(c *gin.Context) {
	if h.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "post-ingest queue not configured"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	status := c.DefaultQuery("status", "all")
	includeRemoved := strings.EqualFold(c.DefaultQuery("include_removed", "0"), "1") ||
		strings.EqualFold(c.DefaultQuery("include_removed", ""), "true")
	rows, err := h.Queue.ListEncrypt(c.Request.Context(), status, limit, includeRemoved)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]encryptTaskJSON, 0, len(rows))
	for _, r := range rows {
		items = append(items, encryptTaskJSON{
			ID: r.ID, MediaID: r.MediaID, Title: r.Title, Status: string(r.Status),
			Attempts: r.Attempts, MaxAttempts: r.MaxAttempts, LastError: r.LastError,
			StartedAt: nullString(r.StartedAt), FinishedAt: nullString(r.FinishedAt), AvailableAt: nullString(r.AvailableAt),
			LeaseOwner: nullString(r.LeaseOwner), LeaseUntil: nullString(r.LeaseUntil), UpdatedAt: nullString(r.UpdatedAt),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) CancelEncryptTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task id"})
		return
	}
	if h.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "post-ingest queue not configured"})
		return
	}
	if h.Dispatcher != nil {
		h.Dispatcher.CancelTask(id)
	}
	if err := h.Queue.AdminCancelEncrypt(c.Request.Context(), id); err != nil {
		// Soft cancel may already have terminalized the row; treat "cannot be cancelled" after soft as OK when not found racing.
		if strings.Contains(err.Error(), "cannot be cancel") {
			var status postingest.Status
			if qerr := h.App.DB.QueryRowContext(c.Request.Context(), `SELECT status FROM post_ingest_task WHERE id=? AND task_type='encrypt'`, id).Scan(&status); qerr == nil {
				if status == postingest.StatusCancelled || status == postingest.StatusFailed || status == postingest.StatusDone {
					c.JSON(http.StatusOK, gin.H{"ok": true, "status": string(status)})
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
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "cancelled"})
}

func (h *Handler) ResetEncryptTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task id"})
		return
	}
	if h.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "post-ingest queue not configured"})
		return
	}
	if err := h.Queue.AdminResetEncrypt(c.Request.Context(), id, middleware.UserID(c)); err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "waiting"})
}

func (h *Handler) DeleteEncryptTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task id"})
		return
	}
	if h.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "post-ingest queue not configured"})
		return
	}
	if err := h.Queue.AdminRemoveEncrypt(c.Request.Context(), id, middleware.UserID(c)); err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if strings.Contains(err.Error(), "running") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// EncryptMediaAssets enqueues on-demand envelope encryption onto the post-ingest queue.
func (h *Handler) EncryptMediaAssets(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	if h.AssetEncryptor == nil || h.App == nil || !h.App.Config.EncryptedAssetsEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "encrypted assets not configured"})
		return
	}
	if h.Queue == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "post-ingest queue not configured"})
		return
	}
	var filePath string
	if err := h.App.DB.QueryRow(`SELECT file_path FROM media WHERE id = ?`, id).Scan(&filePath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if storage.IsMediaEncrypted(h.App.DB, id, filePath) {
		c.JSON(http.StatusConflict, gin.H{"error": "already encrypted"})
		return
	}
	taskID, already, err := h.Queue.EnqueueEncryptManual(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	status := "queued"
	if already {
		status = "already_queued"
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "status": status, "task_id": taskID})
}
