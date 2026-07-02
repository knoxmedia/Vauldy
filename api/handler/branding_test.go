package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
)

func TestGetBrandingUsesConfigAppName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Branding.AppName = "My Media Hub"
	h := &Handler{App: &app.App{Config: cfg}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/branding", nil)
	h.GetBranding(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "My Media Hub") {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestServeBrandingFaviconCustomPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	icon := filepath.Join(dir, "icon.png")
	if err := os.WriteFile(icon, []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Branding.FaviconPath = icon
	h := &Handler{App: &app.App{Config: cfg, ConfigPath: filepath.Join(dir, "config.yml")}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	h.ServeBrandingFavicon(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if w.Body.String() != "png-bytes" {
		t.Fatalf("body=%q", w.Body.String())
	}
}
