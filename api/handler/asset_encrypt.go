package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"knox-media/internal/storage"
)

// KickEncryptMediaAsset encrypts media at rest when the library has encrypted_assets_enabled.
func (h *Handler) KickEncryptMediaAsset(mediaID int64) {
	if h == nil || h.App == nil {
		return
	}
	storage.KickEncryptMedia(h.AssetEncryptor, h.App.Config, mediaID)
}

// EncryptMediaAssets queues on-demand envelope encryption for one media item.
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
	var filePath string
	if err := h.App.DB.QueryRow(`SELECT file_path FROM media WHERE id = ?`, id).Scan(&filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}
	if storage.IsMediaEncrypted(h.App.DB, id, filePath) {
		c.JSON(http.StatusConflict, gin.H{"error": "already encrypted"})
		return
	}
	storage.KickEncryptMediaManual(h.AssetEncryptor, h.App.Config, id)
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "status": "queued"})
}
