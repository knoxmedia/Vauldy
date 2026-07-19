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

func TestUserHistoryDuplicateCompletionDominatesAndFreshestRowDisplays(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows string
	}{
		{
			name: "completed inserted first with mixed timestamps",
			rows: `(1,'f-movie',940,1,'2026-07-19T01:00:00Z'),
			       (1,'f-movie',120,0,'2026-07-19 02:00:00'),
			       (2,'f-movie',500,1,'2026-07-19 03:00:00')`,
		},
		{
			name: "completed inserted last with equal timestamps",
			rows: `(1,'f-movie',120,0,'2026-07-19 02:00:00'),
			       (2,'f-movie',500,1,'2026-07-19 03:00:00'),
			       (1,'f-movie',940,1,'2026-07-19 02:00:00')`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			h := setupUserHistoryTestDB(t)
			if _, err := h.App.DB.Exec(`DELETE FROM play_progress; INSERT INTO user (id,username,password,role,can_play,library_scope) VALUES (2,'other','x','user',1,'all'); INSERT INTO play_progress(user_id,file_id,position,completed,update_at) VALUES ` + tc.rows); err != nil {
				t.Fatal(err)
			}
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
				t.Fatal(err)
			}
			if len(payload.Items) != 0 {
				t.Fatalf("any current-user completed duplicate must exclude history; other user must not affect result: %s", w.Body.String())
			}
		})
	}
}

func TestUserHistoryOtherUserCompletionDoesNotExcludeCurrentUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupUserHistoryTestDB(t)
	if _, err := h.App.DB.Exec(`DELETE FROM play_progress;
		INSERT INTO user (id,username,password,role,can_play,library_scope) VALUES (2,'other','x','user',1,'all');
		INSERT INTO play_progress(user_id,file_id,position,completed,update_at) VALUES
		(2,'f-movie',900,1,'2026-07-19 03:00:00'),
		(1,'f-movie',240,0,'2026-07-19 02:00:00')`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/user/history?limit=10", nil)
	setUserCtx(c, 1, "user", "viewer")
	h.UserHistory(c)
	var payload struct {
		Items []struct {
			Position  int64 `json:"position"`
			Completed int64 `json:"completed"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(payload.Items) != 1 || payload.Items[0].Position != 240 || payload.Items[0].Completed != 0 {
		t.Fatalf("other-user completion leaked into current history: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUserHistoryDuplicateIncompleteRowsUseFreshestDisplayRow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupUserHistoryTestDB(t)
	if _, err := h.App.DB.Exec(`DELETE FROM play_progress; INSERT INTO play_progress(user_id,file_id,position,completed,update_at) VALUES
		(1,'f-movie',120,0,'2026-07-19T01:00:00Z'),
		(1,'f-movie',240,0,'2026-07-19 02:00:00'),
		(1,'f-movie',360,0,'2026-07-19 02:00:00')`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/user/history?limit=10", nil)
	setUserCtx(c, 1, "user", "viewer")
	h.UserHistory(c)
	var payload struct {
		Items []struct {
			Position int64  `json:"position"`
			UpdateAt string `json:"update_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(payload.Items) != 1 || payload.Items[0].Position != 360 || payload.Items[0].UpdateAt != "2026-07-19T02:00:00Z" {
		t.Fatalf("freshest row must be deterministic by update_at then id: status=%d body=%s", w.Code, w.Body.String())
	}
}
