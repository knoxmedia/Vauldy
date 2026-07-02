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
	"knox-media/internal/store"
)

func setupFavoriteFolderTestDB(t *testing.T) *Handler {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "favorite-folder.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stmts := []string{
		`INSERT INTO library (id, name, type, path, enabled) VALUES (1, 'movies', 'movie', 'E:/movies', 1)`,
		`INSERT INTO media (id, library_id, file_id, title, file_path, file_type) VALUES (10, 1, 'f-10', 'Movie One', 'E:/movies/a.mp4', 'video')`,
		`INSERT INTO media (id, library_id, file_id, title, file_path, file_type) VALUES (11, 1, 'f-11', 'Movie Two', 'E:/movies/b.mp4', 'video')`,
		`INSERT INTO user (id, username, password, role, can_play, library_scope) VALUES (1, 'viewer', 'x', 'user', 1, 'all')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
}

func TestFavoriteFolderCRUD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupFavoriteFolderTestDB(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/favorite-folders", bytes.NewBufferString(`{"name":"我的影片"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setUserCtx(c, 1, "user", "viewer")
	h.CreateFavoriteFolder(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("expected folder id, got %v", created.ID)
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/favorite-folders/1/items", bytes.NewBufferString(`{"media_id":10}`))
	c.Request.Header.Set("Content-Type", "application/json")
	setUserCtx(c, 1, "user", "viewer")
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.AddFavoriteFolderItem(c)
	if w.Code != http.StatusOK {
		t.Fatalf("add item status=%d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/favorite-folders/1", nil)
	setUserCtx(c, 1, "user", "viewer")
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.GetFavoriteFolder(c)
	if w.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", w.Code, w.Body.String())
	}
	var detail struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if len(detail.Items) != 1 {
		t.Fatalf("expected 1 item, got %d body=%s", len(detail.Items), w.Body.String())
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/favorite-folders", nil)
	setUserCtx(c, 1, "user", "viewer")
	h.ListFavoriteFolders(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
}
