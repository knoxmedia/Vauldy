package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/store"
)

func TestCreateLibraryPersistsDRMFlags(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "handler.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	widevineEnabled := true
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{
		DRM: config.DRMConfig{
			Widevine: config.WidevineConfig{Enabled: &widevineEnabled},
			PowerDRM: config.PowerDRMConfig{Enabled: true},
		},
	}}, runningScans: map[int64]scanRuntime{}}

	body := map[string]any{
		"name":                               "Movies",
		"type":                               "movie",
		"folders":                            []string{"E:/videos/movies"},
		"drm_enabled":                        1,
		"encryption_mode":                    "powerdrm",
		"cleanup_local_source_after_package": 1,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/libraries", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.CreateLibrary(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var gotDRM int
	var gotMode string
	var gotCleanup int
	if err := db.QueryRow(`SELECT drm_enabled, encryption_mode, cleanup_local_source_after_package FROM library ORDER BY id DESC LIMIT 1`).
		Scan(&gotDRM, &gotMode, &gotCleanup); err != nil {
		t.Fatalf("query flags: %v", err)
	}
	if gotDRM != 1 || gotMode != "powerdrm" || gotCleanup != 1 {
		t.Fatalf("unexpected flags drm=%d mode=%s cleanup=%d", gotDRM, gotMode, gotCleanup)
	}
}
