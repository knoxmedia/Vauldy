package license

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/config"
	"knox-media/internal/coreiface"
	"knox-media/internal/store"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	return db
}

func setLicense(db *sql.DB, key, secret string) {
	raw := "{}"
	if m, err := json.Marshal(map[string]any{"license_key": key, "license_secret": secret}); err == nil {
		raw = string(m)
	}
	_, _ = db.Exec(`INSERT INTO system_options (id, options_json) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET options_json=excluded.options_json`, raw)
}

func TestParseLicenseValid(t *testing.T) {
	c := Claims{Pretranscode: true, Trial: false, Exp: time.Now().Add(72 * time.Hour).Unix()}
	key := IssueLicense("", c)
	got, err := parseLicense(key, builtinSigningSecret)
	if err != nil {
		t.Fatalf("parseLicense: %v", err)
	}
	if !got.Pretranscode {
		t.Errorf("Pretranscode should be true")
	}
}

func TestParseLicenseBadSignature(t *testing.T) {
	c := Claims{Pretranscode: true, Exp: time.Now().Add(72 * time.Hour).Unix()}
	key := IssueLicense("wrong-secret", c)
	_, err := parseLicense(key, builtinSigningSecret)
	if err == nil || !strings.Contains(err.Error(), "bad_signature") {
		t.Errorf("expected bad_signature error, got %v", err)
	}
}

func TestParseLicenseMalformed(t *testing.T) {
	_, err := parseLicense("not-a-valid-key", builtinSigningSecret)
	if err == nil {
		t.Errorf("expected malformed_key error")
	}
}

func TestLoadStatusMissingKey(t *testing.T) {
	m := &Module{db: newTestDB(t)}
	st := m.loadStatus()
	// Commercial builds without a license key get full access by default.
	// Community builds should return missing_key error.
	if config.Edition == "commercial" {
		if !st.Valid || !st.Pretranscode {
			t.Errorf("commercial build missing key should yield valid status, got %+v", st)
		}
	} else {
		if st.Valid || st.Pretranscode {
			t.Errorf("community build missing key should yield invalid status, got %+v", st)
		}
		if st.ErrorCode != "missing_key" {
			t.Errorf("expected missing_key, got %s", st.ErrorCode)
		}
	}
}

func TestLoadStatusExpired(t *testing.T) {
	db := newTestDB(t)
	c := Claims{Pretranscode: true, Exp: time.Now().Add(-1 * time.Hour).Unix()}
	setLicense(db, IssueLicense("", c), "")
	m := &Module{db: db}
	st := m.loadStatus()
	if !st.Expired {
		t.Errorf("should be expired")
	}
	if st.Pretranscode {
		t.Errorf("expired license should not grant pretranscode")
	}
}

func TestLoadStatusExpiringSoon(t *testing.T) {
	db := newTestDB(t)
	c := Claims{Pretranscode: true, Exp: time.Now().Add(15 * 24 * time.Hour).Unix()}
	setLicense(db, IssueLicense("", c), "")
	m := &Module{db: db}
	st := m.loadStatus()
	if !st.ExpiringSoon {
		t.Errorf("should be expiring soon (≤30 days)")
	}
	if !st.Pretranscode {
		t.Errorf("should still be valid")
	}
}

func runMiddleware(t *testing.T, mod *Module) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	licenseSingleton = mod
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	RequireFeature("pretranscode")(c)
	return c, w
}

func TestRequireFeatureAllows(t *testing.T) {
	db := newTestDB(t)
	c := Claims{Pretranscode: true, Exp: time.Now().Add(72 * time.Hour).Unix()}
	setLicense(db, IssueLicense("", c), "")
	_, w := runMiddleware(t, &Module{db: db})
	if w.Code != 200 {
		t.Errorf("expected 200 (proceed), got %d", w.Code)
	}
}

func TestRequireFeatureBlocksExpired(t *testing.T) {
	db := newTestDB(t)
	c := Claims{Pretranscode: true, Exp: time.Now().Add(-1 * time.Hour).Unix()}
	setLicense(db, IssueLicense("", c), "")
	_, w := runMiddleware(t, &Module{db: db})
	if w.Code != 403 {
		t.Errorf("expected 403 for expired, got %d", w.Code)
	}
}

func TestRequireFeatureBlocksUnlicensed(t *testing.T) {
	db := newTestDB(t)
	c := Claims{Pretranscode: false, Exp: time.Now().Add(72 * time.Hour).Unix()}
	setLicense(db, IssueLicense("", c), "")
	_, w := runMiddleware(t, &Module{db: db})
	if w.Code != 403 {
		t.Errorf("expected 403 for unlicensed, got %d", w.Code)
	}
}

func TestRequireFeatureTrialLimit(t *testing.T) {
	db := newTestDB(t)
	c := Claims{Pretranscode: true, Trial: true, Exp: time.Now().Add(72 * time.Hour).Unix()}
	setLicense(db, IssueLicense("", c), "")
	// Seed a parent transcode_task row + preset row so the rendition_job FK
	// constraints are satisfied, then 10 completed jobs to trip the trial cap.
	_, _ = db.Exec(`INSERT INTO transcode_preset (name, output_format, video_codec, audio_codec, audio_bitrate) VALUES ('test','hls','libx264','aac','128k')`)
	_, _ = db.Exec(`INSERT INTO transcode_task (file_id, quality, status, task_type) VALUES ('fid','720p','done','pretranscode')`)
	for i := 0; i < 10; i++ {
		_, _ = db.Exec(`INSERT INTO pretranscode_rendition_job (task_id, rendition_id, rendition_name, status) VALUES (1, 1, '360p', 'done')`)
	}
	_, w := runMiddleware(t, &Module{db: db})
	if w.Code != 403 {
		t.Errorf("expected 403 for trial limit, got %d", w.Code)
	}
}

func TestRequireFeatureExpiringSoonHeader(t *testing.T) {
	db := newTestDB(t)
	c := Claims{Pretranscode: true, Exp: time.Now().Add(10 * 24 * time.Hour).Unix()}
	setLicense(db, IssueLicense("", c), "")
	_, w := runMiddleware(t, &Module{db: db})
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if v := w.Header().Get("X-Knox-License-Warning"); v == "" {
		t.Errorf("expected X-Knox-License-Warning header set")
	}
}

func TestInitBindsDB(t *testing.T) {
	m := NewModule()
	db := newTestDB(t)
	if err := m.Init(context.Background(), coreiface.ModuleDeps{DB: db}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if m.db == nil {
		t.Errorf("db not bound")
	}
}
