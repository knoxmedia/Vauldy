package handler

import (
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/subtitle"
)

func (h *Handler) ListMediaSubtitles(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	items, err := h.Subtitle.List(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) SubtitleVTT(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	mid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	sid, err := strconv.ParseInt(c.Param("sid"), 10, 64)
	if err != nil || sid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid subtitle id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mid, true); !ok {
		return
	}
	p, err := h.Subtitle.VTTPath(mid, sid)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	p = filepath.Clean(p)
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "file missing"})
		return
	}
	if strings.EqualFold(strings.TrimSpace(c.Query("format")), "powerplayer") {
		content, err := h.Subtitle.ReadVTTContent(mid, sid, h.KeyVault)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		normalized, err := subtitle.NormalizeForPowerPlayer(content)
		if err != nil {
			log.Printf("subtitle powerplayer normalize media=%d sid=%d: %v", mid, sid, err)
			c.Header("Content-Type", "text/vtt; charset=utf-8")
			c.Header("Cache-Control", "private, no-store")
			_, _ = io.WriteString(c.Writer, content)
			return
		}
		c.Header("Content-Type", "text/vtt; charset=utf-8")
		c.Header("Cache-Control", "private, no-store")
		_, _ = io.WriteString(c.Writer, normalized)
		return
	}
	c.Header("Content-Type", "text/vtt; charset=utf-8")
	h.serveDerivedAsset(c, mid, p, "text/vtt; charset=utf-8")
}

func (h *Handler) ProcessMediaSubtitles(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.Subtitle.ProcessMedia(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
