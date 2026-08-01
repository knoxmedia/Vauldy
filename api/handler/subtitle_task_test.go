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
	"knox-media/internal/subtitle"
)

func TestListSubtitleTasksIncludesOnlyCurrentGenerationAndDomainRows(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "subtitle-list.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','x')`); err != nil {
		t.Fatal(err)
	}
	for _, media := range []struct {
		id    int
		title string
	}{
		{1, "queue-only"},
		{2, "with-domain"},
		{3, "domain-only"},
		{4, "stale-only"},
		{5, "failed-queue"},
		{6, "cancelled-queue"},
	} {
		if _, err := db.Exec(`INSERT INTO media(id,library_id,title,file_path,file_type,ingest_generation) VALUES(?,?,?,'x','video',2)`, media.id, 1, media.title); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,last_error,created_at,started_at,finished_at,updated_at) VALUES
		(2,2,'subtitle','running','queue running','2026-08-01 10:02:00','2026-08-01 10:03:00',NULL,'2026-08-01 10:04:00'),
		(1,2,'subtitle','waiting','queued message','2026-08-01 10:00:00',NULL,NULL,'2026-08-01 10:01:00'),
		(2,1,'subtitle','failed','stale current-domain media','2026-08-01 10:06:00',NULL,'2026-08-01 10:07:00','2026-08-01 10:07:00'),
		(4,1,'subtitle','waiting','stale-only message','2026-08-01 10:08:00',NULL,NULL,'2026-08-01 10:08:00'),
		(5,2,'subtitle','failed','current failure','2026-08-01 10:09:00',NULL,'2026-08-01 10:10:00','2026-08-01 10:10:00'),
		(6,2,'subtitle','cancelled','current cancellation','2026-08-01 10:11:00',NULL,'2026-08-01 10:12:00','2026-08-01 10:12:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO subtitle_task(media_id,status,message,created_at,started_at,finished_at,updated_at) VALUES
		(2,'pending','domain message','2026-08-01 09:00:00',NULL,NULL,'2026-08-01 09:01:00'),
		(3,'done','manual','2025-01-01','2025-01-01 00:01:00','2025-01-01 00:02:00','2025-01-02'),
		(5,'pending','',NULL,NULL,NULL,NULL),
		(6,'pending','',NULL,NULL,NULL,NULL)`); err != nil {
		t.Fatal(err)
	}

	h := &Handler{App: &app.App{DB: db}, Subtitle: &subtitle.Service{DB: db}}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/subtitle/task", nil)
	h.ListSubtitleTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var response struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 5 {
		t.Fatalf("items = %d, want 5: %s", len(response.Items), w.Body.String())
	}

	wantOrder := []int{2, 1, 5, 6, 3}
	byMedia := make(map[int]map[string]any, len(response.Items))
	for i, item := range response.Items {
		mediaID := int(item["media_id"].(float64))
		if mediaID != wantOrder[i] {
			t.Errorf("item %d media_id = %d, want active order %d", i, mediaID, wantOrder[i])
		}
		if got := int(item["id"].(float64)); got != mediaID {
			t.Errorf("media %d id = %d, want stable table id equal to media_id", mediaID, got)
		}
		if _, duplicate := byMedia[mediaID]; duplicate {
			t.Errorf("duplicate row for media_id %d", mediaID)
		}
		byMedia[mediaID] = item
	}
	if _, exists := byMedia[4]; exists {
		t.Errorf("stale-generation-only media was returned: %#v", byMedia[4])
	}

	queueOnly := byMedia[1]
	if queueOnly["status"] != "waiting" || queueOnly["domain_status"] != "" {
		t.Errorf("queue-only status = %#v", queueOnly)
	}
	if got := int(queueOnly["queue_task_id"].(float64)); got == 0 {
		t.Errorf("queue-only queue_task_id = %d, want nonzero", got)
	}
	for field, want := range map[string]string{
		"message":    "queued message",
		"created_at": "2026-08-01 10:00:00",
		"updated_at": "2026-08-01 10:01:00",
	} {
		if got := queueOnly[field]; got != want {
			t.Errorf("queue-only %s = %#v, want %q", field, got, want)
		}
	}

	withDomain := byMedia[2]
	if withDomain["status"] != "running" || withDomain["queue_status"] != "running" {
		t.Errorf("current queue was not synthesized: %#v", withDomain)
	}
	for field, want := range map[string]string{
		"message":    "domain message",
		"created_at": "2026-08-01 09:00:00",
		"started_at": "2026-08-01 10:03:00",
		"updated_at": "2026-08-01 09:01:00",
	} {
		if got := withDomain[field]; got != want {
			t.Errorf("domain/queue %s = %#v, want %q", field, got, want)
		}
	}
	if byMedia[3]["domain_status"] != "done" || byMedia[3]["queue_status"] != "" {
		t.Errorf("domain-only row = %#v", byMedia[3])
	}
	if byMedia[5]["status"] != "failed" {
		t.Errorf("failed queue plus pending domain status = %#v", byMedia[5])
	}
	if byMedia[6]["status"] != "cancelled" {
		t.Errorf("cancelled queue plus pending domain status = %#v", byMedia[6])
	}
}

func TestListSubtitleTasksHidesAdminDeletedMarkerOnly(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "subtitle-deleted-list.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'l','video','x');
		INSERT INTO media(id,library_id,file_id,title,file_type,ingest_generation) VALUES
			(10,1,'f10','deleted','video',1),(11,1,'f11','ordinary','video',1);
		INSERT INTO post_ingest_task(media_id,generation,task_type,status,last_error) VALUES
			(10,1,'subtitle','cancelled','deleted by admin'),
			(11,1,'subtitle','cancelled','cancelled by admin')`); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}, Subtitle: &subtitle.Service{DB: db}}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/subtitle/task", nil)
	h.ListSubtitleTasks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || int(response.Items[0]["media_id"].(float64)) != 11 {
		t.Fatalf("items=%#v, want only ordinary cancelled", response.Items)
	}
}
