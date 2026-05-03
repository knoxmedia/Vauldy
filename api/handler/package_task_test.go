package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func TestListTasksIncludesPipelineAndCleanupStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "task-list.sqlite")
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
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress, drm_status, source_cleanup_status, error_message, output_path) VALUES (1, 'cmaf_drm', 'done', 100, 'done', 'success', '', 'E:/out/master.m3u8')`); err != nil {
		t.Fatalf("insert package task: %v", err)
	}

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcode/task?limit=10", nil)
	c.Request = req

	h.ListTranscodeTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, expected := range []string{
		`"pipeline_type":"cmaf_drm"`,
		`"drm_status":"done"`,
		`"source_cleanup_status":"success"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected %s in body: %s", expected, body)
		}
	}
}

func TestGetTranscodeTaskStatusSupportsPackageTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "task-status.sqlite")
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
	res, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress, drm_status, source_cleanup_status, output_path) VALUES (1, 'cmaf_drm', 'done', 100, 'done', 'success', 'E:/out/master.m3u8')`)
	if err != nil {
		t.Fatalf("insert package task: %v", err)
	}
	taskID, _ := res.LastInsertId()

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcode/task/1/status", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Params[0].Value = strconv.FormatInt(taskID, 10)

	h.GetTranscodeTaskStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, expected := range []string{
		`"task_id":` + strconv.FormatInt(taskID, 10),
		`"status":"done"`,
		`"quality":"cmaf_drm"`,
		`"ready":true`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected %s in body: %s", expected, body)
		}
	}
}

func TestCancelTranscodeTaskSupportsPackageTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "task-cancel.sqlite")
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
	res, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress) VALUES (1, 'cmaf_drm', 'waiting', 0)`)
	if err != nil {
		t.Fatalf("insert package task: %v", err)
	}
	taskID, _ := res.LastInsertId()

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/transcode/task/"+strconv.FormatInt(taskID, 10)+"/cancel", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(taskID, 10)}}
	h.CancelTranscodeTask(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM package_task WHERE id = ?`, taskID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("status=%s want cancelled", status)
	}
}

func TestRetryTranscodeTaskSupportsFailedPackageTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "task-retry.sqlite")
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
	res, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress, error_message) VALUES (1, 'cmaf_drm', 'failed', 0, 'x')`)
	if err != nil {
		t.Fatalf("insert package task: %v", err)
	}
	taskID, _ := res.LastInsertId()

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/transcode/task/"+strconv.FormatInt(taskID, 10)+"/retry", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(taskID, 10)}}
	h.RetryTranscodeTask(c)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM package_task WHERE id = ?`, taskID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "waiting" && status != "running" {
		t.Fatalf("status=%s want waiting|running", status)
	}
}
