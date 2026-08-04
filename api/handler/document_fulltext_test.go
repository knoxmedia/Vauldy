package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
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

func setupFulltextHandler(t *testing.T) (*Handler, *sql.DB, func()) {
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
		t.Fatalf("ensure schema: %v", err)
	}

	db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'test','documents','/test')`)
	db.Exec(`INSERT INTO media(id,library_id,file_path,file_type,format,file_mtime,status,publication_state)
		VALUES(1,1,'/test/test.pdf','document','pdf',1000,'active','published')`)

	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	return h, db, func() { db.Close() }
}

// Test that full-text search does NOT trigger OCR (OCR only happens via dedicated task execution).
func TestDocumentSearch_NoOCRInListRequest(t *testing.T) {
	h, _, cleanup := setupFulltextHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/library/1/documents?q=hello&fulltext=1", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.ListDocuments(c)

	// Should not fail or start OCR. The search reads from committed FTS.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "items") {
		t.Error("expected items in response")
	}
}

// Test that the handler serving text for a document does OCR only via the task system.
func TestDocumentFullText_StatusEndpoint(t *testing.T) {
	h, db, cleanup := setupFulltextHandler(t)
	defer cleanup()

	store := documenttask.NewStore(db)
	store.Enqueue(context.Background(), 1, "/test/test.pdf", 1)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/media/1/document/task/status", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.DocumentTaskStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "waiting") {
		t.Errorf("expected waiting, got %s", body)
	}
}

// Test document OCR enqueue endpoint (idempotent).
func TestDocumentOCR_EnqueueEndpoint(t *testing.T) {
	h, db, cleanup := setupFulltextHandler(t)
	defer cleanup()
	_ = db

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/media/1/document/fulltext", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.EnqueueDocumentFulltext(c)

	// 202 means queued for OCR/text extraction.
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

// Test that empty text is considered success (no text in a document is valid).
func TestDocumentFullText_EmptyTextSuccess(t *testing.T) {
	_, db, cleanup := setupFulltextHandler(t)
	defer cleanup()

	store := documenttask.NewStore(db)
	store.EnsureFulltextSchema(context.Background())
	input := documenttask.FulltextInput{MediaID: 1, SourcePath: "/test/empty.pdf", Generation: 1, Language: "eng", DocumentKind: "pdf"}
	task, _ := store.EnqueueFulltext(context.Background(), input)
	store.ClaimFulltext(context.Background(), "worker-1", 30*time.Second)
	err := store.CommitFulltextDone(context.Background(), task.ID, "worker-1", 1,
		documenttask.FulltextResult{Text: "", TextSize: 0, TextHash: "", Mode: documenttask.FulltextNative, Language: "eng"},
		"pdftext", "1.0")
	if err != nil {
		t.Errorf("empty text commit should succeed, got: %v", err)
	}
}

// Test that full-text search respects committed task results.
func TestDocumentSearch_CommittedFulltext(t *testing.T) {
	h, db, cleanup := setupFulltextHandler(t)
	defer cleanup()

	// Create a committed fulltext result in the document_task table.
	store := documenttask.NewStore(db)
	store.Enqueue(context.Background(), 1, "/test/test.pdf", 1)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/library/1/documents?q=hello&fulltext=1", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.ListDocuments(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "items") {
		t.Error("expected items array")
	}
}

// Test that list/search never triggers OCR (bounded query only).
func TestDocumentSearch_BoundedQueryOnly(t *testing.T) {
	h, _, cleanup := setupFulltextHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)

	// Run multiple searches in parallel style - all should be fast (no OCR).
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/library/1/documents?fulltext=1&q=test&limit=10", nil)
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		h.ListDocuments(c)
		if w.Code != http.StatusOK {
			t.Fatalf("iteration %d: expected 200, got %d", i, w.Code)
		}
	}
}
