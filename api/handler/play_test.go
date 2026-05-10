package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func TestHLSInfo_DRM_DoesNotExposeWidevineProxyFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "play-contract.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mkv')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, output_path) VALUES (1, 'cmaf_drm', 'done', 'E:/transcode/1/master.m3u8')`); err != nil {
		t.Fatalf("insert package task: %v", err)
	}

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	drmRaw, ok := payload["drm"]
	if !ok {
		t.Fatalf("expected drm payload in response: %s", w.Body.String())
	}
	drm, ok := drmRaw.(map[string]any)
	if !ok {
		t.Fatalf("expected drm object, got %T", drmRaw)
	}
	if got := payload["mode"]; got != "hls_drm" {
		t.Fatalf("unexpected mode: %v", got)
	}
	if got := payload["status"]; got != "done" {
		t.Fatalf("unexpected status: %v", got)
	}
	if got := payload["hls_master"]; got != "http://example.com/api/v1/media/1/hls/master.m3u8" {
		t.Fatalf("unexpected hls_master: %v", got)
	}
	if got := payload["fallback"]; got != "http://example.com/api/v1/media/1/play" {
		t.Fatalf("unexpected fallback: %v", got)
	}
	ppRaw, ok := payload["powerplayer"]
	if !ok {
		t.Fatalf("expected powerplayer in response: %s", w.Body.String())
	}
	pp, ok := ppRaw.(map[string]any)
	if !ok {
		t.Fatalf("expected powerplayer object, got %T", ppRaw)
	}
	if got := pp["base_url"]; got != "/static/powerplayer6" {
		t.Fatalf("unexpected powerplayer.base_url: %v", got)
	}
	if got := pp["skin"]; got != "skin.zip" {
		t.Fatalf("unexpected powerplayer.skin: %v", got)
	}
	if got := pp["client_cert"]; got != "powerplayer" {
		t.Fatalf("unexpected powerplayer.client_cert: %v", got)
	}
	engRaw, ok := payload["player_engine_order"]
	if !ok {
		t.Fatalf("expected player_engine_order: %s", w.Body.String())
	}
	eng, ok := engRaw.([]any)
	if !ok || len(eng) != 3 {
		t.Fatalf("expected player_engine_order len 3, got %T %v", engRaw, engRaw)
	}
	if g0, g1, g2 := fmt.Sprint(eng[0]), fmt.Sprint(eng[1]), fmt.Sprint(eng[2]); g0 != "powerplayer" || g1 != "shaka" || g2 != "xgplayer" {
		t.Fatalf("unexpected player_engine_order: %v %v %v", g0, g1, g2)
	}
	if _, exists := drm["widevine_raw_proxy"]; exists {
		t.Fatalf("unexpected widevine_raw_proxy in drm payload: %s", w.Body.String())
	}
	if got := drm["widevine_license_url"]; got != "http://example.com/api/v1/drm/widevine/license" {
		t.Fatalf("unexpected widevine_license_url: %v", got)
	}
	if got := drm["fairplay_cert_url"]; got != "http://example.com/api/v1/drm/fairplay/cert" {
		t.Fatalf("unexpected fairplay_cert_url: %v", got)
	}
	if got := drm["fairplay_license_url"]; got != "http://example.com/api/v1/drm/fairplay/license" {
		t.Fatalf("unexpected fairplay_license_url: %v", got)
	}
}
