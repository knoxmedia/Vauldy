package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/publication"
	"knox-media/internal/store"
)

func seedAdminPrepareRetry(t *testing.T) (*Handler, int64, int64) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "admin-prepare-retry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	snapshot := `[{"rendition_id":0,"rendition_name":"360p","config_snapshot":{"preset":{"id":1},"rendition":{"name":"360p"},"output_path":"out","priority":"normal"}}]`
	if _, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(2,'admin','x','admin',1,'all'); INSERT INTO media(id,library_id,file_id,file_type,publication_state,publication_error,ingest_generation,published_at) VALUES(10,1,'prep','video','published','',1,'2026-07-01'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json) VALUES(20,10,1,'scan','published','{}'); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error) VALUES(30,20,10,1,'prepare',0,'failed',3,3,'old'); INSERT INTO transcode_task(id,file_id,status,progress,error_message,task_type,media_id,ingest_run_id,ingest_step_id,generation,retry_round) VALUES(40,'prep','failed',10,'old','pretranscode',10,20,30,1,0); INSERT INTO pretranscode_task_meta(task_id,preset_id,output_format,encryption_mode,priority,output_path,ingest_jobs_snapshot_json) VALUES(40,1,'hls','none','normal','root',?); INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status,progress,config_snapshot_json) VALUES(40,'old','failed',1,'{}')`, snapshot); err != nil {
		t.Fatal(err)
	}
	return New(&app.App{DB: db}, Dependencies{PublicationCapabilities: publication.NewCapabilityMatrix([]string{"prepare"})}), 10, 30
}

func TestAdminRetryOptionalPrepareSuccessAndRejectionMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, mediaID, stepID := seedAdminPrepareRetry(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/media/10/ingest/steps/30/retry", strings.NewReader(`{"reason":""}`))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "10"}, {Key: "step_id", Value: "30"}}
	setUserCtx(c, 2, "admin", "admin")
	h.AdminRetryOptionalScrape(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty reason status=%d body=%s", w.Code, w.Body.String())
	}

	h.PublicationCapabilities = publication.NewCapabilityMatrix(nil)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/media/10/ingest/steps/30/retry", bytes.NewBufferString(`{"reason":"retry"}`))
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "10"}, {Key: "step_id", Value: "30"}}
	setUserCtx(c, 2, "admin", "admin")
	h.AdminRetryOptionalScrape(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", w.Code, w.Body.String())
	}

	h.PublicationCapabilities = publication.NewCapabilityMatrix([]string{"prepare"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/media/10/ingest/steps/30/retry", bytes.NewBufferString(`{"reason":"operator"}`))
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "10"}, {Key: "step_id", Value: "30"}}
	setUserCtx(c, 2, "admin", "admin")
	h.AdminRetryOptionalScrape(c)
	if w.Code != http.StatusOK {
		t.Fatalf("success status=%d body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != true {
		t.Fatalf("payload=%v", payload)
	}
	var round int
	var status string
	if err := h.App.DB.QueryRow(`SELECT retry_round,status FROM transcode_task WHERE ingest_step_id=?`, stepID).Scan(&round, &status); err != nil {
		t.Fatal(err)
	}
	if round != 1 || status != "waiting" {
		t.Fatalf("task=%s/%d", status, round)
	}
	_ = mediaID
}
