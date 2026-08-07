package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/coreiface"
	"knox-media/internal/postingest"
	"knox-media/internal/publication"
	"knox-media/internal/scanner"
	"knox-media/internal/storage"
	"knox-media/internal/store"
	"knox-media/pkg/ffprobe"
)

type publicationE2E struct {
	db                                *sql.DB
	h                                 *Handler
	root, upload                      string
	libraryID, scanID, mediaID, runID int64
}

func newPublicationE2E(t *testing.T, all bool) *publicationE2E {
	t.Helper()
	root, upload := t.TempDir(), t.TempDir()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "publication-e2e.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	preview, encrypted, prepare := 0, 0, 0
	opts := publication.PlanOptions{}
	if all {
		preview, encrypted, prepare = 1, 1, 1
		preparePlanner := coreiface.IngestPreparePlannerHandle()
		prepareCapabilities := publication.NewCapabilityMatrix(nil)
		if preparePlanner != nil {
			prepareCapabilities = publication.NewCapabilityMatrix([]string{"prepare"})
		}
		opts = publication.PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true, PreparePlanner: preparePlanner, Capabilities: prepareCapabilities}
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path,preview_extract,encrypted_assets_enabled,jit_prepare_on_ingest) VALUES('movies','video',?,?,?,?)`, root, preview, encrypted, prepare)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanID, _ := res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(1,'viewer','x','user',1,'all'),(2,'admin','x','admin',1,'all')`); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(root, "Task15.Movie.2026.mp4")
	if err = os.WriteFile(video, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc := &scanner.Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		return &ffprobe.Summary{Width: 1, Height: 1}, nil
	}}
	planner := publication.NewPlanner(opts)
	added, err := sc.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), libraryID, []string{root}, scanner.ScanCallbacks{OnMediaDiscoveredTx: func(ctx context.Context, tx *sql.Tx, discovery scanner.ScanDiscovery) error {
		diagnostics := make([]publication.MetadataDiagnostic, len(discovery.MetadataAttempt.Errors))
		for i, diagnostic := range discovery.MetadataAttempt.Errors {
			diagnostics[i] = publication.MetadataDiagnostic{Source: diagnostic.Source, Message: diagnostic.Message}
		}
		_, e := planner.PlanNewMediaTx(ctx, tx, publication.NewMedia{
			MediaID: discovery.MediaID, ScanTaskID: scanID, FileType: discovery.FileType,
			MetadataAttempt: publication.MetadataAttempt{Attempted: discovery.MetadataAttempt.Attempted, Fields: append([]string(nil), discovery.MetadataAttempt.Fields...), Errors: diagnostics},
		})
		return e
	}})
	if err != nil || added != 1 {
		t.Fatalf("scan added=%d err=%v", added, err)
	}
	var mediaID, runID int64
	if err = db.QueryRow(`SELECT id FROM media WHERE library_id=?`, libraryID).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT id FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Data.Upload = upload
	h := &Handler{App: &app.App{DB: db, Config: cfg}, Queue: postingest.NewQueue(db, "task15", nil), runningScans: map[int64]scanRuntime{}}
	return &publicationE2E{db: db, h: h, root: root, upload: upload, libraryID: libraryID, scanID: scanID, mediaID: mediaID, runID: runID}
}

func e2eRequest(t *testing.T, e *publicationE2E, admin bool, method, target string, call func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	if admin {
		setUserCtx(c, 2, "admin", "admin")
	} else {
		setUserCtx(c, 1, "user", "viewer")
	}
	if e.mediaID > 0 {
		c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(e.mediaID)}}
	}
	call(c)
	return w
}

func completePostIngest(t *testing.T, e *publicationE2E, typ postingest.TaskType) {
	t.Helper()
	task, err := e.h.Queue.Claim(context.Background(), typ)
	if err != nil || task == nil {
		t.Fatalf("claim %s task=%v err=%v", typ, task, err)
	}
	if typ == postingest.TaskPoster {
		dir := filepath.Join(e.upload, "posters")
		if err = os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.jpg", e.mediaID)), []byte("jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err = e.db.Exec(`UPDATE media SET meta_json=json_set(COALESCE(NULLIF(meta_json,''),'{}'),'$.scrape.poster',?) WHERE id=?`, storage.PlainPosterURL(e.mediaID), e.mediaID); err != nil {
			t.Fatal(err)
		}
	}
	if err = e.h.Queue.Complete(context.Background(), *task); err != nil {
		t.Fatal(err)
	}
}

func completeScrape(t *testing.T, e *publicationE2E) {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM scrape_task WHERE ingest_run_id=?`, e.runID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	claim, err := claimScrapeTaskWithOwner(context.Background(), e.db, id)
	if err != nil || claim == nil {
		t.Fatalf("claim scrape=%v err=%v", claim, err)
	}
	if err = completeScrapeTaskTx(context.Background(), e.db, id, e.mediaID, "auto-scan", "task15", "done", `{"ok":true}`, claim.Owner); err != nil {
		t.Fatal(err)
	}
}

func TestNewVideoHiddenUntilRequiredIngestCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := newPublicationE2E(t, true)
	if w := e2eRequest(t, e, false, http.MethodGet, "/api/v1/media", e.h.ListMedia); w.Code != 200 || w.Body.String() != `{"items":[]}` {
		t.Fatalf("ordinary list status=%d body=%s", w.Code, w.Body.String())
	}
	if w := e2eRequest(t, e, false, http.MethodGet, fmt.Sprintf("/api/v1/media/%d", e.mediaID), e.h.GetMedia); w.Code != 404 {
		t.Fatalf("ordinary detail status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := e.db.Exec(`INSERT INTO play_progress(user_id,file_id,position,play_count,update_at) SELECT 1,file_id,10,1,CURRENT_TIMESTAMP FROM media WHERE id=?`, e.mediaID); err != nil {
		t.Fatal(err)
	}
	if w := e2eRequest(t, e, false, http.MethodGet, "/api/v1/playback-history?range=all", e.h.ListPlaybackHistory); w.Code != 200 {
		t.Fatalf("ordinary history status=%d body=%s", w.Code, w.Body.String())
	} else {
		var history struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || len(history.Items) != 0 {
			t.Fatalf("ordinary history leaked processing media: err=%v body=%s", err, w.Body.String())
		}
	}
	w := e2eRequest(t, e, true, http.MethodGet, "/api/v1/admin/media?publication_state=processing", e.h.AdminListMedia)
	if w.Code != 200 {
		t.Fatalf("admin list: %s", w.Body.String())
	}
	var list struct {
		Items []struct {
			ID         int64  `json:"id"`
			State      string `json:"publication_state"`
			Generation int64  `json:"ingest_generation"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != e.mediaID || list.Items[0].State != "processing" || list.Items[0].Generation != 1 {
		t.Fatalf("admin items=%+v", list.Items)
	}
	w = e2eRequest(t, e, true, http.MethodGet, fmt.Sprintf("/api/v1/admin/media/%d/ingest", e.mediaID), e.h.AdminGetMediaIngest)
	var ingest struct {
		Run struct {
			ID int64 `json:"id"`
		} `json:"run"`
		Steps []struct {
			Type     string `json:"type"`
			Required bool   `json:"required"`
			Status   string `json:"status"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ingest); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, s := range ingest.Steps {
		requiredness := "optional"
		if s.Required {
			requiredness = "required"
		}
		got = append(got, s.Type+":"+s.Status+":"+requiredness)
	}
	sort.Strings(got)
	want := []string{"atrack_extract:waiting:optional", "encrypt:waiting:required", "media_visible:waiting:optional", "poster:waiting:required", "preview:waiting:optional", "scrape:waiting:optional", "subtitle_extract:waiting:optional"}
	if coreiface.IngestPreparePlannerHandle() != nil {
		want = append(want, "prepare:waiting:optional")
	}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) || ingest.Run.ID != e.runID {
		t.Fatalf("run=%d steps=%v", ingest.Run.ID, got)
	}
}

func TestNewVideoPublishesAfterRequiredStepsWhileOptionalRemainWaiting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := newPublicationE2E(t, true)
	completePostIngest(t, e, postingest.TaskPoster)
	var state string
	if err := e.db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, e.mediaID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "processing" {
		t.Fatalf("published before encrypt: %s", state)
	}
	completePostIngest(t, e, postingest.TaskEncrypt)

	w := e2eRequest(t, e, false, http.MethodGet, "/api/v1/media", e.h.ListMedia)
	var p struct {
		Items []struct {
			ID     int64  `json:"id"`
			Poster string `json:"poster_url"`
			State  string `json:"publication_state"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].ID != e.mediaID || p.Items[0].State != "published" || p.Items[0].Poster != storage.PlainPosterURL(e.mediaID) {
		t.Fatalf("published payload=%+v body=%s", p.Items, w.Body.String())
	}
	var optionalWaiting int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=? AND required=0 AND status='waiting'`, e.runID).Scan(&optionalWaiting); err != nil {
		t.Fatal(err)
	}
	if optionalWaiting != 6 {
		t.Fatalf("optional waiting steps=%d, want 6", optionalWaiting)
	}
	w = e2eRequest(t, e, false, http.MethodGet, fmt.Sprintf("/api/v1/media/%d/poster.jpg", e.mediaID), e.h.ServeMediaPoster)
	if w.Code != 200 || w.Body.String() != "jpeg" {
		t.Fatalf("poster status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestNewVideoBecomesFailedAndRemainsHiddenAfterRequiredExhaustion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := newPublicationE2E(t, false)
	// Local poster defaults to a single attempt; raise the bound to exercise exhaustion.
	if _, err := e.db.Exec(`UPDATE post_ingest_task SET max_attempts=3 WHERE ingest_run_id=? AND task_type='poster';
UPDATE media_ingest_step SET max_attempts=3 WHERE run_id=? AND step_type='poster'`, e.runID, e.runID); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		task, err := e.h.Queue.Claim(context.Background(), postingest.TaskPoster)
		if err != nil || task == nil {
			t.Fatalf("claim attempt %d task=%v err=%v", attempt, task, err)
		}
		if err = e.h.Queue.Fail(context.Background(), task, postingest.FailureRetryable, fmt.Errorf("poster failed %d", attempt+1)); err != nil {
			t.Fatal(err)
		}
		if attempt < 2 {
			if _, err = e.db.Exec(`UPDATE post_ingest_task SET available_at=CURRENT_TIMESTAMP WHERE id=?`, task.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var mediaState, runState string
	if err := e.db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, e.mediaID).Scan(&mediaState); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, e.runID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if mediaState != "failed" || runState != "failed" {
		t.Fatalf("media state=%s run state=%s, want failed/failed", mediaState, runState)
	}
	if w := e2eRequest(t, e, false, http.MethodGet, fmt.Sprintf("/api/v1/media/%d", e.mediaID), e.h.GetMedia); w.Code != http.StatusNotFound {
		t.Fatalf("failed media became visible: %d %s", w.Code, w.Body.String())
	}
}

func TestRestartRecoversRunningGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := newPublicationE2E(t, false)
	// Local defaults are single-attempt; allow one reclaim after lease expiry.
	if _, err := e.db.Exec(`UPDATE post_ingest_task SET max_attempts=2 WHERE ingest_run_id=? AND task_type='poster';
UPDATE media_ingest_step SET max_attempts=2 WHERE run_id=? AND step_type='poster'`, e.runID, e.runID); err != nil {
		t.Fatal(err)
	}
	task, err := e.h.Queue.Claim(context.Background(), postingest.TaskPoster)
	if err != nil || task == nil {
		t.Fatalf("claim=%v %v", task, err)
	}
	if _, err = e.db.Exec(`UPDATE post_ingest_task SET lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	restarted := postingest.NewQueue(e.db, "restarted", nil)
	if n, err := restarted.RecoverExpired(context.Background()); err != nil || n != 1 {
		t.Fatalf("recover=(%d,%v)", n, err)
	}
	e.h.Queue = restarted
	completePostIngest(t, e, postingest.TaskPoster)
	var state string
	e.db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, e.mediaID).Scan(&state)
	if state != "published" {
		t.Fatalf("restart state=%s", state)
	}
}

func TestNewVideoGenerationFencingStaleWorkerCannotPublishCurrentGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := newPublicationE2E(t, false)
	stale, err := e.h.Queue.Claim(context.Background(), postingest.TaskPoster)
	if err != nil || stale == nil {
		t.Fatalf("claim stale=%v err=%v", stale, err)
	}
	tx, err := e.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := publication.NewPlanner(publication.PlanOptions{}).RepairMediaTx(context.Background(), tx, e.mediaID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if next.Generation != 2 {
		t.Fatalf("next generation=%d", next.Generation)
	}
	var beforeRunState, beforeTaskState string
	if err = e.db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, e.runID).Scan(&beforeRunState); err != nil {
		t.Fatal(err)
	}
	if err = e.db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, stale.ID).Scan(&beforeTaskState); err != nil {
		t.Fatal(err)
	}
	completionErr := e.h.Queue.Complete(context.Background(), *stale)

	var mediaGeneration int64
	var supersededGeneration sql.NullInt64
	var state, staleRunState, terminalReason, staleTaskState string
	if err = e.db.QueryRow(`SELECT ingest_generation,publication_state FROM media WHERE id=?`, e.mediaID).Scan(&mediaGeneration, &state); err != nil {
		t.Fatal(err)
	}
	if err = e.db.QueryRow(`SELECT status,terminal_reason,superseded_by_generation FROM media_ingest_run WHERE id=?`, e.runID).Scan(&staleRunState, &terminalReason, &supersededGeneration); err != nil {
		t.Fatal(err)
	}
	if err = e.db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, stale.ID).Scan(&staleTaskState); err != nil {
		t.Fatal(err)
	}
	if completionErr == nil || staleRunState != beforeRunState || staleTaskState != beforeTaskState {
		t.Fatalf("stale completion err=%v changed run %s->%s task %s->%s", completionErr, beforeRunState, staleRunState, beforeTaskState, staleTaskState)
	}
	if mediaGeneration != 2 || state != "processing" || staleRunState != "cancelled" || terminalReason != "superseded_by_policy_v2" || !supersededGeneration.Valid || supersededGeneration.Int64 != 2 || staleTaskState != "cancelled" {
		t.Fatalf("generation=%d state=%s stale_run=%s reason=%q superseded_by=%v stale_task=%s", mediaGeneration, state, staleRunState, terminalReason, supersededGeneration, staleTaskState)
	}
	if w := e2eRequest(t, e, false, http.MethodGet, fmt.Sprintf("/api/v1/media/%d", e.mediaID), e.h.GetMedia); w.Code != http.StatusNotFound {
		t.Fatalf("stale completion exposed media: %d %s", w.Code, w.Body.String())
	}
}

func TestScannerDuplicateUnchangedDoesNotCreateGeneration(t *testing.T) {
	e := newPublicationE2E(t, false)
	var callbacks int
	sc := &scanner.Scanner{DB: e.db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		return &ffprobe.Summary{Width: 1, Height: 1}, nil
	}}
	added, err := sc.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), e.libraryID, []string{e.root}, scanner.ScanCallbacks{OnMediaDiscoveredTx: func(context.Context, *sql.Tx, scanner.ScanDiscovery) error { callbacks++; return nil }})
	if err != nil || added != 0 {
		t.Fatalf("second scan added=%d err=%v", added, err)
	}
	var generation, runs int
	if err = e.db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, e.mediaID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err = e.db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, e.mediaID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if callbacks != 0 || generation != 1 || runs != 1 {
		t.Fatalf("callbacks=%d generation=%d runs=%d", callbacks, generation, runs)
	}
}
