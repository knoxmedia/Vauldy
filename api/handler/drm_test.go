package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	drmsvc "knox-media/internal/drm"
	"knox-media/internal/store"
)

func newDRMHandlerForTest(t *testing.T) *Handler {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "drm-test.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	drmDir := filepath.Join(t.TempDir(), "drm")
	if err := os.MkdirAll(drmDir, 0o755); err != nil {
		t.Fatalf("mkdir drm dir: %v", err)
	}
	keyRef := filepath.Join(drmDir, "drm_key_ref.json")
	manifest := filepath.Join(drmDir, "master.m3u8")
	if err := os.WriteFile(keyRef, []byte(`{"kid":"kid-1","key":"00112233445566778899aabbccddeeff"}`), 0o600); err != nil {
		t.Fatalf("write key ref: %v", err)
	}
	if err := os.WriteFile(manifest, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO drm_asset (media_id, kid, key_ref, manifest_path, license_policy_json) VALUES (1, 'kid-1', ?, ?, '{}')`, keyRef, manifest); err != nil {
		t.Fatalf("insert drm_asset: %v", err)
	}
	return &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
}

func TestWidevineServiceCertRequiresAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/drm/widevine/service-cert", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.WidevineServiceCert(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestWidevineServiceCertUnavailableWithoutPrivateModule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/drm/widevine/service-cert", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))

	h.WidevineServiceCert(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestWidevineServiceCertProxiesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/service-cert" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0xde, 0xad, 0xbe, 0xef})
	}))
	t.Cleanup(up.Close)

	h := newDRMHandlerForTest(t)
	h.App.Config = &config.Config{
		DRM: config.DRMConfig{
			Widevine: config.WidevineConfig{
				PrivateModuleURL:            up.URL + "/license",
				PrivateModuleTimeoutSeconds: 5,
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/drm/widevine/service-cert", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))

	h.WidevineServiceCert(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatalf("unexpected body: %q", w.Body.String())
	}
}

func TestWidevineLicenseRequiresAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)

	body := map[string]any{"media_id": 1, "challenge": "abc"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drm/widevine/license", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.WidevineLicense(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestFairPlayEndpointsReturnExpectedShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)

	wCert := httptest.NewRecorder()
	cCert, _ := gin.CreateTestContext(wCert)
	certReq := httptest.NewRequest(http.MethodGet, "/api/v1/drm/fairplay/cert", nil)
	certReq.Header.Set("Content-Type", "application/json")
	cCert.Request = certReq
	cCert.Set("user_id", int64(1))
	cCert.Set("role", "user")
	cCert.Set("username", "u")
	h.FairPlayCert(cCert)
	if wCert.Code != http.StatusOK {
		t.Fatalf("cert status=%d body=%s", wCert.Code, wCert.Body.String())
	}
	if ct := wCert.Header().Get("Content-Type"); !strings.Contains(ct, "application/octet-stream") {
		t.Fatalf("unexpected cert content-type: %s", ct)
	}
	if wCert.Body.Len() == 0 {
		t.Fatalf("expected non-empty cert response")
	}

	wLic := httptest.NewRecorder()
	cLic, _ := gin.CreateTestContext(wLic)
	licBody := map[string]any{"media_id": 1, "spc": "spcdata"}
	licRaw, _ := json.Marshal(licBody)
	licReq := httptest.NewRequest(http.MethodPost, "/api/v1/drm/fairplay/license", bytes.NewReader(licRaw))
	licReq.Header.Set("Content-Type", "application/json")
	cLic.Request = licReq
	cLic.Set("user_id", int64(1))
	cLic.Set("role", "user")
	cLic.Set("username", "u")
	h.FairPlayLicense(cLic)
	if wLic.Code != http.StatusOK {
		t.Fatalf("license status=%d body=%s", wLic.Code, wLic.Body.String())
	}
	if !strings.Contains(wLic.Body.String(), `"ckc":"`) {
		t.Fatalf("unexpected license body: %s", wLic.Body.String())
	}
	if !strings.Contains(wLic.Body.String(), `"kid":"kid-1"`) || !strings.Contains(wLic.Body.String(), `"sig":"`) || !strings.Contains(wLic.Body.String(), `"exp":`) {
		t.Fatalf("fairplay response missing kid/sig/exp: %s", wLic.Body.String())
	}
	if !strings.Contains(wLic.Body.String(), `"kid_version":"v1"`) || !strings.Contains(wLic.Body.String(), `"sig_version":"hmac-sha256-v1"`) {
		t.Fatalf("fairplay response missing kid_version/sig_version: %s", wLic.Body.String())
	}
}

func TestWidevineLicenseRequiresDRMAssetBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	if _, err := h.App.DB.Exec(`DELETE FROM drm_asset WHERE media_id = 1`); err != nil {
		t.Fatalf("delete drm_asset: %v", err)
	}

	body := map[string]any{"media_id": 1, "challenge": "abc"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drm/widevine/license", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.WidevineLicense(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	bodyStr := strings.ToLower(w.Body.String())
	if !strings.Contains(bodyStr, "drm asset") && !strings.Contains(bodyStr, "no rows") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestWidevineLicenseRejectsEmptyChallenge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)

	body := map[string]any{"media_id": 1, "challenge": "   "}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drm/widevine/license", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.WidevineLicense(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	bodyStr := strings.ToLower(w.Body.String())
	if !strings.Contains(bodyStr, "challenge") || !strings.Contains(bodyStr, "required") {
		t.Fatalf("expected challenge-required error, got: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"license":"`) {
		t.Fatalf("expected no license issuance on empty challenge, got: %s", w.Body.String())
	}

	var result, reason string
	if err := h.App.DB.QueryRow(`SELECT result, reason FROM drm_license_audit WHERE media_id = 1 AND drm_type = 'widevine' ORDER BY id DESC LIMIT 1`).Scan(&result, &reason); err != nil {
		t.Fatalf("query latest audit: %v", err)
	}
	if result != "denied" || reason != "empty_challenge" {
		t.Fatalf("unexpected audit result=%q reason=%q", result, reason)
	}
}

func TestWidevineLicenseResponseContainsKidExpSig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)

	body := map[string]any{"media_id": 1, "challenge": "abc"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drm/widevine/license", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.WidevineLicense(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, `"kid":"kid-1"`) || !strings.Contains(bodyStr, `"sig":"`) || !strings.Contains(bodyStr, `"exp":`) {
		t.Fatalf("missing kid/sig/exp in response: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"kid_version":"v1"`) || !strings.Contains(bodyStr, `"sig_version":"hmac-sha256-v1"`) {
		t.Fatalf("missing kid_version/sig_version in response: %s", bodyStr)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	license, _ := resp["license"].(string)
	decoded, err := base64.StdEncoding.DecodeString(license)
	if err != nil {
		t.Fatalf("decode license: %v", err)
	}
	if !strings.Contains(string(decoded), `"kid":"kid-1"`) {
		t.Fatalf("decoded license missing kid: %s", string(decoded))
	}
}

func TestWidevineLicense_LocalIssuance_NoProxyBranch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	h.App.Config = &config.Config{
		Security: config.SecurityConfig{
			JWTSecret: "unit-test-secret",
		},
	}

	body := map[string]any{"media_id": 1, "challenge": "plain-non-base64-challenge"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drm/widevine/license", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.WidevineLicense(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, `"license":"`) || !strings.Contains(bodyStr, `"kid":"kid-1"`) {
		t.Fatalf("expected local signed license response, got: %s", bodyStr)
	}
	if strings.Contains(strings.ToLower(bodyStr), "proxy") {
		t.Fatalf("response should not indicate proxy path: %s", bodyStr)
	}
}

func TestWidevineLicense_LocalIssuance_RawMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)

	body := map[string]any{"media_id": 1, "challenge": "plain-non-base64-challenge"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drm/widevine/license?raw=1", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/octet-stream")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.WidevineLicense(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/octet-stream") {
		t.Fatalf("unexpected content-type: %s", ct)
	}
	if w.Body.Len() == 0 {
		t.Fatalf("expected non-empty raw license bytes")
	}
	if got := w.Header().Get("X-DRM-KID"); got != "kid-1" {
		t.Fatalf("unexpected X-DRM-KID: %s", got)
	}
	if w.Header().Get("X-DRM-Sig") == "" || w.Header().Get("X-DRM-Exp") == "" {
		t.Fatalf("expected signature and expiration headers")
	}
}

func TestWidevineLicense_LocalIssuance_AcceptOctetStreamWithoutRawFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)

	body := map[string]any{"media_id": 1, "challenge": "plain-non-base64-challenge"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drm/widevine/license", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/octet-stream")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.WidevineLicense(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/octet-stream") {
		t.Fatalf("unexpected content-type: %s", ct)
	}
	if w.Body.Len() == 0 {
		t.Fatalf("expected non-empty raw license bytes")
	}
	if w.Header().Get("X-DRM-KID") == "" {
		t.Fatalf("expected X-DRM-KID header")
	}
	if w.Header().Get("X-DRM-Sig") == "" || w.Header().Get("X-DRM-Exp") == "" {
		t.Fatalf("expected signature and expiration headers")
	}
}

func TestWidevineLicenseUsesConfiguredVersionFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	h.App.Config = &config.Config{
		Security: config.SecurityConfig{
			JWTSecret:  "unit-test-secret",
			KIDVersion: "kid-v2",
			SigVersion: "hmac-sha256-v2",
		},
	}

	body := map[string]any{"media_id": 1, "challenge": "abc"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drm/widevine/license", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.WidevineLicense(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, `"kid_version":"kid-v2"`) || !strings.Contains(bodyStr, `"sig_version":"hmac-sha256-v2"`) {
		t.Fatalf("missing configured version fields: %s", bodyStr)
	}
}

func TestVerifyLicenseAdminOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/drm/license/verify", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.VerifyLicense(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestVerifyLicenseReturnsClaimsForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	h.App.Config = &config.Config{
		Security: config.SecurityConfig{
			JWTSecret:  "verify-secret",
			KIDVersion: "kid-v2",
			SigVersion: "hmac-sha256-v2",
		},
	}
	license, _, sig, _, _, err := drmsvc.BuildSignedLicense("widevine", 1, "kid-1", "ref-1", "n", "verify-secret", "kid-v2", "hmac-sha256-v2", 2*time.Minute)
	if err != nil {
		t.Fatalf("build signed license: %v", err)
	}
	body := map[string]any{
		"license": license,
		"sig":     sig,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/drm/license/verify", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "admin")
	c.Set("username", "admin")

	h.VerifyLicense(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"valid":true`) || !strings.Contains(w.Body.String(), `"media_id":1`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"canonical":"widevine|1|kid-1|ref-1|`) {
		t.Fatalf("verify response missing canonical: %s", w.Body.String())
	}
}

func TestVerifyLicenseReturnsSignatureMismatchCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	h.App.Config = &config.Config{
		Security: config.SecurityConfig{
			JWTSecret:  "verify-secret",
			KIDVersion: "kid-v2",
			SigVersion: "hmac-sha256-v2",
		},
	}
	license, _, _, _, _, err := drmsvc.BuildSignedLicense("widevine", 1, "kid-1", "ref-1", "n", "verify-secret", "kid-v2", "hmac-sha256-v2", 2*time.Minute)
	if err != nil {
		t.Fatalf("build signed license: %v", err)
	}
	body := map[string]any{
		"license": license,
		"sig":     "bad-signature",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/drm/license/verify", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "admin")
	c.Set("username", "admin")

	h.VerifyLicense(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"signature_mismatch"`) {
		t.Fatalf("expected signature_mismatch code, got: %s", w.Body.String())
	}
}

func TestVerifyLicenseReturnsVersionMismatchCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	h.App.Config = &config.Config{
		Security: config.SecurityConfig{
			JWTSecret:  "verify-secret",
			KIDVersion: "kid-v3",
			SigVersion: "hmac-sha256-v3",
		},
	}
	license, _, sig, _, _, err := drmsvc.BuildSignedLicense("widevine", 1, "kid-1", "ref-1", "n", "verify-secret", "kid-v2", "hmac-sha256-v2", 2*time.Minute)
	if err != nil {
		t.Fatalf("build signed license: %v", err)
	}
	body := map[string]any{
		"license": license,
		"sig":     sig,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/drm/license/verify", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "admin")
	c.Set("username", "admin")

	h.VerifyLicense(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"kid_version_mismatch"`) {
		t.Fatalf("expected kid_version_mismatch code, got: %s", w.Body.String())
	}
}

func TestVerifyLicenseReturnsSigVersionMismatchCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	h.App.Config = &config.Config{
		Security: config.SecurityConfig{
			JWTSecret:  "verify-secret",
			KIDVersion: "kid-v2",
			SigVersion: "hmac-sha256-v3",
		},
	}
	license, _, sig, _, _, err := drmsvc.BuildSignedLicense("widevine", 1, "kid-1", "ref-1", "n", "verify-secret", "kid-v2", "hmac-sha256-v2", 2*time.Minute)
	if err != nil {
		t.Fatalf("build signed license: %v", err)
	}
	body := map[string]any{
		"license": license,
		"sig":     sig,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/drm/license/verify", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "admin")
	c.Set("username", "admin")

	h.VerifyLicense(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"sig_version_mismatch"`) {
		t.Fatalf("expected sig_version_mismatch code, got: %s", w.Body.String())
	}
}

func TestVerifyLicenseReturnsInvalidPayloadCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	h.App.Config = &config.Config{
		Security: config.SecurityConfig{
			JWTSecret:  "verify-secret",
			KIDVersion: "kid-v2",
			SigVersion: "hmac-sha256-v2",
		},
	}
	body := map[string]any{
		"license": "%%%not-base64%%%",
		"sig":     "x",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/drm/license/verify", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "admin")
	c.Set("username", "admin")

	h.VerifyLicense(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"invalid_payload"`) {
		t.Fatalf("expected invalid_payload code, got: %s", w.Body.String())
	}
}

func TestVerifyLicenseReturnsExpiredCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	h.App.Config = &config.Config{
		Security: config.SecurityConfig{
			JWTSecret:  "verify-secret",
			KIDVersion: "kid-v2",
			SigVersion: "hmac-sha256-v2",
		},
	}
	license, _, sig, _, _, err := drmsvc.BuildSignedLicense("widevine", 1, "kid-1", "ref-1", "n", "verify-secret", "kid-v2", "hmac-sha256-v2", time.Second)
	if err != nil {
		t.Fatalf("build signed license: %v", err)
	}
	time.Sleep(2 * time.Second)
	body := map[string]any{
		"license": license,
		"sig":     sig,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/drm/license/verify", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "admin")
	c.Set("username", "admin")

	h.VerifyLicense(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"license_expired"`) {
		t.Fatalf("expected license_expired code, got: %s", w.Body.String())
	}
}

func TestWidevineLicenseFailsWhenDRMAssetFilesMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)
	if _, err := h.App.DB.Exec(`UPDATE drm_asset SET key_ref = 'E:/not-exists/key.json', manifest_path = 'E:/not-exists/master.m3u8' WHERE media_id = 1`); err != nil {
		t.Fatalf("update drm_asset path: %v", err)
	}

	body := map[string]any{"media_id": 1, "challenge": "abc"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/drm/widevine/license", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.WidevineLicense(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "drm asset") {
		t.Fatalf("expected drm asset not ready error, got: %s", w.Body.String())
	}
}

func TestPowerDRMKeyReturnsKeyByKID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newDRMHandlerForTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/drm/powerdrm/key?kid=kid-1", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", int64(1))
	c.Set("role", "user")
	c.Set("username", "u")

	h.PowerDRMKey(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"kid":"kid-1"`) || !strings.Contains(w.Body.String(), `"key":"00112233445566778899aabbccddeeff"`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}
