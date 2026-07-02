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

func setupPlaybackHistoryTestDB(t *testing.T) *Handler {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "playback-history.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stmts := []string{
		`INSERT INTO library (id, name, type, path, enabled) VALUES (1, 'movies', 'movie', 'E:/movies', 1)`,
		`INSERT INTO media (id, library_id, file_id, title, file_path, file_type) VALUES (10, 1, 'f-10', 'Test Movie', 'E:/movies/a.mp4', 'video')`,
		`INSERT INTO user (id, username, password, role, can_play, library_scope) VALUES (1, 'viewer', 'x', 'user', 1, 'all')`,
		`INSERT INTO play_progress (user_id, file_id, position, play_count, update_at) VALUES (1, 'f-10', 120, 2, CURRENT_TIMESTAMP)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
}

func TestListPlaybackHistoryFromPlayProgress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupPlaybackHistoryTestDB(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/playback-history?range=all", nil)
	setUserCtx(c, 1, "user", "viewer")
	h.ListPlaybackHistory(c)
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
		t.Fatalf("expected 1 item from play_progress, got %d body=%s", len(payload.Items), w.Body.String())
	}
	if int(payload.Items[0]["media_id"].(float64)) != 10 {
		t.Fatalf("media_id=%v", payload.Items[0]["media_id"])
	}
}

func TestParsePlaybackUserAgent(t *testing.T) {
	msg := "playback start; pos=0; completed=0; ip=127.0.0.1; ua=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0"
	player, platform := parsePlaybackUserAgent(msg)
	if player != "Microsoft Edge" {
		t.Fatalf("player=%q want Microsoft Edge", player)
	}
	if platform != "Windows" {
		t.Fatalf("platform=%q want Windows", platform)
	}
}

func TestParsePlaybackUserAgentEmpty(t *testing.T) {
	player, platform := parsePlaybackUserAgent("")
	if player != "-" || platform != "-" {
		t.Fatalf("got player=%q platform=%q", player, platform)
	}
}
