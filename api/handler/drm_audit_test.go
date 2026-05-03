package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func TestListDRMLicenseAuditsWithFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "drm-audit.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4')`)
	_, _ = db.Exec(`INSERT INTO drm_license_audit (media_id, drm_type, result, reason, client_ip) VALUES (1, 'widevine', 'allowed', '', '1.1.1.1')`)
	_, _ = db.Exec(`INSERT INTO drm_license_audit (media_id, drm_type, result, reason, client_ip) VALUES (1, 'fairplay', 'denied', 'drm_asset_not_ready', '2.2.2.2')`)

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/drm-license-audit?media_id=1&drm_type=fairplay&result=denied&limit=20", nil)

	h.ListDRMLicenseAudits(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"drm_type":"fairplay"`) || !strings.Contains(body, `"result":"denied"`) {
		t.Fatalf("unexpected body: %s", body)
	}
	if strings.Contains(body, `"drm_type":"widevine"`) {
		t.Fatalf("filter did not apply: %s", body)
	}
}

func TestListDRMLicenseAuditsWithTimeRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "drm-audit-range.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4')`)
	_, _ = db.Exec(`INSERT INTO drm_license_audit (media_id, drm_type, result, reason, client_ip, created_at) VALUES (1, 'widevine', 'allowed', '', '1.1.1.1', '2026-04-20 10:00:00')`)
	_, _ = db.Exec(`INSERT INTO drm_license_audit (media_id, drm_type, result, reason, client_ip, created_at) VALUES (1, 'fairplay', 'denied', 'x', '2.2.2.2', '2026-04-24 10:00:00')`)

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/drm-license-audit?range=custom&from=2026-04-23%2000:00:00&to=2026-04-24%2023:59:59", nil)

	h.ListDRMLicenseAudits(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"drm_type":"fairplay"`) {
		t.Fatalf("expected fairplay row in body: %s", body)
	}
	if strings.Contains(body, `"drm_type":"widevine"`) {
		t.Fatalf("unexpected widevine row in body: %s", body)
	}
}

func TestListDRMLicenseAuditsWithRangePreset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "drm-audit-preset.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4')`)
	_, _ = db.Exec(`INSERT INTO drm_license_audit (media_id, drm_type, result, reason, client_ip, created_at) VALUES (1, 'widevine', 'allowed', '', '1.1.1.1', datetime('now','-40 days'))`)
	_, _ = db.Exec(`INSERT INTO drm_license_audit (media_id, drm_type, result, reason, client_ip, created_at) VALUES (1, 'fairplay', 'denied', 'x', '2.2.2.2', datetime('now','-1 days'))`)

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/drm-license-audit?range=7d", nil)

	h.ListDRMLicenseAudits(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"drm_type":"fairplay"`) {
		t.Fatalf("expected recent fairplay row in body: %s", body)
	}
	if strings.Contains(body, `"drm_type":"widevine"`) {
		t.Fatalf("unexpected old widevine row in body: %s", body)
	}
}

func TestListDRMLicenseAuditsWithReasonKeyword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "drm-audit-reason.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4')`)
	_, _ = db.Exec(`INSERT INTO drm_license_audit (media_id, drm_type, result, reason, client_ip) VALUES (1, 'fairplay', 'denied', 'drm_asset_not_ready', '1.1.1.1')`)
	_, _ = db.Exec(`INSERT INTO drm_license_audit (media_id, drm_type, result, reason, client_ip) VALUES (1, 'widevine', 'denied', 'policy_denied', '2.2.2.2')`)

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/drm-license-audit?reason=asset_not", nil)

	h.ListDRMLicenseAudits(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"reason":"drm_asset_not_ready"`) {
		t.Fatalf("expected matched reason row in body: %s", body)
	}
	if strings.Contains(body, `"reason":"policy_denied"`) {
		t.Fatalf("unexpected unmatched reason row in body: %s", body)
	}
}
