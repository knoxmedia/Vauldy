package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/store"
	"knox-media/internal/transcode"
	"knox-media/internal/upload"
)

func newUploadDRMHandler(t *testing.T, drmEnabled int) *Handler {
	t.Helper()
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, "upload-drm.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	libPath := filepath.Join(baseDir, "library")
	if err := os.MkdirAll(libPath, 0o755); err != nil {
		t.Fatalf("mkdir library path: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, drm_enabled) VALUES (1, 'lib1', 'movie', ?, ?)`, libPath, drmEnabled); err != nil {
		t.Fatalf("insert library: %v", err)
	}

	cfg := &config.Config{
		Data: config.DataConfig{
			Upload:    filepath.Join(baseDir, "upload"),
			Chunks:    filepath.Join(baseDir, "chunks"),
			Transcode: filepath.Join(baseDir, "transcode"),
		},
		FFmpeg: config.FFmpegConfig{
			FFprobePath: "ffprobe-not-used",
			FFmpegPath:  "ffmpeg-not-used",
		},
	}
	if err := os.MkdirAll(cfg.Data.Upload, 0o755); err != nil {
		t.Fatalf("mkdir upload path: %v", err)
	}
	if err := os.MkdirAll(cfg.Data.Chunks, 0o755); err != nil {
		t.Fatalf("mkdir chunks path: %v", err)
	}
	if err := os.MkdirAll(cfg.Data.Transcode, 0o755); err != nil {
		t.Fatalf("mkdir transcode path: %v", err)
	}

	return &Handler{
		App:           &app.App{DB: db, Config: cfg},
		PackageWorker: transcode.NewPackageWorker(db, cfg, nil),
		Upload:        &upload.Service{UploadDir: cfg.Data.Upload, ChunksDir: cfg.Data.Chunks},
		runningScans:  map[int64]scanRuntime{},
	}
}

func buildUploadSingleRequest(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("library_id", "1"); err != nil {
		t.Fatalf("write library_id: %v", err)
	}
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func waitForPackageTask(t *testing.T, db *sql.DB, mediaID int64) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow(`SELECT COUNT(1) FROM package_task WHERE media_id = ? AND pipeline_type = 'cmaf_drm'`, mediaID).Scan(&n); err != nil {
			t.Fatalf("query package_task: %v", err)
		}
		if n > 0 {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

func TestUploadSingleSkipsIngestPackageForDRMEnabledLibrary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newUploadDRMHandler(t, 1)

	req := buildUploadSingleRequest(t, "movie.mp4", []byte("fake video"))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.UploadSingle(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	mediaID, _ := resp["id"].(float64)
	if mediaID <= 0 {
		t.Fatalf("invalid media id in response: %s", w.Body.String())
	}
	if got := waitForPackageTask(t, h.App.DB, int64(mediaID)); got != 0 {
		t.Fatalf("expected no ingest package_task when stream drm enabled, got %d", got)
	}
}

func TestUploadSingleSkipsDRMWhenLibraryDRMDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newUploadDRMHandler(t, 0)

	req := buildUploadSingleRequest(t, "movie.mp4", []byte("fake video"))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.UploadSingle(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	mediaID, _ := resp["id"].(float64)
	if mediaID <= 0 {
		t.Fatalf("invalid media id in response: %s", w.Body.String())
	}

	time.Sleep(200 * time.Millisecond)
	var n int
	if err := h.App.DB.QueryRow(`SELECT COUNT(1) FROM package_task WHERE media_id = ? AND pipeline_type = 'cmaf_drm'`, int64(mediaID)).Scan(&n); err != nil {
		t.Fatalf("query package_task: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no package_task when drm disabled, got %d", n)
	}
}

func TestUploadMergeSkipsIngestPackageForDRMEnabledLibrary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newUploadDRMHandler(t, 1)

	uploadID := "merge-drm-test"
	if _, err := h.Upload.SaveChunk(uploadID, 0, bytes.NewReader([]byte("fake merged video"))); err != nil {
		t.Fatalf("save chunk: %v", err)
	}

	body := map[string]any{
		"upload_id":   uploadID,
		"filename":    "movie.mp4",
		"total_parts": 1,
		"library_id":  1,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload/merge", io.NopCloser(bytes.NewReader(raw)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.UploadMerge(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	mediaID, _ := resp["id"].(float64)
	if mediaID <= 0 {
		t.Fatalf("invalid media id in response: %s", w.Body.String())
	}
	if got := waitForPackageTask(t, h.App.DB, int64(mediaID)); got != 0 {
		t.Fatalf("expected no ingest package_task for merged upload when stream drm enabled, got %d", got)
	}
}
