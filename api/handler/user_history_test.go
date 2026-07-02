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

func setupUserHistoryTestDB(t *testing.T) *Handler {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "user-history.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stmts := []string{
		`INSERT INTO library (id, name, type, path, enabled) VALUES (1, 'movies', 'movie', 'E:/movies', 1)`,
		`INSERT INTO library (id, name, type, path, enabled) VALUES (2, 'music', 'music', 'E:/music', 1)`,
		`INSERT INTO media (id, library_id, file_id, title, file_path, file_type) VALUES (10, 1, 'f-movie', 'Movie One', 'E:/movies/a.mp4', 'video')`,
		`INSERT INTO media (id, library_id, file_id, title, file_path, file_type) VALUES (20, 2, 'f-music', 'Song One', 'E:/music/a.flac', 'audio')`,
		`INSERT INTO user (id, username, password, role, can_play, library_scope) VALUES (1, 'viewer', 'x', 'user', 1, 'all')`,
		`INSERT INTO play_progress (user_id, file_id, position, play_count, update_at) VALUES (1, 'f-music', 90, 13, CURRENT_TIMESTAMP)`,
		`INSERT INTO play_progress (user_id, file_id, position, play_count, update_at) VALUES (1, 'f-movie', 120, 2, datetime('now', '-1 hour'))`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
}

func TestUserHistoryLibraryTypeFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupUserHistoryTestDB(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/user/history?limit=10&library_types=movie,tv,video", nil)
	setUserCtx(c, 1, "user", "viewer")
	h.UserHistory(c)
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
		t.Fatalf("expected 1 video item, got %d body=%s", len(payload.Items), w.Body.String())
	}
	if int(payload.Items[0]["media_id"].(float64)) != 10 {
		t.Fatalf("media_id=%v want 10", payload.Items[0]["media_id"])
	}
	if payload.Items[0]["library_type"] != "movie" {
		t.Fatalf("library_type=%v want movie", payload.Items[0]["library_type"])
	}
}

func TestUserHistoryWithoutLibraryTypeFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupUserHistoryTestDB(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/user/history?limit=10", nil)
	setUserCtx(c, 1, "user", "viewer")
	h.UserHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(payload.Items) != 2 {
		t.Fatalf("expected 2 items without filter, got %d body=%s", len(payload.Items), w.Body.String())
	}
}
