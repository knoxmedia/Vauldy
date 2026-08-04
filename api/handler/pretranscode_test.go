package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/coreiface"
	"knox-media/internal/license"
	"knox-media/internal/pretranscode"
	"knox-media/internal/store"
)

// setupPretranscodeTest builds a Handler-equivalent gin engine with the
// pretranscode routes mounted and a valid license in place, so handler tests
// can exercise the full HTTP path.
func setupPretranscodeTest(t *testing.T) (*gin.Engine, *sql.DB, *pretranscode.Module) {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Seed a valid license.
	c := license.Claims{Pretranscode: true, Exp: time.Now().Add(72 * time.Hour).Unix()}
	licenseKey := license.IssueLicense("", c)
	opts := map[string]any{"license_key": licenseKey}
	raw, _ := json.Marshal(opts)
	_, _ = db.Exec(`INSERT INTO system_options (id, options_json) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET options_json=excluded.options_json`, string(raw))

	// Initialize the pretranscode module directly (bypassing init() globals).
	mod := pretranscode.NewModule()
	cfg := &config.Config{Data: config.DataConfig{Transcode: t.TempDir()}, FFmpeg: config.FFmpegConfig{FFmpegPath: "ffmpeg"}}
	deps := coreiface.ModuleDeps{DB: db, Config: cfg, TranscodeDir: cfg.Data.Transcode, FFmpegPath: "ffmpeg"}
	if err := mod.Init(context.Background(), deps); err != nil {
		t.Fatalf("module init: %v", err)
	}
	t.Cleanup(func() { _ = mod.Shutdown(context.Background()) })

	// Bind the license singleton to the test DB so RequireFeature passes.
	license.SetSingletonForTest(license.NewModuleWithDB(db))

	application := &app.App{DB: db, Config: cfg}
	h := &Handler{App: application}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	adm := r.Group("/api/v1")
	adm.Use(PretranscodeLicenseMiddleware())
	{
		adm.GET("/admin/settings/pretranscode-presets", h.ListPresets)
		adm.POST("/admin/settings/pretranscode-presets", h.CreatePreset)
		adm.GET("/admin/settings/pretranscode-presets/:id", h.GetPreset)
		adm.PUT("/admin/settings/pretranscode-presets/:id", h.UpdatePreset)
		adm.DELETE("/admin/settings/pretranscode-presets/:id", h.DeletePreset)
		adm.POST("/admin/settings/pretranscode-presets/:id/clone", h.ClonePreset)
		adm.PUT("/admin/settings/pretranscode-presets/:id/toggle", h.TogglePreset)
		adm.GET("/admin/settings/pretranscode-presets/:id/renditions", h.ListRenditions)
		adm.POST("/admin/settings/pretranscode-presets/:id/renditions", h.AddRendition)
		adm.PUT("/admin/settings/pretranscode-presets/:id/renditions/:rid", h.UpdateRendition)
		adm.DELETE("/admin/settings/pretranscode-presets/:id/renditions/:rid", h.DeleteRendition)

		adm.GET("/admin/transcode/tasks", h.ListUnifiedTasks)
		adm.POST("/admin/transcode/tasks", h.CreatePretranscodeTask)
		adm.POST("/admin/transcode/batch", h.CreateBatchPretranscodeTask)
		adm.GET("/admin/transcode/tasks/:id", h.GetPretranscodeTask)
		adm.DELETE("/admin/transcode/tasks/:id", h.DeletePretranscodeTask)
		adm.POST("/admin/transcode/tasks/:id/cancel", h.CancelPretranscodeTask)
		adm.POST("/admin/transcode/tasks/:id/retry", h.RetryPretranscodeTask)
		adm.POST("/admin/transcode/tasks/:id/pause", h.PausePretranscodeTask)
		adm.POST("/admin/transcode/tasks/:id/resume", h.ResumePretranscodeTask)
		adm.GET("/admin/transcode/tasks/:id/renditions", h.ListRenditionJobs)
		adm.POST("/admin/transcode/cleanup-failed", h.CleanupFailedPretranscodeTasks)
		adm.GET("/admin/transcode/storage", h.GetPretranscodeStorage)

		adm.GET("/admin/settings/pretranscode-webhooks", h.ListWebhooks)
		adm.POST("/admin/settings/pretranscode-webhooks", h.CreateWebhook)
		adm.PUT("/admin/settings/pretranscode-webhooks/:id", h.UpdateWebhook)
		adm.DELETE("/admin/settings/pretranscode-webhooks/:id", h.DeleteWebhook)

		adm.GET("/admin/transcode/cluster/nodes", h.ListClusterNodes)
		adm.GET("/admin/transcode/cluster/stats", h.GetClusterStats)
	}
	return r, db, mod
}

func TestAPI_ListPresetsIncludesBuiltin(t *testing.T) {
	r, _, _ := setupPretranscodeTest(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/pretranscode-presets", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []pretranscode.Preset `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) < 7 {
		t.Errorf("expected ≥7 builtin presets, got %d", len(resp.Items))
	}
}

func TestAPI_CreatePresetAndTask(t *testing.T) {
	r, db, _ := setupPretranscodeTest(t)
	mediaDir := t.TempDir()
	mediaPath := filepath.Join(mediaDir, "x.mp4")
	if err := os.WriteFile(mediaPath, []byte("mock video"), 0o644); err != nil {
		t.Fatalf("write media source: %v", err)
	}

	// Create a preset.
	body := `{"name":"api-test","output_format":"hls","video_codec":"libx264","audio_codec":"aac","audio_bitrate":"128k","renditions":[{"name":"720p","height":720,"video_bitrate":"2800k"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/pretranscode-presets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("create preset: %d %s", w.Code, w.Body.String())
	}
	var preset pretranscode.Preset
	_ = json.Unmarshal(w.Body.Bytes(), &preset)

	// Seed media.
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1,'L','video',?)`, mediaDir)
	_, _ = db.Exec(`INSERT INTO media (library_id, file_id, title, file_path, file_type, duration, height) VALUES (1,'fid-api','T',?,'video',120,720)`, mediaPath)

	// Create task.
	taskBody := `{"media_ids":[1],"preset_id":` + jsonInt(preset.ID) + `,"priority":"normal"}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/transcode/tasks", bytes.NewReader([]byte(taskBody)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("create task: %d %s", w.Code, w.Body.String())
	}

	// List unified tasks (pretranscode filter).
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/transcode/tasks?type=pretranscode", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("list tasks: %d %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Items []pretranscode.UnifiedTask `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Items) != 1 || listResp.Items[0].TaskType != "pretranscode" {
		t.Errorf("pretranscode task missing: %+v", listResp.Items)
	}
}

func TestAPI_LicenseGateRejectsWhenUnlicensed(t *testing.T) {
	origEdition := config.Edition
	config.Edition = "community"
	defer func() { config.Edition = origEdition }()

	db, _ := store.OpenSQLite(":memory:")
	defer db.Close()
	// No license key set.
	license.SetSingletonForTest(license.NewModuleWithDB(db))
	cfg := &config.Config{Data: config.DataConfig{Transcode: t.TempDir()}}
	application := &app.App{DB: db, Config: cfg}
	h := &Handler{App: application}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	adm := r.Group("/api/v1")
	adm.Use(PretranscodeLicenseMiddleware())
	adm.GET("/admin/settings/pretranscode-presets", h.ListPresets)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/pretranscode-presets", nil)
	r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("expected 403 without license, got %d", w.Code)
	}
}

func TestAPI_ClusterNodesStub(t *testing.T) {
	r, _, _ := setupPretranscodeTest(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/transcode/cluster/nodes", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Nodes []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"nodes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Nodes) != 1 || resp.Nodes[0].ID != "self" || resp.Nodes[0].Status != "online" {
		t.Errorf("cluster stub wrong: %+v", resp)
	}
}

func TestAPI_WebhookCRUD(t *testing.T) {
	r, _, _ := setupPretranscodeTest(t)
	body := `{"name":"wh","url":"http://example.com","events":["task.completed"],"is_enabled":true}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/pretranscode-webhooks", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("create webhook: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/pretranscode-webhooks", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("list webhooks: %d", w.Code)
	}
	var resp struct {
		Items []pretranscode.Webhook `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 1 {
		t.Errorf("expected 1 webhook, got %d", len(resp.Items))
	}
}

func TestAPI_PresetMP4RejectsEncryption(t *testing.T) {
	r, _, _ := setupPretranscodeTest(t)
	// Request MP4 + aes128 — server must coerce to none.
	body := `{"name":"mp4-enc","output_format":"mp4","encryption_mode":"aes128","video_codec":"libx264","audio_codec":"aac","audio_bitrate":"128k","renditions":[{"name":"720p","height":720,"video_bitrate":"2800k"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/pretranscode-presets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var p pretranscode.Preset
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.EncryptionMode != "none" {
		t.Errorf("MP4 preset must force encryption_mode=none, got %s", p.EncryptionMode)
	}
}

// jsonInt formats an int64 as a JSON number string.
func jsonInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
