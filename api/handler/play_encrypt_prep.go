package handler

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/jit/session"
)

func (h *Handler) ensureEncryptedISOPipePlayback(c *gin.Context, mediaID int64, sourcePath string) bool {
	if h == nil || h.AssetEncryptor == nil || mediaID <= 0 {
		return true
	}
	ctx := c.Request.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Hour)
	defer cancel()
	if err := h.AssetEncryptor.EnsureEncryptedISOPipePlayback(ctx, mediaID, sourcePath); err != nil {
		log.Printf("encrypted iso pipe prep media=%d: %v", mediaID, err)
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "encrypted media not ready for streaming playback",
			"message": err.Error(),
		})
		return false
	}
	return true
}

func (h *Handler) createJITSession(c *gin.Context, mediaID int64, fileID, sourcePath, bitrate, resolution string, duration float64) (*session.Session, error) {
	if h == nil || h.SessionManager == nil {
		return nil, fmt.Errorf("jit session manager not configured")
	}
	if !h.ensureEncryptedISOPipePlayback(c, mediaID, sourcePath) {
		return nil, fmt.Errorf("encrypted pipe prep declined")
	}
	if !h.instantTranscodeSlotsAvailable() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "instant transcode capacity reached",
			"message": "服务器实时转码并发已达上限，请稍后重试",
		})
		return nil, fmt.Errorf("instant transcode capacity reached")
	}
	return h.SessionManager.CreateSession(mediaID, fileID, sourcePath, bitrate, resolution, duration)
}
