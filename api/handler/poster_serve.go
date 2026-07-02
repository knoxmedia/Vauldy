package handler

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/storage"
)

// ServeMediaPoster serves a locally captured video poster (encrypted or plaintext).
func (h *Handler) ServeMediaPoster(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	var fileType sql.NullString
	if err := h.App.DB.QueryRow(`SELECT file_type FROM media WHERE id = ?`, id).Scan(&fileType); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if fileType.String != "video" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not a video"})
		return
	}
	uploadDir := ""
	if h.App != nil && h.App.Config != nil {
		uploadDir = h.App.Config.Data.Upload
	}
	target := storage.ResolvePosterServePath(h.App.DB, uploadDir, id)
	if target == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "poster missing"})
		return
	}
	if h.DerivedStore != nil && storage.NeedsDerivedEncryption(h.App.DB, id) && !kcrypto.IsEncFile(target) {
		if encPath, encErr := h.DerivedStore.FinalizePath(c.Request.Context(), id, "poster", "poster.jpg", target); encErr == nil {
			target = encPath
		}
	}
	h.serveDerivedAsset(c, id, target, "image/jpeg")
}
