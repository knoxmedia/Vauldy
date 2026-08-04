package handler

import (
	"context"
	"database/sql"
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
	"knox-media/internal/documenttask"
	"knox-media/internal/store"
)

func setupDocumentTaskHandler(t *testing.T) (*Handler, *sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	ds := documenttask.NewStore(db)
	if err := ds.EnsureSchema(context.Background()); err != nil {
		db.Close()
		t.Fatalf("ensure document task schema: %v", err)
	}

	// Seed library and media rows.
	db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'test','documents','/test')`)
	db.Exec(`INSERT INTO media(id,library_id,file_path,file_type,format,file_mtime,status,publication_state)
		VALUES(1,1,'/test/test.docx','document','docx',1000,'active','published')`)

	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	return h, db, func() { db.Close() }
}

func TestPreviewPDF_EnqueueBeforeResponse(t *testing.T) {
	h, db, cleanup := setupDocumentTaskHandler(t)
	defer cleanup()

	store := documenttask.NewStore(db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/1/document/preview.pdf", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.ServeDocumentPreviewTask(c)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "status_url") {
		t.Error("expected status_url in response")
	}

	task, err := store.GetByMediaID(context.Background(), 1)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != documenttask.StatusWaiting {
		t.Errorf("expected waiting, got %s", task.Status)
	}
}

func TestPreviewPDF_CommittedOutput(t *testing.T) {
	h, db, cleanup := setupDocumentTaskHandler(t)
	defer cleanup()

	store := documenttask.NewStore(db)

	artifactDir := filepath.Join(t.TempDir(), "committed", "1")
	os.MkdirAll(artifactDir, 0o755)
	pdfPath := filepath.Join(artifactDir, "preview.pdf")
	os.WriteFile(pdfPath, []byte("%PDF-1.4\ntest pdf"), 0o644)

	task, _ := store.Enqueue(context.Background(), 1, "/test/test.docx", 1)
	store.Claim(context.Background(), "worker-1", 30*time.Second)
	store.CommitDone(context.Background(), task.ID, "worker-1", 1,
		documenttask.ConvertOutput{PDFPath: pdfPath, PDFSize: 20, PDFHash: "abc", PageCount: 1},
		documenttask.EngineOffice)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/1/document/preview.pdf", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.ServeDocumentPreviewTask(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPreviewPDF_DocumentTaskStatus(t *testing.T) {
	h, db, cleanup := setupDocumentTaskHandler(t)
	defer cleanup()

	store := documenttask.NewStore(db)
	store.Enqueue(context.Background(), 1, "/test/test.docx", 1)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/1/document/task/status", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.DocumentTaskStatus(c)

	body := w.Body.String()
	if !strings.Contains(body, "waiting") {
		t.Errorf("expected waiting status, got: %s", body)
	}
}

func TestDocumentConvert_HTTPEnqueue(t *testing.T) {
	h, db, cleanup := setupDocumentTaskHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/media/1/document/convert", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.EnqueueDocumentConvert(c)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	store := documenttask.NewStore(db)
	task, err := store.GetByMediaID(context.Background(), 1)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != documenttask.StatusWaiting {
		t.Errorf("expected waiting, got %s", task.Status)
	}
}

func TestDocumentConvert_IdempotentHTTPEnqueue(t *testing.T) {
	h, db, cleanup := setupDocumentTaskHandler(t)
	defer cleanup()

	store := documenttask.NewStore(db)
	store.Enqueue(context.Background(), 1, "/test/test.docx", 1)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/media/1/document/convert", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.EnqueueDocumentConvert(c)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for idempotent enqueue, got %d: %s", w.Code, w.Body.String())
	}

	if !strings.Contains(w.Body.String(), "status_url") {
		t.Errorf("expected status_url in response: %s", w.Body.String())
	}
}
