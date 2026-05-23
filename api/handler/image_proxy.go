package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/metadatalib"
)

func (h *Handler) ProxyRemoteImage(c *gin.Context) {
	raw := strings.TrimSpace(c.Query("url"))
	if raw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url required"})
		return
	}
	if !metadatalib.ProxyAllowedHost(raw) {
		c.JSON(http.StatusForbidden, gin.H{"error": "host not allowed"})
		return
	}
	if err := metadatalib.StreamRemoteImage(c.Writer, raw); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
}
