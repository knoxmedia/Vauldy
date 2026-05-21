package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/store"
)

func TestDeleteMediaRemovesFileAndRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	libDir := filepath.Join(base, "movies")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("mkdir lib: %v", err)
	}
	mediaFile := filepath.Join(libDir, "sample.mp4")
	if err := os.WriteFile(mediaFile, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	sidecar := filepath.Join(libDir, "sample.srt")
	if err := os.WriteFile(sidecar, []byte("sub"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	dbPath := filepath.Join(base, "test.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	uploadDir := filepath.Join(base, "upload")
	if err := os.MkdirAll(filepath.Join(uploadDir, "posters"), 0o755); err != nil {
		t.Fatalf("mkdir upload: %v", err)
	}
	posterFile := filepath.Join(uploadDir, "posters", "1.jpg")
	if err := os.WriteFile(posterFile, []byte("jpg"), 0o644); err != nil {
		t.Fatalf("write poster: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, enabled) VALUES (1, 'movies', 'movie', ?, 1)`, libDir); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'sample.mp4')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	cfg := &config.Config{}
	cfg.Data.Upload = uploadDir
	cfg.Data.Subtitle = filepath.Join(base, "subtitles")
	cfg.Data.Preview = filepath.Join(base, "preview")
	cfg.Data.ATracks = filepath.Join(base, "atracks")
	h := &Handler{App: &app.App{DB: db, Config: cfg}, runningScans: map[int64]scanRuntime{}}

	wPlan := httptest.NewRecorder()
	cPlan, _ := gin.CreateTestContext(wPlan)
	cPlan.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/1/deletion-plan", nil)
	cPlan.Params = gin.Params{{Key: "id", Value: "1"}}
	h.GetMediaDeletionPlan(cPlan)
	if wPlan.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", wPlan.Code, wPlan.Body.String())
	}
	var plan struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(wPlan.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if len(plan.Files) < 2 {
		t.Fatalf("expected at least main + sidecar in plan, got %v", plan.Files)
	}

	wDel := httptest.NewRecorder()
	cDel, _ := gin.CreateTestContext(wDel)
	cDel.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/media/1", nil)
	cDel.Params = gin.Params{{Key: "id", Value: "1"}}
	h.DeleteMedia(cDel)
	if wDel.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", wDel.Code, wDel.Body.String())
	}
	if _, err := os.Stat(mediaFile); !os.IsNotExist(err) {
		t.Fatalf("media file still exists: %v", err)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("sidecar still exists: %v", err)
	}
	if _, err := os.Stat(posterFile); !os.IsNotExist(err) {
		t.Fatalf("poster still exists: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE id = 1`).Scan(&count); err != nil {
		t.Fatalf("count media: %v", err)
	}
	if count != 0 {
		t.Fatalf("media row still exists")
	}
}
