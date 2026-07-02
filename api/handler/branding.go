package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/branding"
	"knox-media/internal/config"
)

// GetBranding returns public UI branding (app name, favicon URL). No auth required.
func (h *Handler) GetBranding(c *gin.Context) {
	appName := branding.DefaultAppName
	if h != nil && h.App != nil && h.App.Config != nil {
		appName = h.App.Config.BrandingAppName()
	}
	c.JSON(http.StatusOK, gin.H{
		"app_name":    appName,
		"favicon_url": "/favicon.svg",
	})
}

// ServeBrandingFavicon serves the configured favicon or the bundled default SVG.
func (h *Handler) ServeBrandingFavicon(c *gin.Context) {
	if h != nil && h.App != nil && h.App.Config != nil {
		if p := h.App.Config.ResolveBrandingFaviconPath(h.App.ConfigPath); p != "" {
			c.Header("Cache-Control", "public, max-age=3600")
			c.File(p)
			return
		}
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, "image/svg+xml", branding.DefaultFaviconSVG)
}

// PutBranding updates branding in config.yml (admin).
func (h *Handler) PutBranding(c *gin.Context) {
	if h == nil || h.App == nil || h.App.Config == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config unavailable"})
		return
	}
	cfgPath := strings.TrimSpace(h.App.ConfigPath)
	if cfgPath == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config path unknown"})
		return
	}
	var body struct {
		AppName     string `json:"app_name"`
		FaviconPath string `json:"favicon_path"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	appName := strings.TrimSpace(body.AppName)
	if appName == "" {
		appName = branding.DefaultAppName
	}
	faviconPath := strings.TrimSpace(body.FaviconPath)
	if err := config.SaveBranding(cfgPath, config.BrandingConfig{
		AppName:     appName,
		FaviconPath: faviconPath,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.App.Config.Branding.AppName = appName
	h.App.Config.Branding.FaviconPath = faviconPath
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"app_name":    appName,
		"favicon_path": faviconPath,
		"favicon_url": "/favicon.svg",
	})
}
