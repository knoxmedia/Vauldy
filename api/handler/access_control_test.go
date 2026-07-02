package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

// setupAccessTestDB creates: 2 libraries; 1 media in each; 1 user with selected scope
// limited to library 1, plus an admin user.
func setupAccessTestDB(t *testing.T) *Handler {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "access.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, enabled) VALUES (1, 'allowed', 'movie', 'E:/lib1', 1)`); err != nil {
		t.Fatalf("insert lib1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, enabled) VALUES (2, 'denied', 'movie', 'E:/lib2', 1)`); err != nil {
		t.Fatalf("insert lib2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, enabled) VALUES (3, 'disabled', 'movie', 'E:/lib3', 0)`); err != nil {
		t.Fatalf("insert lib3: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (10, 1, 'f-10', 'E:/lib1/a.mp4')`); err != nil {
		t.Fatalf("insert media10: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (20, 2, 'f-20', 'E:/lib2/b.mp4')`); err != nil {
		t.Fatalf("insert media20: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user (id, username, password, role, can_play, library_scope) VALUES (1, 'normal', 'x', 'user', 1, 'selected')`); err != nil {
		t.Fatalf("insert user1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_library_permission (user_id, library_id) VALUES (1, 1)`); err != nil {
		t.Fatalf("insert user_lib_perm: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user (id, username, password, role, can_play, library_scope) VALUES (2, 'admin', 'x', 'admin', 1, 'all')`); err != nil {
		t.Fatalf("insert admin: %v", err)
	}
	return &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
}

func setUserCtx(c *gin.Context, uid int64, role, username string) {
	c.Set("user_id", uid)
	c.Set("role", role)
	c.Set("username", username)
}

func TestListLibrariesFiltersBySelectedScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/library", nil)
	setUserCtx(c, 1, "user", "normal")
	h.ListLibraries(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected 1 library, got %d body=%s", len(payload.Items), w.Body.String())
	}
	if int(payload.Items[0]["id"].(float64)) != 1 {
		t.Fatalf("expected library id=1, got %v", payload.Items[0]["id"])
	}
}

func TestListLibrariesHidesDisabledFromNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE user SET library_scope='all' WHERE id = 1`); err != nil {
		t.Fatalf("update scope: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/library", nil)
	setUserCtx(c, 1, "user", "normal")
	h.ListLibraries(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	for _, it := range payload.Items {
		if int(it["id"].(float64)) == 3 {
			t.Fatalf("disabled library exposed to non-admin: %v", it)
		}
	}
}

func TestListLibrariesAdminSeesDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/library", nil)
	setUserCtx(c, 2, "admin", "admin")
	h.ListLibraries(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	if len(payload.Items) != 3 {
		t.Fatalf("admin should see all 3 libraries, got %d", len(payload.Items))
	}
}

func TestGetMediaDeniesUnauthorizedLibrary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/20", nil)
	c.Params = gin.Params{{Key: "id", Value: "20"}}
	setUserCtx(c, 1, "user", "normal")
	h.GetMedia(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGetMediaAllowsAuthorizedLibrary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10", nil)
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	setUserCtx(c, 1, "user", "normal")
	h.GetMedia(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestListMediaRejectsUnauthorizedLibraryQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media?library_id=2", nil)
	setUserCtx(c, 1, "user", "normal")
	h.ListMedia(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestListMediaFiltersByPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media", nil)
	setUserCtx(c, 1, "user", "normal")
	h.ListMedia(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	for _, it := range payload.Items {
		if int(it["library_id"].(float64)) != 1 {
			t.Fatalf("media from disallowed library leaked: %v", it)
		}
	}
}

func TestPlayMediaDeniedForUnauthorizedLibrary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/20/play", nil)
	c.Params = gin.Params{{Key: "id", Value: "20"}}
	setUserCtx(c, 1, "user", "normal")
	h.PlayMedia(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPlayMediaDeniedWhenCanPlayDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE user SET can_play = 0 WHERE id = 1`); err != nil {
		t.Fatalf("update can_play: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10/play", nil)
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	setUserCtx(c, 1, "user", "normal")
	h.PlayMedia(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	if payload.Error != "playback denied" {
		t.Fatalf("expected playback denied, got %q body=%s", payload.Error, w.Body.String())
	}
}

func TestPhotoPreviewInfoAllowsWhenCanPlayDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET file_type = 'image' WHERE id = 10`); err != nil {
		t.Fatalf("update file_type: %v", err)
	}
	if _, err := h.App.DB.Exec(`UPDATE user SET can_play = 0 WHERE id = 1`); err != nil {
		t.Fatalf("update can_play: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10/photo", nil)
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	setUserCtx(c, 1, "user", "normal")
	h.PhotoPreviewInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for photo preview info without play permission, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestServePhotoFaceThumbAllowsWhenCanPlayDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET file_type = 'image' WHERE id = 10`); err != nil {
		t.Fatalf("update file_type: %v", err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO photo_face (id, media_id, library_id, bbox_x, bbox_y, bbox_w, bbox_h) VALUES (129, 10, 1, 0.1, 0.1, 0.2, 0.2)`); err != nil {
		t.Fatalf("insert photo_face: %v", err)
	}
	if _, err := h.App.DB.Exec(`UPDATE user SET can_play = 0 WHERE id = 1`); err != nil {
		t.Fatalf("update can_play: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/photo/face/129/thumb.jpg", nil)
	c.Params = gin.Params{{Key: "id", Value: "129"}}
	setUserCtx(c, 1, "user", "normal")
	h.ServePhotoFaceThumb(c)
	if w.Code == http.StatusForbidden {
		t.Fatalf("face thumb should not require play permission, got 403 body=%s", w.Body.String())
	}
}

func TestServeDocumentCoverAllowsWhenCanPlayDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET file_type = 'document', format = 'pdf' WHERE id = 10`); err != nil {
		t.Fatalf("update file_type: %v", err)
	}
	if _, err := h.App.DB.Exec(`UPDATE user SET can_play = 0 WHERE id = 1`); err != nil {
		t.Fatalf("update can_play: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10/document/cover.jpg", nil)
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	setUserCtx(c, 1, "user", "normal")
	h.ServeDocumentCover(c)
	if w.Code == http.StatusForbidden {
		t.Fatalf("document cover should not require play permission, got 403 body=%s", w.Body.String())
	}
}

func TestGetMediaAllowsWhenCanPlayDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE user SET can_play = 0 WHERE id = 1`); err != nil {
		t.Fatalf("update can_play: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10", nil)
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	setUserCtx(c, 1, "user", "normal")
	h.GetMedia(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 so browsing works without play, got status=%d body=%s", w.Code, w.Body.String())
	}
}
