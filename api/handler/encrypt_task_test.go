package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

func TestEncryptTaskAdminCRUDAndEnqueue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "encrypt-task.db"))
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
	if _, err := db.Exec(`INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(2,'admin','x','admin',1,'all')`); err != nil {
		t.Fatal(err)
	}
	enabled := true
	q := postingest.NewQueue(db, "handler", nil)
	h := &Handler{
		App:            &app.App{DB: db, Config: &config.Config{EncryptedAssets: config.EncryptedAssetsConfig{Enabled: &enabled}}},
		Queue:          q,
		AssetEncryptor: &storage.AssetEncryptor{DB: db},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/media/10/encrypt-assets", nil)
	setUserCtx(c, 2, "admin", "admin")
	h.EncryptMediaAssets(c)
	if w.Code != http.StatusAccepted {
		t.Fatalf("enqueue status=%d body=%s", w.Code, w.Body.String())
	}
	var enq map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &enq); err != nil {
		t.Fatal(err)
	}
	if enq["status"] != "queued" {
		t.Fatalf("status=%v", enq["status"])
	}
	taskID := int64(enq["task_id"].(float64))

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Params = gin.Params{{Key: "id", Value: "10"}}
	c2.Request = httptest.NewRequest(http.MethodPost, "/api/v1/media/10/encrypt-assets", nil)
	setUserCtx(c2, 2, "admin", "admin")
	h.EncryptMediaAssets(c2)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("reenq status=%d body=%s", w2.Code, w2.Body.String())
	}
	var enq2 map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &enq2)
	if enq2["status"] != "already_queued" {
		t.Fatalf("status=%v", enq2["status"])
	}

	wList := httptest.NewRecorder()
	cList, _ := gin.CreateTestContext(wList)
	cList.Request = httptest.NewRequest(http.MethodGet, "/api/v1/encrypt/task?status=waiting", nil)
	h.ListEncryptTasks(cList)
	if wList.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", wList.Code, wList.Body.String())
	}

	idStr := strconv.FormatInt(taskID, 10)
	wCancel := httptest.NewRecorder()
	cCancel, _ := gin.CreateTestContext(wCancel)
	cCancel.Params = gin.Params{{Key: "id", Value: idStr}}
	cCancel.Request = httptest.NewRequest(http.MethodPost, "/api/v1/encrypt/task/"+idStr+"/cancel", nil)
	h.CancelEncryptTask(cCancel)
	if wCancel.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", wCancel.Code, wCancel.Body.String())
	}

	wReset := httptest.NewRecorder()
	cReset, _ := gin.CreateTestContext(wReset)
	cReset.Params = gin.Params{{Key: "id", Value: idStr}}
	cReset.Request = httptest.NewRequest(http.MethodPost, "/api/v1/encrypt/task/"+idStr+"/reset", nil)
	h.ResetEncryptTask(cReset)
	if wReset.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", wReset.Code, wReset.Body.String())
	}
	var round int
	if err := db.QueryRow(`SELECT retry_round FROM post_ingest_task WHERE id=?`, taskID).Scan(&round); err != nil || round != 1 {
		t.Fatalf("handler reset round=%d err=%v", round, err)
	}

	if _, err := db.Exec(`UPDATE post_ingest_task SET status='failed' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	wDel := httptest.NewRecorder()
	cDel, _ := gin.CreateTestContext(wDel)
	cDel.Params = gin.Params{{Key: "id", Value: idStr}}
	cDel.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/encrypt/task/"+idStr, nil)
	h.DeleteEncryptTask(cDel)
	if wDel.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", wDel.Code, wDel.Body.String())
	}
	var removedAt sql.NullString
	if err := db.QueryRow(`SELECT removed_at FROM post_ingest_task WHERE id=?`, taskID).Scan(&removedAt); err != nil || !removedAt.Valid {
		t.Fatalf("logical remove missing tombstone: %v %v", removedAt, err)
	}
	wHidden := httptest.NewRecorder()
	cHidden, _ := gin.CreateTestContext(wHidden)
	cHidden.Request = httptest.NewRequest(http.MethodGet, "/api/v1/encrypt/task?status=all", nil)
	h.ListEncryptTasks(cHidden)
	if wHidden.Code != http.StatusOK || strings.Contains(wHidden.Body.String(), idStr) {
		t.Fatalf("default list should hide removed: status=%d body=%s", wHidden.Code, wHidden.Body.String())
	}
	wShown := httptest.NewRecorder()
	cShown, _ := gin.CreateTestContext(wShown)
	cShown.Request = httptest.NewRequest(http.MethodGet, "/api/v1/encrypt/task?status=all&include_removed=1", nil)
	h.ListEncryptTasks(cShown)
	if wShown.Code != http.StatusOK || !strings.Contains(wShown.Body.String(), idStr) {
		t.Fatalf("include_removed list missing task: status=%d body=%s", wShown.Code, wShown.Body.String())
	}
}
