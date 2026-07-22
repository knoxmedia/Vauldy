package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestLibraryPreviewHelpersHandleNilConfig(t *testing.T) {
	h := setupAccessTestDB(t)
	h.scheduleLibraryPreviewRefresh(1)
	if got := h.libraryPreviewPublicURL(1); got != "" {
		t.Fatalf("library preview URL = %q, want empty with nil config", got)
	}
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

func TestListMediaSelectedScopeWithNoAllowedLibrariesDoesNotLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`DELETE FROM user_library_permission WHERE user_id=1`); err != nil {
		t.Fatal(err)
	}
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
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 0 {
		t.Fatalf("selected user with no libraries received %v", payload.Items)
	}
}

func TestListMediaFolderScopeStillFiltersWithinAllowedLibrary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(1,1,'E:/lib1/visible')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`UPDATE media SET file_path='E:/lib1/hidden/a.mp4' WHERE id=10`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,created_at_sort) VALUES(11,1,'f-11','E:/lib1/visible/b.mp4','2026-01-01T00:00:00.000000Z')`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media?sort=id_desc", nil)
	setUserCtx(c, 1, "user", "normal")
	h.ListMedia(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ID != 11 {
		t.Fatalf("items=%v body=%s", payload.Items, w.Body.String())
	}
}

func TestPathMatchesAnyFolderUsesDirectoryBoundaries(t *testing.T) {
	tests := []struct {
		file, folder string
		want         bool
	}{
		{`C:\Media\Allowed\movie.mkv`, `c:/media/allowed`, true},
		{`C:/Media/Allowed`, `c:\media\allowed\`, true},
		{`C:/Media/Allowed-Other/movie.mkv`, `C:/Media/Allowed`, false},
		{`C:/Media//Allowed/./sub/movie.mkv`, `c:/media/allowed`, true},
		{`C:/Media/Allowed/../Secret/movie.mkv`, `C:/Media/Allowed`, false},
		{`C:/anything/movie.mkv`, `C:/`, true},
		{`/srv/media/movie.mkv`, `/`, true},
	}
	for _, tt := range tests {
		if got := pathMatchesAnyFolder(tt.file, []string{tt.folder}); got != tt.want {
			t.Errorf("pathMatchesAnyFolder(%q,%q)=%v want %v", tt.file, tt.folder, got, tt.want)
		}
	}
}

func TestLoadUserPermissionProfileContextPropagatesPermissionScanErrors(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`PRAGMA foreign_keys=OFF; INSERT INTO user_library_permission(user_id,library_id) VALUES(1,'not-an-integer')`); err != nil {
		t.Fatal(err)
	}
	_, err := h.loadUserPermissionProfileContext(context.Background(), 1)
	if err == nil {
		t.Fatal("malformed permission row was ignored")
	}
}

func TestLoadUserPermissionProfileContextHonorsCancellation(t *testing.T) {
	h := setupAccessTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.loadUserPermissionProfileContext(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestListMediaPermissionScanErrorFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`PRAGMA foreign_keys=OFF; INSERT INTO user_library_permission(user_id,library_id) VALUES(1,'bad-library')`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media", nil)
	setUserCtx(c, 1, "user", "normal")
	h.ListMedia(c)
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("permission scan error leaked success: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestLoadUserPermissionProfileContextMissingUserFailsClosed(t *testing.T) {
	h := setupAccessTestDB(t)
	_, err := h.loadUserPermissionProfileContext(context.Background(), 999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err=%v want sql.ErrNoRows", err)
	}
}

func TestListMediaMissingUserReturnsNoItems(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAccessTestDB(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media", nil)
	setUserCtx(c, 999, "user", "deleted")
	h.ListMedia(c)
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("missing user got success: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPathMatchesAnyFolderHonorsPathStyleCaseRules(t *testing.T) {
	tests := []struct {
		name, file, folder string
		want               bool
	}{
		{"posix exact case", `/srv/Media/movie.mkv`, `/srv/Media`, true},
		{"posix case mismatch", `/srv/media/movie.mkv`, `/srv/Media`, false},
		{"drive insensitive", `C:/MEDIA/movie.mkv`, `c:/media`, true},
		{"unc insensitive", `\\Server\Share\MEDIA\movie.mkv`, `//server/share/media`, true},
		{"relative case sensitive", `Media/movie.mkv`, `media`, false},
		{"drive versus posix mismatch", `C:/media/movie.mkv`, `/c:/media`, false},
		{"unc versus posix mismatch", `//server/share/media/movie.mkv`, `/server/share/media`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathMatchesAnyFolder(tt.file, []string{tt.folder}); got != tt.want {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestRequireMediaAccessHidesEveryUnpublishedStateFromOrdinaryCallers(t *testing.T) {
	for _, state := range []string{"processing", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			h := setupAccessTestDB(t)
			if _, err := h.App.DB.Exec(`UPDATE media SET publication_state=? WHERE id=10`, state); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10/meta", nil)
			setUserCtx(c, 1, "user", "normal")
			if _, ok := h.requireMediaAccess(c, 10, false); ok || w.Code != http.StatusNotFound {
				t.Fatalf("state=%s ok=%v status=%d body=%s", state, ok, w.Code, w.Body.String())
			}
		})
	}
}

func TestPlayMediaDirectLookupHidesProcessing(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET publication_state='processing' WHERE id=10`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10/play", nil)
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	setUserCtx(c, 1, "user", "normal")
	h.PlayMedia(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAPIClientCannotBypassPublicationVisibility(t *testing.T) {
	h := setupAccessTestDB(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET publication_state='processing' WHERE id=10`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/10", nil)
	setUserCtx(c, 0, "api_client", "machine")
	if _, ok := h.requireMediaAccess(c, 10, false); ok || w.Code != http.StatusNotFound {
		t.Fatalf("ok=%v status=%d body=%s", ok, w.Code, w.Body.String())
	}
}
