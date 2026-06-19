package handler

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/subtitle"
)

type subtitleTranslateBody struct {
	Src        string `json:"src"`
	Lang       string `json:"lang"`
	TargetLang string `json:"targetLang"`
	Srclang    string `json:"srclang"`
}

// TranslateSubtitle POST /api/v1/subtitles/translate — PowerPlayer-compatible auto translate API.
func (h *Handler) TranslateSubtitle(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	var body subtitleTranslateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	target := strings.TrimSpace(body.TargetLang)
	if target == "" {
		target = strings.TrimSpace(body.Lang)
	}
	if target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "targetLang required"})
		return
	}
	srcLang := strings.TrimSpace(body.Srclang)
	if srcLang == "" {
		srcLang = strings.TrimSpace(body.Lang)
	}

	content, ok := h.loadSubtitleContentForTranslate(c, strings.TrimSpace(body.Src))
	if !ok {
		return
	}
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty subtitle content"})
		return
	}

	providers := h.loadEnabledAIProviders()
	out, err := subtitle.TranslateContent(c.Request.Context(), content, srcLang, target, providers)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (h *Handler) loadSubtitleContentForTranslate(c *gin.Context, src string) (string, bool) {
	if src == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "src required"})
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(src), "blob:") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "blob subtitle URL is not supported; use Knox subtitle URL"})
		return "", false
	}

	if mid, sid, ok := subtitle.ParseMediaSubtitleVTTURL(src); ok {
		if _, allowed := h.requireMediaAccess(c, mid, true); !allowed {
			return "", false
		}
		content, err := h.Subtitle.ReadVTTContent(mid, sid, h.KeyVault)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "subtitle not found"})
			return "", false
		}
		return content, true
	}

	u, err := url.Parse(src)
	if err != nil || u.Scheme == "" || u.Host == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid subtitle src"})
		return "", false
	}
	reqHost := strings.ToLower(strings.TrimSpace(c.Request.Host))
	if host := strings.ToLower(u.Hostname()); host != "" && reqHost != "" {
		reqHostName := reqHost
		if i := strings.Index(reqHostName, ":"); i >= 0 {
			reqHostName = reqHostName[:i]
		}
		if host != reqHostName && host != "127.0.0.1" && host != "localhost" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "external subtitle URL not allowed"})
			return "", false
		}
	}
	if mid, sid, ok := subtitle.ParseMediaSubtitleVTTURL(u.Path); ok {
		if _, allowed := h.requireMediaAccess(c, mid, true); !allowed {
			return "", false
		}
		content, err := h.Subtitle.ReadVTTContent(mid, sid, h.KeyVault)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "subtitle not found"})
			return "", false
		}
		return content, true
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported subtitle src"})
	return "", false
}
