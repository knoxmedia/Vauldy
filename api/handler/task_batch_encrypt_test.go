package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

func TestBatchEncryptRetryAndDeleteShareAdminSemantics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "batch-encrypt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	plain := filepath.Join(t.TempDir(), "a.mp4")
	if err := os.WriteFile(plain, []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'l','video','/tmp',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type) VALUES(10,1,'f','t',?,'video')`, plain); err != nil {
		t.Fatal(err)
	}
	q := postingest.NewQueue(db, "batch", nil)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: q}
	id, _, err := q.EnqueueEncryptManual(httptest.NewRequest(http.MethodGet, "/", nil).Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.AdminCancelEncrypt(httptest.NewRequest(http.MethodGet, "/", nil).Context(), id); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"action": "retry", "ids": []int64{id}})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/encrypt/task/batch", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.BatchEncryptTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("batch retry status=%d body=%s", w.Code, w.Body.String())
	}
	var round int
	if err := db.QueryRow(`SELECT retry_round FROM post_ingest_task WHERE id=?`, id).Scan(&round); err != nil || round != 1 {
		t.Fatalf("batch retry round=%d err=%v", round, err)
	}

	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(map[string]any{"action": "delete", "ids": []int64{id}})
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/api/v1/encrypt/task/batch", bytes.NewReader(body))
	c2.Request.Header.Set("Content-Type", "application/json")
	h.BatchEncryptTasks(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("batch delete status=%d body=%s", w2.Code, w2.Body.String())
	}
	var removed interface{}
	if err := db.QueryRow(`SELECT removed_at FROM post_ingest_task WHERE id=?`, id).Scan(&removed); err != nil || removed == nil {
		t.Fatalf("batch delete tombstone missing: %v %v", removed, err)
	}
}
