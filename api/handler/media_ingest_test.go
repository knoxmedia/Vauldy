package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/app"
	"knox-media/internal/publication"
	"knox-media/internal/store"
)

func setupMediaIngestTestHandler(t *testing.T) *Handler {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "media-ingest.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
INSERT INTO library(id,name,type,path,enabled) VALUES(1,'movies','movie','E:/movies',1);
INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(2,'admin','x','admin',1,'all');
INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,publication_error,ingest_generation) VALUES
 (101,1,'processing','Processing','E:/movies/processing.mkv','video','active','processing','',1),
 (102,1,'degraded','Degraded','E:/movies/degraded.mkv','video','active','degraded','poster failed',1),
 (103,1,'published','Published','E:/movies/published.mkv','video','active','published','',1);
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,preserve_visibility,config_snapshot_json,error_message) VALUES
 (201,101,1,'scan','processing',0,'{"library_id":1,"file_type":"video","steps":["poster","scrape"]}',''),
 (202,102,1,'scan','degraded',1,'{"library_id":1,"file_type":"video","steps":["poster","scrape"]}','poster failed'),
 (203,103,1,'scan','published',0,'{"library_id":1,"file_type":"video","steps":["poster","scrape"]}','');
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error) VALUES
 (301,201,101,1,'poster',1,'running',1,3,''),(302,201,101,1,'scrape',1,'waiting',0,3,''),
 (303,202,102,1,'poster',1,'failed',3,3,'poster failed'),(304,202,102,1,'scrape',1,'done',1,3,''),
 (305,202,102,1,'preview',0,'failed',3,3,'optional failed'),
 (306,203,103,1,'poster',1,'done',1,3,''),(307,203,103,1,'scrape',1,'done',1,3,'');
INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,last_error)
 VALUES(102,202,303,1,'poster','failed',3,3,'poster failed');
INSERT INTO scrape_task(media_id,source,status,progress,ingest_run_id,ingest_step_id,generation)
 VALUES(102,'auto-scan','done',100,202,304,1);
`)
	if err != nil {
		t.Fatal(err)
	}
	return New(&app.App{DB: db}, Dependencies{PublicationPlanner: publication.NewPlanner(publication.PlanOptions{})})
}

func adminIngestContext(method, target, id string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	setUserCtx(c, 2, "admin", "admin")
	return c, w
}

func TestAdminListMediaCanFilterPublicationState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	c, w := adminIngestContext(http.MethodGet, "/api/v1/admin/media?publication_state=processing&limit=20", "")
	h.AdminListMedia(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Items []struct {
			ID               int64  `json:"id"`
			PublicationState string `json:"publication_state"`
			IngestGeneration int64  `json:"ingest_generation"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ID != 101 || payload.Items[0].PublicationState != "processing" || payload.Items[0].IngestGeneration != 1 {
		t.Fatalf("items=%+v body=%s", payload.Items, w.Body.String())
	}
}

func TestAdminListMediaReturnsAndAcceptsNextCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	first, w1 := adminIngestContext(http.MethodGet, "/api/v1/admin/media?sort=id_desc&limit=2", "")
	h.AdminListMedia(first)
	var page1 struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	if err := json.Unmarshal(w1.Body.Bytes(), &page1); err != nil {
		t.Fatal(err)
	}
	if w1.Code != http.StatusOK || len(page1.Items) != 2 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("page1 status=%d payload=%+v body=%s", w1.Code, page1, w1.Body.String())
	}
	second, w2 := adminIngestContext(http.MethodGet, "/api/v1/admin/media?sort=id_desc&limit=2&cursor="+page1.NextCursor, "")
	h.AdminListMedia(second)
	var page2 struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &page2); err != nil {
		t.Fatal(err)
	}
	if w2.Code != http.StatusOK || len(page2.Items) != 1 || page2.HasMore || page2.NextCursor != "" || page2.Items[0].ID == page1.Items[0].ID || page2.Items[0].ID == page1.Items[1].ID {
		t.Fatalf("page2 status=%d payload=%+v body=%s", w2.Code, page2, w2.Body.String())
	}
}

func TestAdminListMediaLimit500UsesLookahead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	if _, err := h.App.DB.Exec(`DELETE FROM media`); err != nil {
		t.Fatal(err)
	}
	tx, err := h.App.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,1,?,?,?,'video','active','published',1)`)
	if err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 501; id++ {
		if _, err := stmt.Exec(id, fmt.Sprintf("f-%d", id), fmt.Sprintf("Media %d", id), fmt.Sprintf("E:/movies/%d.mkv", id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	readPage := func(target string) struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} {
		c, w := adminIngestContext(http.MethodGet, target, "")
		h.AdminListMedia(c)
		var page struct {
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		return page
	}
	page1 := readPage("/api/v1/admin/media?sort=id_desc&limit=500")
	if len(page1.Items) != 500 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("page1=%+v", page1)
	}
	page2 := readPage("/api/v1/admin/media?sort=id_desc&limit=500&cursor=" + page1.NextCursor)
	if len(page2.Items) != 1 || page2.HasMore || page2.NextCursor != "" {
		t.Fatalf("page2=%+v", page2)
	}
	seen := make(map[int64]bool, 501)
	for _, item := range page1.Items {
		seen[item.ID] = true
	}
	if seen[page2.Items[0].ID] || len(seen) != 500 {
		t.Fatalf("overlap page2=%d seen=%d", page2.Items[0].ID, len(seen))
	}
}

func TestAdminGetMediaIngestReturnsCurrentOrderedSteps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	c, w := adminIngestContext(http.MethodGet, "/api/v1/admin/media/101/ingest", "101")
	h.AdminGetMediaIngest(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Media struct {
			ID               int64  `json:"id"`
			IngestGeneration int64  `json:"ingest_generation"`
			PublicationState string `json:"publication_state"`
		} `json:"media"`
		Run struct {
			ID, Generation     int64
			Status, Reason     string
			PreserveVisibility bool `json:"preserve_visibility"`
		} `json:"run"`
		Steps []struct {
			ID                    int64
			Type, Status          string
			Required              bool
			Attempts, MaxAttempts int
		} `json:"steps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Media.ID != 101 || payload.Media.IngestGeneration != 1 || payload.Run.ID != 201 || payload.Run.Generation != 1 {
		t.Fatalf("payload=%+v", payload)
	}
	if len(payload.Steps) != 2 || payload.Steps[0].Type != "poster" || payload.Steps[1].Type != "scrape" {
		t.Fatalf("steps=%+v", payload.Steps)
	}
}

func TestAdminRetryDegradedIngestCreatesVisibilityPreservingGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET published_at='2026-07-01 02:03:04' WHERE id=102`); err != nil {
		t.Fatal(err)
	}
	c, w := adminIngestContext(http.MethodPost, "/api/v1/admin/media/102/ingest/retry", "102")
	h.AdminRetryMediaIngest(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Media struct {
			IngestGeneration int64  `json:"ingest_generation"`
			PublicationState string `json:"publication_state"`
			PublicationError string `json:"publication_error"`
			PublishedAt      string `json:"published_at"`
		} `json:"media"`
		Run struct {
			Generation         int64  `json:"generation"`
			Status             string `json:"status"`
			Reason             string `json:"reason"`
			PreserveVisibility bool   `json:"preserve_visibility"`
		} `json:"run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Media.IngestGeneration != 2 || payload.Media.PublicationState != "degraded" || payload.Media.PublicationError != "poster failed" || payload.Media.PublishedAt != "2026-07-01 02:03:04" ||
		payload.Run.Generation != 2 || payload.Run.Status != "processing" || payload.Run.Reason != "manual_retry" || !payload.Run.PreserveVisibility {
		t.Fatalf("payload=%+v body=%s", payload, w.Body.String())
	}
	var generation, preserve, oldAttempts int
	var mediaState, mediaErr, publishedAt, reason, runState, oldStep, oldTask string
	if err := h.App.DB.QueryRow(`SELECT ingest_generation,publication_state,publication_error,published_at FROM media WHERE id=102`).Scan(&generation, &mediaState, &mediaErr, &publishedAt); err != nil {
		t.Fatal(err)
	}
	if err := h.App.DB.QueryRow(`SELECT reason,status,preserve_visibility FROM media_ingest_run WHERE media_id=102 AND generation=2`).Scan(&reason, &runState, &preserve); err != nil {
		t.Fatal(err)
	}
	if err := h.App.DB.QueryRow(`SELECT status,attempts FROM media_ingest_step WHERE id=303`).Scan(&oldStep, &oldAttempts); err != nil {
		t.Fatal(err)
	}
	if err := h.App.DB.QueryRow(`SELECT status FROM post_ingest_task WHERE ingest_step_id=303`).Scan(&oldTask); err != nil {
		t.Fatal(err)
	}
	if generation != 2 || mediaState != "degraded" || mediaErr != "poster failed" || publishedAt != "2026-07-01T02:03:04Z" {
		t.Fatalf("media=%d/%s/%q/%s", generation, mediaState, mediaErr, publishedAt)
	}
	if reason != "manual_retry" || runState != "processing" || preserve != 1 || oldStep != "failed" || oldAttempts != 3 || oldTask != "failed" {
		t.Fatalf("new=%s/%s/%d old=%s/%d/%s", reason, runState, preserve, oldStep, oldAttempts, oldTask)
	}
}

func TestOrdinaryUserCannotAccessMediaIngestAdminRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	r := gin.New()
	admin := r.Group("/api/v1/admin")
	admin.Use(func(c *gin.Context) { setUserCtx(c, 1, "user", "viewer") })
	admin.Use(middleware.RequireAdmin())
	admin.GET("/media", h.AdminListMedia)
	admin.GET("/media/:id/ingest", h.AdminGetMediaIngest)
	admin.POST("/media/:id/ingest/retry", h.AdminRetryMediaIngest)
	for _, tc := range []struct{ method, path string }{{http.MethodGet, "/api/v1/admin/media"}, {http.MethodGet, "/api/v1/admin/media/101/ingest"}, {http.MethodPost, "/api/v1/admin/media/101/ingest/retry"}} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestAdminMediaIngestValidationAndNoRun(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	for _, tc := range []struct {
		call       func(*gin.Context)
		target, id string
		want       int
	}{
		{h.AdminListMedia, "/api/v1/admin/media?publication_state=bogus", "", 400},
		{h.AdminGetMediaIngest, "/api/v1/admin/media/nope/ingest", "nope", 400},
		{h.AdminGetMediaIngest, "/api/v1/admin/media/999/ingest", "999", 404},
	} {
		c, w := adminIngestContext(http.MethodGet, tc.target, tc.id)
		tc.call(c)
		if w.Code != tc.want {
			t.Fatalf("%s status=%d body=%s", tc.target, w.Code, w.Body.String())
		}
	}
	if _, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(104,1,'no-run','E:/movies/no.mkv','video','active','failed',1)`); err != nil {
		t.Fatal(err)
	}
	c, w := adminIngestContext(http.MethodGet, "/api/v1/admin/media/104/ingest", "104")
	h.AdminGetMediaIngest(c)
	if w.Code != 404 {
		t.Fatalf("no run status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminRetryTerminalIngestCreatesNewGenerationWithAllExecutions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	h.PublicationPlanner = publication.NewPlanner(publication.PlanOptions{EncryptGlobal: true})
	if _, err := h.App.DB.Exec(`UPDATE library SET encrypted_assets_enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`UPDATE media SET publication_state='failed' WHERE id=101; UPDATE media_ingest_run SET status='failed' WHERE id=201; UPDATE media_ingest_step SET status='failed' WHERE run_id=201; INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(101,201,301,1,'poster','failed'); INSERT INTO scrape_task(media_id,source,status,ingest_run_id,ingest_step_id,generation) VALUES(101,'auto-scan','failed',201,302,1)`); err != nil {
		t.Fatal(err)
	}
	c, w := adminIngestContext(http.MethodPost, "/api/v1/admin/media/101/ingest/retry", "101")
	h.AdminRetryMediaIngest(c)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var generation, runs, steps, post, scrape, encrypt int
	h.App.DB.QueryRow(`SELECT ingest_generation FROM media WHERE id=101`).Scan(&generation)
	h.App.DB.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=101 AND reason='manual_retry'`).Scan(&runs)
	h.App.DB.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=101 AND generation=2`).Scan(&steps)
	h.App.DB.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=101 AND generation=2`).Scan(&post)
	h.App.DB.QueryRow(`SELECT COUNT(*) FROM scrape_task WHERE media_id=101 AND generation=2`).Scan(&scrape)
	h.App.DB.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=101 AND generation=2 AND step_type='encrypt' AND required=1`).Scan(&encrypt)
	if generation != 2 || runs != 1 || steps != 4 || post != 2 || scrape != 1 || encrypt != 1 {
		t.Fatalf("generation=%d runs=%d steps=%d post=%d scrape=%d encrypt=%d", generation, runs, steps, post, scrape, encrypt)
	}
	c, w = adminIngestContext(http.MethodPost, "/api/v1/admin/media/101/ingest/retry", "101")
	h.AdminRetryMediaIngest(c)
	if w.Code != 409 {
		t.Fatalf("second retry status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminRetryConcurrentTerminalIngestCreatesOneGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	if _, err := h.App.DB.Exec(`UPDATE media SET publication_state='failed' WHERE id=101; UPDATE media_ingest_run SET status='failed' WHERE id=201; UPDATE media_ingest_step SET status='failed' WHERE run_id=201; INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(101,201,301,1,'poster','failed'); INSERT INTO scrape_task(media_id,source,status,ingest_run_id,ingest_step_id,generation) VALUES(101,'auto-scan','failed',201,302,1)`); err != nil {
		t.Fatal(err)
	}
	codes := make(chan int, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			c, w := adminIngestContext(http.MethodPost, "/api/v1/admin/media/101/ingest/retry", "101")
			h.AdminRetryMediaIngest(c)
			codes <- w.Code
		}()
	}
	close(start)
	a, b := <-codes, <-codes
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("concurrent codes=%d,%d", a, b)
	}
	var generation, runs int
	h.App.DB.QueryRow(`SELECT ingest_generation FROM media WHERE id=101`).Scan(&generation)
	h.App.DB.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=101 AND generation=2`).Scan(&runs)
	if generation != 2 || runs != 1 {
		t.Fatalf("generation=%d runs=%d", generation, runs)
	}
}

func TestAdminRetryDegradedUsesFreshCurrentPolicyExecutions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	if _, err := h.App.DB.Exec(`UPDATE library SET preview_extract=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	c, w := adminIngestContext(http.MethodPost, "/api/v1/admin/media/102/ingest/retry", "102")
	h.AdminRetryMediaIngest(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var steps, executions, legacy int
	h.App.DB.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=102 AND generation=2`).Scan(&steps)
	h.App.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM post_ingest_task WHERE media_id=102 AND generation=2)+(SELECT COUNT(*) FROM scrape_task WHERE media_id=102 AND generation=2)`).Scan(&executions)
	h.App.DB.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=102 AND generation=2 AND step_type IN ('keyframe','atrack')`).Scan(&legacy)
	if steps != 4 || executions != 3 || legacy != 0 {
		t.Fatalf("steps=%d executions=%d legacy=%d", steps, executions, legacy)
	}
}

func TestAdminListMediaIncludesCurrentIngestRunStatusOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupMediaIngestTestHandler(t)
	if _, err := h.App.DB.Exec(`UPDATE media_ingest_run SET status='processing' WHERE media_id=102 AND generation=1;
INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,publication_error,ingest_generation)
VALUES(104,1,'no-run','No Run','E:/movies/no-run.mkv','video','active','degraded','legacy degraded',1)`); err != nil {
		t.Fatal(err)
	}
	adminContext, adminRecorder := adminIngestContext(http.MethodGet, "/api/v1/admin/media?limit=20", "")
	h.AdminListMedia(adminContext)
	if adminRecorder.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", adminRecorder.Code, adminRecorder.Body.String())
	}
	var adminPayload struct {
		Items []struct {
			ID               int64  `json:"id"`
			PublicationState string `json:"publication_state"`
			IngestRunStatus  string `json:"ingest_run_status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(adminRecorder.Body.Bytes(), &adminPayload); err != nil {
		t.Fatal(err)
	}
	items := make(map[int64]struct{ publicationState, ingestRunStatus string }, len(adminPayload.Items))
	for _, item := range adminPayload.Items {
		items[item.ID] = struct{ publicationState, ingestRunStatus string }{item.PublicationState, item.IngestRunStatus}
	}
	if got := items[102]; got.publicationState != "degraded" || got.ingestRunStatus != "processing" {
		t.Fatalf("degraded retry item=%+v body=%s", got, adminRecorder.Body.String())
	}
	if got := items[104]; got.publicationState != "degraded" || got.ingestRunStatus != "" {
		t.Fatalf("no-run item=%+v body=%s", got, adminRecorder.Body.String())
	}
	ordinaryContext, ordinaryRecorder := listMediaTestContext("/api/v1/media?library_id=1&limit=20", 2)
	h.ListMedia(ordinaryContext)
	if ordinaryRecorder.Code != http.StatusOK {
		t.Fatalf("ordinary status=%d body=%s", ordinaryRecorder.Code, ordinaryRecorder.Body.String())
	}
	var ordinaryPayload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(ordinaryRecorder.Body.Bytes(), &ordinaryPayload); err != nil {
		t.Fatal(err)
	}
	for _, item := range ordinaryPayload.Items {
		if _, exists := item["ingest_run_status"]; exists {
			t.Fatalf("ordinary list exposed ingest_run_status: %s", ordinaryRecorder.Body.String())
		}
	}
}
