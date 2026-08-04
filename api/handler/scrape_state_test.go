package handler

import (
	"context"
	"database/sql"
	"encoding/json"

	"errors"
	"fmt"
	"knox-media/internal/config"
	"knox-media/internal/metadatalib"
	"knox-media/internal/postingest"
	"knox-media/internal/publication"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"knox-media/internal/app"
)

func TestCompleteScrapeTaskTxRollsBackDoneWhenHistoryFails(t *testing.T) {
	db, id := posterHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,fail_count) VALUES(90,?,'running',0); CREATE TRIGGER fail_done_history BEFORE INSERT ON scrape_history WHEN NEW.status='done' BEGIN SELECT RAISE(ABORT,'history failed'); END`, id); err != nil {
		t.Fatal(err)
	}
	err := completeScrapeTaskTx(context.Background(), db, 90, id, "tmdb", "q", "ok", "{}", "")
	if err == nil {
		t.Fatal("expected completion error")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM scrape_task WHERE id=90`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "done" {
		t.Fatal("task falsely committed done")
	}
}

func TestFailScrapeTaskReturnsJoinedErrors(t *testing.T) {
	db, id := posterHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,fail_count) VALUES(91,?,'running',0); CREATE TRIGGER fail_task_update BEFORE UPDATE ON scrape_task BEGIN SELECT RAISE(ABORT,'update failed'); END; CREATE TRIGGER fail_history BEFORE INSERT ON scrape_history BEGIN SELECT RAISE(ABORT,'history failed'); END`, id); err != nil {
		t.Fatal(err)
	}
	err := failScrapeTaskDB(context.Background(), db, 91, id, "tmdb", "q", "failed", "")
	if err == nil {
		t.Fatal("expected joined errors")
	}
	if !errors.Is(err, err) {
		t.Fatal("unreachable")
	}
}

var _ *sql.DB

func TestClaimScrapeTaskOnlyClaimsEligibleStates(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	statuses := []struct {
		id       int
		status   string
		failures int
		want     bool
	}{{101, "waiting", 0, true}, {102, "failed", 2, true}, {103, "running", 0, false}, {104, "done", 0, false}, {105, "abandoned", 0, false}, {106, "failed", 3, false}}
	for _, tc := range statuses {
		if _, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,fail_count) VALUES(?,?,?,?)`, tc.id, mediaID, tc.status, tc.failures); err != nil {
			t.Fatal(err)
		}
		got, err := claimScrapeTask(context.Background(), db, int64(tc.id))
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("id=%d got=%v want=%v", tc.id, got, tc.want)
		}
	}
}

func TestClaimScrapeTaskConcurrentOnlyOneSucceeds(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,fail_count) VALUES(110,?,'waiting',0)`, mediaID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; ok, err := claimScrapeTask(context.Background(), db, 110); results <- ok; errs <- err }()
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if <-results {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d", successes)
	}
}

func TestInsertUploadedMediaPersistsPhotoSortMetadata(t *testing.T) {
	db, _ := posterHandlerTestDB(t)
	h := &Handler{App: &app.App{DB: db}}
	meta := `{"photo":{"taken_at":"2026-07-18T09:10:11+08:00","place_id":"beach"}}`
	res, err := h.insertUploadedMedia(context.Background(), nil, "upload-photo", "photo", "/photo.jpg", "image", nil, nil, nil, nil, nil, nil, meta)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	var created, taken, place string
	if err := db.QueryRow(`SELECT created_at_sort,photo_taken_at,photo_place_id FROM media WHERE id=?`, id).Scan(&created, &taken, &place); err != nil {
		t.Fatal(err)
	}
	if created == "" || taken != "2026-07-18T01:10:11.000000Z" || place != "beach" {
		t.Fatalf("created=%q taken=%q place=%q", created, taken, place)
	}
}

func TestClaimScrapeTaskClaimsIngestStep(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=1 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	run, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,config_snapshot_json) VALUES(?,1,NULL,'manual_retry','processing','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	step, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'scrape',1,'waiting')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := step.LastInsertId()
	if _, err := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,ingest_run_id,ingest_step_id,generation) VALUES(?,'waiting',0,?,?,1)`, mediaID, runID, stepID); err != nil {
		t.Fatal(err)
	}
	taskID := int64(1)
	claimed, err := claimScrapeTask(context.Background(), db, taskID)
	if err != nil || !claimed {
		t.Fatalf("claim=(%v,%v)", claimed, err)
	}
	var status, owner string
	if err := db.QueryRow(`SELECT status,COALESCE(lease_owner,'') FROM media_ingest_step WHERE id=?`, stepID).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner == "" {
		t.Fatalf("linked step status=%q owner=%q", status, owner)
	}
}

func TestCompleteScrapeTaskAggregatesPublication(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=1 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	run, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,config_snapshot_json) VALUES(?,1,NULL,'manual_retry','processing','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	step, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'scrape',1,'running')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := step.LastInsertId()
	if _, err := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,ingest_run_id,ingest_step_id,generation) VALUES(?,'running',0,?,?,1)`, mediaID, runID, stepID); err != nil {
		t.Fatal(err)
	}
	if err := completeScrapeTaskTx(context.Background(), db, 1, mediaID, "auto", "movie", "ok", "{}", ""); err != nil {
		t.Fatal(err)
	}
	var taskStatus, stepStatus, publication string
	if err := db.QueryRow(`SELECT status FROM scrape_task WHERE id=1`).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&publication); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "done" || stepStatus != "done" || publication != "published" {
		t.Fatalf("task=%q step=%q publication=%q", taskStatus, stepStatus, publication)
	}
}

func TestScrapeExhaustionDegradesMedia(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=1 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	run, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,1,NULL,'manual_retry','processing',1,'{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	step, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,max_attempts) VALUES(?,?,1,'scrape',1,'running',3,3)`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := step.LastInsertId()
	if _, err := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,ingest_run_id,ingest_step_id,generation) VALUES(?,'running',3,?,?,1)`, mediaID, runID, stepID); err != nil {
		t.Fatal(err)
	}
	if err := failScrapeTaskDB(context.Background(), db, 1, mediaID, "auto", "movie", "no metadata", ""); err != nil {
		t.Fatal(err)
	}
	var stepStatus, publication string
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&publication); err != nil {
		t.Fatal(err)
	}
	if stepStatus != "failed" || publication != "degraded" {
		t.Fatalf("step=%q publication=%q", stepStatus, publication)
	}
}

func TestThirdLinkedScrapeAttemptExecutes(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=1,publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	runResult, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,1,'scan','published','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runResult.LastInsertId()
	stepResult, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts) VALUES(?,?,1,'scrape',0,'waiting',2)`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := stepResult.LastInsertId()
	taskResult, err := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,source,ingest_run_id,ingest_step_id,generation) VALUES(?,'waiting',2,'auto-scan',?,?,1)`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := taskResult.LastInsertId()
	calls := 0
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, PublicationCapabilities: publication.NewCapabilityMatrix([]string{"scrape"}), scrapeWithConfig: func(string, string, scraper.Config) (*scraper.ScrapeResult, error) {
		calls++
		return &scraper.ScrapeResult{Title: "third-attempt", Overview: "ok", Extra: map[string]any{}}, nil
	}}
	if done, failed := h.runScrapeTasksWithLimit(context.Background(), []int64{taskID}, 1); done != 1 || failed != 0 {
		t.Fatalf("cycle=(%d,%d), want (1,0)", done, failed)
	}
	var taskStatus, stepStatus string
	var failCount, attempts int
	if err := db.QueryRow(`SELECT status,fail_count FROM scrape_task WHERE id=?`, taskID).Scan(&taskStatus, &failCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,attempts FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus, &attempts); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || taskStatus != "done" || stepStatus != "done" || failCount != 0 || attempts != 3 {
		t.Fatalf("calls=%d task=%q step=%q fail_count=%d attempts=%d", calls, taskStatus, stepStatus, failCount, attempts)
	}
}

func TestPendingScrapeCountExcludesExhaustedWaitingTasks(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count) VALUES(?,'waiting',3)`, mediaID); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	if got := h.countPendingScrapeTasks(context.Background()); got != 0 {
		t.Fatalf("pending=%d, want 0", got)
	}
}

func TestExpiredExhaustedScrapeFailsLinkedStep(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=1,publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	runResult, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,1,'scan','published','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runResult.LastInsertId()
	stepResult, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,lease_owner,lease_until) VALUES(?,?,1,'scrape',0,'running',3,'expired',datetime(CURRENT_TIMESTAMP,'-1 second'))`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := stepResult.LastInsertId()
	taskResult, err := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,lease_owner,lease_until,ingest_run_id,ingest_step_id,generation) VALUES(?,'running',3,'expired',datetime(CURRENT_TIMESTAMP,'-1 second'),?,?,1)`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := taskResult.LastInsertId()
	h := &Handler{App: &app.App{DB: db}}
	h.recoverExpiredScrapeTasks(context.Background())
	var taskStatus, stepStatus string
	if err := db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, taskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "failed" || stepStatus != "failed" {
		t.Fatalf("expired recovery task=%q step=%q", taskStatus, stepStatus)
	}
}

func TestStartupScrapeLoopProcessesScannerPlan(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=1 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	run, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,1,'scan','processing','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	doneSteps := []string{"poster", "preview", "keyframe"}
	for _, typ := range doneSteps {
		if _, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,?,1,'done')`, runID, mediaID, typ); err != nil {
			t.Fatal(err)
		}
	}
	step, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'scrape',1,'waiting')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := step.LastInsertId()
	task, err := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,source,ingest_run_id,ingest_step_id,generation) VALUES(?,'waiting',0,'auto-scan',?,?,1)`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := task.LastInsertId()
	calls := 0
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, PublicationCapabilities: publication.NewCapabilityMatrix([]string{"scrape"}), Queue: postingest.NewQueue(db, "test", nil), scrapeWithConfig: func(string, string, scraper.Config) (*scraper.ScrapeResult, error) {
		calls++
		return &scraper.ScrapeResult{Title: "deterministic", Overview: "local fake", Genres: []string{}, Extra: map[string]any{}}, nil
	}}
	if done, failed := h.runScrapeTasksWithLimit(context.Background(), []int64{taskID}, 1); done != 1 || failed != 0 {
		var st, msg string
		_ = db.QueryRow(`SELECT status,message FROM scrape_task WHERE id=?`, taskID).Scan(&st, &msg)
		t.Fatalf("cycle=(%d,%d) status=%q msg=%q", done, failed, st, msg)
	}
	var taskStatus, stepStatus, publication string
	_ = db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, taskID).Scan(&taskStatus)
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus)
	_ = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&publication)
	if calls != 1 || taskStatus != "done" || stepStatus != "done" || publication != "published" {
		t.Fatalf("calls=%d task=%q step=%q publication=%q", calls, taskStatus, stepStatus, publication)
	}
	var history int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scrape_history WHERE task_id=? AND status='done'`, taskID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 1 {
		t.Fatalf("history=%d", history)
	}
}

func TestFailScrapeTaskRejectsOwnerChangedAfterRead(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,fail_count,lease_owner) VALUES(140,?,'running',1,'scrape/old')`, mediaID); err != nil {
		t.Fatal(err)
	}
	before := scrapeBeforeFailUpdate
	scrapeBeforeFailUpdate = func() error {
		_, err := db.Exec(`UPDATE scrape_task SET lease_owner='scrape/new' WHERE id=140`)
		return err
	}
	t.Cleanup(func() { scrapeBeforeFailUpdate = before })
	if err := failScrapeTaskDB(context.Background(), db, 140, mediaID, "auto", "movie", "boom", "scrape/old"); err == nil {
		t.Fatal("expected ownership lost")
	}
	var status, owner string
	if err := db.QueryRow(`SELECT status,lease_owner FROM scrape_task WHERE id=140`).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner != "scrape/new" {
		t.Fatalf("task=%q owner=%q", status, owner)
	}
	var history int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scrape_history WHERE task_id=140`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 0 {
		t.Fatalf("history=%d", history)
	}
}

func TestClaimScrapeTaskRejectsStaleGeneration(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []int{1, 2} {
		run, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,?, 'scan','processing','{}')`, mediaID, generation)
		if err != nil {
			t.Fatal(err)
		}
		runID, _ := run.LastInsertId()
		step, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,?,'scrape',1,'waiting')`, runID, mediaID, generation)
		if err != nil {
			t.Fatal(err)
		}
		stepID, _ := step.LastInsertId()
		if _, err := db.Exec(`INSERT INTO scrape_task(media_id,status,ingest_run_id,ingest_step_id,generation) VALUES(?,'waiting',?,?,?)`, mediaID, runID, stepID, generation); err != nil {
			t.Fatal(err)
		}
	}
	old, err := claimScrapeTaskWithOwner(context.Background(), db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if old != nil {
		t.Fatal("stale generation was claimed")
	}
	current, err := claimScrapeTaskWithOwner(context.Background(), db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.Owner == "" {
		t.Fatalf("current claim=%#v", current)
	}
}

func TestScrapeCompletionRejectsStaleOwner(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,lease_owner,lease_until) VALUES(120,?,'running','scrape/new','9999-01-01')`, mediaID); err != nil {
		t.Fatal(err)
	}
	if err := completeScrapeTaskTx(context.Background(), db, 120, mediaID, "auto", "movie", "ok", "{}", "scrape/old"); err == nil {
		t.Fatal("expected ownership error")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM scrape_task WHERE id=120`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("status=%q", status)
	}
}

func TestScrapeRetriesExactlyThreeClaims(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,fail_count) VALUES(130,?,'waiting',0)`, mediaID); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		claim, err := claimScrapeTaskWithOwner(context.Background(), db, 130)
		if err != nil {
			t.Fatal(err)
		}
		if claim == nil {
			t.Fatalf("attempt %d not claimed", attempt)
		}
		if err := failScrapeTaskDB(context.Background(), db, 130, mediaID, "auto", "movie", "retry", claim.Owner); err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			if _, err := db.Exec(`UPDATE scrape_task SET available_at=CURRENT_TIMESTAMP WHERE id=130`); err != nil {
				t.Fatal(err)
			}
		}
	}
	var status string
	var fails int
	if err := db.QueryRow(`SELECT status,fail_count FROM scrape_task WHERE id=130`).Scan(&status, &fails); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || fails != 3 {
		t.Fatalf("status=%q fails=%d", status, fails)
	}
}

func TestScrapeExactLifecycleRejectsRelinkSameOwner(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mediaID)
	_, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,2,'repair','processing','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	var run int64
	_ = db.QueryRow(`SELECT max(id) FROM media_ingest_run`).Scan(&run)
	res, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,lease_owner) VALUES(?,?,2,'scrape',1,'running',?,?)`, run, mediaID, claim.Attempts, claim.Owner)
	if err != nil {
		t.Fatal(err)
	}
	step, _ := res.LastInsertId()
	_, err = db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=?; UPDATE scrape_task SET ingest_run_id=?,ingest_step_id=?,generation=2 WHERE id=?`, mediaID, run, step, claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := completeScrapeClaim(context.Background(), db, *claim, "auto", "q", "ok", &scraper.ScrapeResult{Title: "x", Genres: []string{}, Extra: map[string]any{}}); !errors.Is(err, ErrScrapeClaimLost) {
		t.Fatalf("complete err=%v", err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, claim.ID).Scan(&status)
	if status != "running" {
		t.Fatalf("status=%s", status)
	}
}

func TestRenewScrapeClaimFencesTaskAndStep(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mediaID)
	_, _ = db.Exec(`UPDATE scrape_task SET lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?; UPDATE media_ingest_step SET lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?`, claim.ID, claim.StepID.Int64)
	before := time.Now()
	if err := renewScrapeClaim(context.Background(), db, *claim); err != nil {
		t.Fatal(err)
	}
	var taskLease, stepLease time.Time
	_ = db.QueryRow(`SELECT lease_until FROM scrape_task WHERE id=?`, claim.ID).Scan(&taskLease)
	_ = db.QueryRow(`SELECT lease_until FROM media_ingest_step WHERE id=?`, claim.StepID.Int64).Scan(&stepLease)
	if !taskLease.After(before) || !taskLease.Equal(stepLease) {
		t.Fatalf("leases=%v/%v before=%v", taskLease, stepLease, before)
	}
	stale := *claim
	stale.Owner = "scrape/stale"
	if err := renewScrapeClaim(context.Background(), db, stale); !errors.Is(err, ErrScrapeClaimLost) {
		t.Fatalf("stale err=%v", err)
	}
}

func seedAndClaimLinkedScrape(t *testing.T, db *sql.DB, mediaID int64) *scrapeClaim {
	t.Helper()
	_, err := db.Exec(`UPDATE media SET ingest_generation=1 WHERE id=?`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,1,'scan','processing','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := r.LastInsertId()
	s, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'scrape',1,'waiting')`, run, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	step, _ := s.LastInsertId()
	q, err := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,ingest_run_id,ingest_step_id,generation) VALUES(?,'waiting',0,?,?,1)`, mediaID, run, step)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := q.LastInsertId()
	c, err := claimScrapeTaskWithOwner(context.Background(), db, id)
	if err != nil || c == nil {
		t.Fatalf("claim=%+v err=%v", c, err)
	}
	return c
}

func TestScrapeLeaseLossPreventsLateCommit(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	_, err := db.Exec(`UPDATE media SET ingest_generation=1 WHERE id=?`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,1,'scan','processing','{}')`, mediaID)
	run, _ := r.LastInsertId()
	s, _ := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'scrape',1,'waiting')`, run, mediaID)
	step, _ := s.LastInsertId()
	q, _ := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,source,ingest_run_id,ingest_step_id,generation) VALUES(?,'waiting',0,'auto',?,?,1)`, mediaID, run, step)
	id, _ := q.LastInsertId()
	entered := make(chan struct{})
	release := make(chan struct{})
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: postingest.NewQueue(db, "side-effect", nil), PublicationCapabilities: publication.NewCapabilityMatrix([]string{"scrape"}), scrapeWithConfig: func(string, string, scraper.Config) (*scraper.ScrapeResult, error) {
		close(entered)
		<-release
		return &scraper.ScrapeResult{Title: "late", Genres: []string{}, Extra: map[string]any{}}, nil
	}}
	done := make(chan [2]int, 1)
	go func() { a, b := h.runScrapeTasksWithLimit(context.Background(), []int64{id}, 1); done <- [2]int{a, b} }()
	<-entered
	_, err = db.Exec(`UPDATE scrape_task SET lease_owner='scrape/reused' WHERE id=?; UPDATE media_ingest_step SET lease_owner='scrape/reused' WHERE id=?`, id, step)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-done
	if result[0] != 0 {
		t.Fatalf("result=%v", result)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, id).Scan(&status)
	if status != "running" {
		t.Fatalf("status=%s", status)
	}
}

func TestLegacyUnlinkedScrapeLifecycleExplicitPath(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	_, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,fail_count) VALUES(200,?,'running',1)`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if err := completeScrapeTaskTx(context.Background(), db, 200, mediaID, "auto", "q", "ok", "{}", ""); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM scrape_task WHERE id=200`).Scan(&status)
	if status != "done" {
		t.Fatalf("status=%s", status)
	}
}

func TestScrapeLeaseLossLeavesAllMetadataSideEffectsUnchanged(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	_, _ = db.Exec(`UPDATE media SET ingest_generation=1,title='before',meta_json='{"keep":true}' WHERE id=?`, mediaID)
	r, _ := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,1,'scan','processing','{}')`, mediaID)
	run, _ := r.LastInsertId()
	s, _ := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'scrape',1,'waiting')`, run, mediaID)
	step, _ := s.LastInsertId()
	q, _ := db.Exec(`INSERT INTO scrape_task(media_id,status,source,ingest_run_id,ingest_step_id,generation) VALUES(?,'waiting','auto',?,?,1)`, mediaID, run, step)
	id, _ := q.LastInsertId()
	entered := make(chan struct{})
	release := make(chan struct{})
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: postingest.NewQueue(db, "metadata-fence", nil), PublicationCapabilities: publication.NewCapabilityMatrix([]string{"scrape"}), scrapeWithConfig: func(string, string, scraper.Config) (*scraper.ScrapeResult, error) {
		close(entered)
		<-release
		return &scraper.ScrapeResult{Title: "after", Overview: "changed", Genres: []string{"x"}, Extra: map[string]any{}}, nil
	}}
	done := make(chan struct{})
	go func() { h.runScrapeTasksWithLimit(context.Background(), []int64{id}, 1); close(done) }()
	<-entered
	_, _ = db.Exec(`UPDATE scrape_task SET lease_owner='lost' WHERE id=?; UPDATE media_ingest_step SET lease_owner='lost' WHERE id=?`, id, step)
	close(release)
	<-done
	var title, meta string
	_ = db.QueryRow(`SELECT title,meta_json FROM media WHERE id=?`, mediaID).Scan(&title, &meta)
	if title != "before" || meta != `{"keep":true}` {
		t.Fatalf("title=%q meta=%s", title, meta)
	}
}

func TestProductionScrapeStageRetryRoundChangeHasZeroEffectsAndRecovers(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	_, _ = db.Exec(`UPDATE media SET ingest_generation=1,title='before',meta_json='{"keep":true}' WHERE id=?`, mediaID)
	r, _ := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,1,'scan','processing','{}')`, mediaID)
	runID, _ := r.LastInsertId()
	s, _ := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'scrape',1,'waiting')`, runID, mediaID)
	stepID, _ := s.LastInsertId()
	q, _ := db.Exec(`INSERT INTO scrape_task(media_id,status,source,ingest_run_id,ingest_step_id,generation) VALUES(?,'waiting','auto',?,?,1)`, mediaID, runID, stepID)
	taskID, _ := q.LastInsertId()
	root := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("production-image")) }))
	defer srv.Close()
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{MetadataLibrary: root, Dir: root, Upload: filepath.Join(root, "uploads")}}}, PublicationCapabilities: publication.NewCapabilityMatrix([]string{"scrape"}), scrapeWithConfig: func(string, string, scraper.Config) (*scraper.ScrapeResult, error) {
		return &scraper.ScrapeResult{Title: "after", Overview: "changed", Poster: srv.URL + "/poster.jpg", Extra: map[string]any{}}, nil
	}}
	barrierCalled := false
	h.scrapeAfterStage = func(c scrapeClaim, _ metadatalib.StagedScrapeArtwork) {
		barrierCalled = true
		_, _ = db.Exec(`UPDATE scrape_task SET retry_round=retry_round+1,lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?; UPDATE media_ingest_step SET lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?`, c.ID, c.StepID.Int64)
	}
	done, _ := h.runScrapeTasksWithLimit(context.Background(), []int64{taskID}, 1)
	if !barrierCalled {
		t.Fatal("stage barrier not called")
	}
	if done != 0 {
		t.Fatalf("done=%d", done)
	}
	var title, meta, taskStatus, stepStatus string
	_ = db.QueryRow(`SELECT title,meta_json FROM media WHERE id=?`, mediaID).Scan(&title, &meta)
	_ = db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, taskID).Scan(&taskStatus)
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus)
	if title != "before" || meta != `{"keep":true}` || taskStatus != "running" || stepStatus != "running" {
		t.Fatalf("media=%q/%s status=%s/%s", title, meta, taskStatus, stepStatus)
	}
	for name, q := range map[string]string{"evidence": `SELECT COUNT(*) FROM media_ingest_evidence WHERE media_id=?`, "history": `SELECT COUNT(*) FROM scrape_history WHERE task_id=?`, "manifest": `SELECT COUNT(*) FROM scrape_effect_commit WHERE task_id=?`} {
		var n int
		_ = db.QueryRow(q, taskID).Scan(&n)
		if name == "evidence" {
			_ = db.QueryRow(q, mediaID).Scan(&n)
		}
		if n != 0 {
			t.Fatalf("%s=%d", name, n)
		}
	}
	var stageID string
	if err := db.QueryRow(`SELECT stage_id FROM media_asset_stage_journal WHERE scrape_task_id=?`, taskID).Scan(&stageID); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE media_asset_stage_journal SET updated_at=datetime(CURRENT_TIMESTAMP,'-11 minutes') WHERE stage_id=?`, stageID)
	cleaned, err := metadatalib.ReconcileScrapeArtworkStages(context.Background(), db, root, 10)
	if err != nil || cleaned != 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, err)
	}
}

func TestCurrentExactScrapeClaimStagesAndCompletes(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mid)
	stage, res, _ := stageAcceptanceArtwork(t, db, claim, mid)
	if err := completeScrapeClaimWithEffects(context.Background(), db, *claim, "auto", "q", "ok", res, scrapeCompletionEffects{Artwork: stage}); err != nil {
		t.Fatal(err)
	}
	var task, state, evidence string
	_ = db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, claim.ID).Scan(&task)
	_ = db.QueryRow(`SELECT state FROM media_asset_stage_journal WHERE stage_id=?`, stage.StageID).Scan(&state)
	_ = db.QueryRow(`SELECT kind FROM media_ingest_evidence WHERE stage_id=?`, stage.StageID).Scan(&evidence)
	if task != "done" || state != "committed" || evidence != "scrape_artwork" {
		t.Fatalf("task=%s stage=%s evidence=%s", task, state, evidence)
	}
}
func TestAutomaticScrapeSuccessRestoresFencedEffects(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mediaID)
	_, _ = db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, mediaID)
	res := &scraper.ScrapeResult{Title: "accepted", Overview: "overview", Genres: []string{"drama"}, Extra: map[string]any{"series_title": "Accepted Show"}}
	effects := scrapeCompletionEffects{PosterFallback: true}
	if err := completeScrapeClaimWithEffects(context.Background(), db, *claim, "auto", "q", "ok", res, effects); err != nil {
		t.Fatal(err)
	}
	var fallback, history int
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND generation=1 AND task_type='poster_repair'`, mediaID).Scan(&fallback)
	_ = db.QueryRow(`SELECT COUNT(*) FROM scrape_history WHERE task_id=? AND status='done'`, claim.ID).Scan(&history)
	if fallback != 1 || history != 1 {
		t.Fatalf("fallback=%d history=%d", fallback, history)
	}
	if err := completeScrapeClaimWithEffects(context.Background(), db, *claim, "auto", "q", "ok", res, effects); !errors.Is(err, ErrScrapeClaimLost) {
		t.Fatalf("second=%v", err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND generation=1 AND task_type='poster_repair'`, mediaID).Scan(&fallback)
	if fallback != 1 {
		t.Fatalf("duplicate fallback=%d", fallback)
	}
}

func TestAutomaticScrapeStaleAndRollbackSelectNoEffects(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mediaID)
	_, _ = db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?; UPDATE scrape_task SET lease_owner='lost' WHERE id=?`, mediaID, claim.ID)
	effects := scrapeCompletionEffects{PosterFallback: true, BeforeTerminal: func() error { return errors.New("rollback") }}
	err := completeScrapeClaimWithEffects(context.Background(), db, *claim, "auto", "q", "ok", &scraper.ScrapeResult{Title: "stale", Extra: map[string]any{}}, effects)
	if !errors.Is(err, ErrScrapeClaimLost) {
		t.Fatalf("err=%v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND generation=1`, mediaID).Scan(&n)
	if n != 0 {
		t.Fatalf("stale effects=%d", n)
	}
}

func TestScrapeEffectsValidClaimRollbackIsExhaustive(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mid)
	LibraryID, _ := seedScrapeAcceptanceSeries(t, db, mid)
	_, _ = db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP,title='before',meta_json='{"keep":true}' WHERE id=?`, mid)
	stage, scrapeResult, root := stageAcceptanceArtwork(t, db, claim, mid)
	sentinel := errors.New("effect rollback")
	effects := scrapeCompletionEffects{PosterFallback: true, Artwork: stage, LibraryID: LibraryID, Credits: []scraper.CreditMember{{TMDBPersonID: "777", Name: "Credit Person", Occupation: "actor"}}, BeforeTerminal: func() error { return sentinel }}
	err := completeScrapeClaimWithEffects(context.Background(), db, *claim, "auto", "q", "ok", scrapeResult, effects)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	var title, meta, task, step string
	_ = db.QueryRow(`SELECT title,meta_json FROM media WHERE id=?`, mid).Scan(&title, &meta)
	_ = db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, claim.ID).Scan(&task)
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, claim.StepID.Int64).Scan(&step)
	if title != "before" || meta != "{\"keep\":true}" || task != "running" || step != "running" {
		t.Fatalf("media=%s/%s states=%s/%s", title, meta, task, step)
	}
	var journal string
	_ = db.QueryRow(`SELECT state FROM media_asset_stage_journal WHERE stage_id=?`, stage.StageID).Scan(&journal)
	if journal != "staged" {
		t.Fatalf("journal=%s", journal)
	}
	for _, v := range stage.Images {
		if _, e := os.Stat(v.Path); e != nil {
			t.Fatal(e)
		}
	}
	for name, q := range map[string]string{"evidence": `SELECT COUNT(*) FROM media_ingest_evidence WHERE stage_id='acceptance-stage'`, "person": `SELECT COUNT(*) FROM cast_person WHERE tmdb_id='777'`, "link": `SELECT COUNT(*) FROM media_person WHERE media_id=` + fmt.Sprint(mid), "repair": `SELECT COUNT(*) FROM post_ingest_task WHERE task_type='poster_repair'`, "history": `SELECT COUNT(*) FROM scrape_history WHERE task_id=` + fmt.Sprint(claim.ID), "manifest": `SELECT COUNT(*) FROM scrape_effect_commit WHERE task_id=` + fmt.Sprint(claim.ID)} {
		var n int
		_ = db.QueryRow(q).Scan(&n)
		if n != 0 {
			t.Fatalf("%s=%d", name, n)
		}
	}
	_, _ = db.Exec(`UPDATE scrape_task SET lease_owner='stale' WHERE id=?`, claim.ID)
	_, _ = db.Exec(`UPDATE media_asset_stage_journal SET updated_at=datetime(CURRENT_TIMESTAMP,'-11 minutes') WHERE stage_id=?`, stage.StageID)
	cleaned, err := metadatalib.ReconcileScrapeArtworkStages(context.Background(), db, root, 10)
	if err != nil || cleaned != 1 {
		t.Fatalf("clean=%d err=%v", cleaned, err)
	}
	for _, v := range stage.Images {
		if _, e := os.Stat(v.Path); !os.IsNotExist(e) {
			t.Fatalf("retained %s", v.Path)
		}
	}

}

func TestScrapeEffectsUncertainActualCommitReconcilesManifestExactlyOnce(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mid)
	LibraryID, siblingID := seedScrapeAcceptanceSeries(t, db, mid)
	_, _ = db.Exec(`UPDATE library SET type='tv' WHERE id=?; UPDATE media SET file_path='Before Show S01E01.mkv',publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, LibraryID, mid)
	orig := withImmediateScrapeTx
	t.Cleanup(func() { withImmediateScrapeTx = orig })
	once := true
	withImmediateScrapeTx = func(ctx context.Context, db *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		out, err := orig(ctx, db, fn)
		if err == nil && once {
			once = false
			return out, &store.ImmediateCommitError{Cause: errors.New("lost response")}
		}
		return out, err
	}
	stage, res, _ := stageAcceptanceArtwork(t, db, claim, mid)
	effects := scrapeCompletionEffects{PosterFallback: true, Artwork: stage, LibraryID: LibraryID, Credits: []scraper.CreditMember{{TMDBPersonID: "778", Name: "Exact Person", Occupation: "actor"}}}
	if err := completeScrapeClaimWithEffects(context.Background(), db, *claim, "auto", "q", "ok", res, effects); err != nil {
		t.Fatal(err)
	}
	var seriesTitle, siblingMeta string
	_ = db.QueryRow(`SELECT title FROM series WHERE library_id=?`, LibraryID).Scan(&seriesTitle)
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, siblingID).Scan(&siblingMeta)
	if seriesTitle != "Before Show" {
		t.Fatalf("series=%q", seriesTitle)
	}
	var sibling map[string]any
	if err := json.Unmarshal([]byte(siblingMeta), &sibling); err != nil {
		t.Fatal(err)
	}
	scrapeMeta, ok := sibling["scrape"].(map[string]any)
	if !ok || scrapeMeta["series_title"] != "Before Show" || !strings.Contains(fmt.Sprint(scrapeMeta["series_poster"]), stage.StageID) || !strings.Contains(fmt.Sprint(scrapeMeta["series_backdrop"]), stage.StageID) {
		t.Fatalf("sibling=%s", siblingMeta)
	}
	for name, q := range map[string]string{"person": `SELECT COUNT(*) FROM cast_person WHERE tmdb_id='778'`, "link": `SELECT COUNT(*) FROM media_person WHERE media_id=` + fmt.Sprint(mid), "repair": `SELECT COUNT(*) FROM post_ingest_task WHERE task_type='poster_repair'`, "history": `SELECT COUNT(*) FROM scrape_history WHERE task_id=` + fmt.Sprint(claim.ID), "manifest": `SELECT COUNT(*) FROM scrape_effect_commit WHERE task_id=` + fmt.Sprint(claim.ID)} {
		var n int
		_ = db.QueryRow(q).Scan(&n)
		if n != 1 {
			t.Fatalf("%s=%d", name, n)
		}
	}
}

func TestScrapeEffectsUncertainRejectsCorruptManifest(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mid)
	_, _ = db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, mid)
	orig := withImmediateScrapeTx
	t.Cleanup(func() { withImmediateScrapeTx = orig })
	withImmediateScrapeTx = func(ctx context.Context, db *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		out, err := orig(ctx, db, fn)
		if err == nil {
			_, _ = db.Exec(`UPDATE scrape_effect_commit SET manifest_json='{"corrupt":true}',manifest_digest='bad' WHERE task_id=?`, claim.ID)
			return out, &store.ImmediateCommitError{Cause: errors.New("lost")}
		}
		return out, err
	}
	err := completeScrapeClaimWithEffects(context.Background(), db, *claim, "auto", "q", "ok", &scraper.ScrapeResult{Title: "exact", Extra: map[string]any{}}, scrapeCompletionEffects{PosterFallback: true})
	var uncertain *store.ImmediateCommitError
	if !errors.As(err, &uncertain) {
		t.Fatalf("err=%v", err)
	}
}

func stageAcceptanceArtwork(t *testing.T, db *sql.DB, c *scrapeClaim, mid int64) (metadatalib.StagedScrapeArtwork, *scraper.ScrapeResult, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("image:" + r.URL.Path)) }))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	res := &scraper.ScrapeResult{Title: "accepted", Poster: srv.URL + "/poster.jpg", Backdrop: srv.URL + "/backdrop.jpg", Logo: srv.URL + "/logo.png", Extra: map[string]any{"series_title": "Accepted Show"}}
	stage, err := metadatalib.StageScrapeImagesDurable(context.Background(), db, root, "", metadatalib.ScrapeStageClaim{TaskID: c.ID, MediaID: mid, RunID: c.RunID.Int64, StepID: c.StepID.Int64, Generation: c.Generation.Int64, LeaseOwner: c.Owner, Attempt: c.Attempts, RetryRound: c.RetryRound}, "acceptance-stage", res)
	if err != nil {
		t.Fatal(err)
	}
	if len(stage.Images) != 3 {
		t.Fatalf("images=%d", len(stage.Images))
	}
	return stage, res, root
}

func seedScrapeAcceptanceSeries(t *testing.T, db *sql.DB, mid int64) (int64, int64) {
	t.Helper()
	var lid int64
	if err := db.QueryRow(`SELECT library_id FROM media WHERE id=?`, mid).Scan(&lid); err != nil {
		t.Fatal(err)
	}
	r, err := db.Exec(`INSERT INTO series(library_id,title,title_norm,meta_json) VALUES(?,'Before Show','before show','{"before":true}')`, lid)
	if err != nil {
		t.Fatal(err)
	}
	seriesID, _ := r.LastInsertId()
	r, _ = db.Exec(`INSERT INTO season(tv_id,season_num) VALUES(?,1)`, seriesID)
	seasonID, _ := r.LastInsertId()
	r, _ = db.Exec(`INSERT INTO episode(season_id,episode_num) VALUES(?,1)`, seasonID)
	episodeID, _ := r.LastInsertId()
	_, _ = db.Exec(`INSERT INTO episode_media(episode_id,media_id) VALUES(?,?)`, episodeID, mid)
	r, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,title,file_type,status,meta_json,publication_state,published_at) VALUES(?,'accept-sibling','sibling.mp4','Sibling','video','active','{"sibling":true}','published',CURRENT_TIMESTAMP)`, lid)
	if err != nil {
		t.Fatal(err)
	}
	siblingID, _ := r.LastInsertId()
	r, _ = db.Exec(`INSERT INTO episode(season_id,episode_num) VALUES(?,2)`, seasonID)
	episodeID, _ = r.LastInsertId()
	_, _ = db.Exec(`INSERT INTO episode_media(episode_id,media_id) VALUES(?,?)`, episodeID, siblingID)
	return lid, siblingID
}

func TestScrapeSeriesEffectErrorsRollbackCompletion(t *testing.T) {
	for _, tc := range []struct{ name, trigger string }{
		{"series_update", `CREATE TRIGGER fail_series_effect BEFORE UPDATE ON series BEGIN SELECT RAISE(ABORT,'series update fault'); END`},
		{"sibling_update", `CREATE TRIGGER fail_sibling_effect BEFORE UPDATE OF meta_json ON media WHEN NEW.file_id='accept-sibling' BEGIN SELECT RAISE(ABORT,'sibling update fault'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mid := posterHandlerTestDB(t)
			claim := seedAndClaimLinkedScrape(t, db, mid)
			libraryID, siblingID := seedScrapeAcceptanceSeries(t, db, mid)
			_, _ = db.Exec(`UPDATE library SET type='tv' WHERE id=?; UPDATE media SET file_path='Before Show S01E01.mkv' WHERE id=?`, libraryID, mid)
			_, _ = db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP,title='before',meta_json='{"keep":true}' WHERE id=?`, mid)
			if _, err := db.Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			err := completeScrapeClaimWithEffects(context.Background(), db, *claim, "auto", "q", "ok", &scraper.ScrapeResult{Title: "After", Extra: map[string]any{"series_title": "After Show"}}, scrapeCompletionEffects{LibraryID: libraryID, PosterFallback: true})
			if err == nil {
				t.Fatal("expected injected series effect error")
			}
			var task, step, seriesTitle, siblingMeta, mediaTitle string
			_ = db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, claim.ID).Scan(&task)
			_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, claim.StepID.Int64).Scan(&step)
			_ = db.QueryRow(`SELECT title FROM series WHERE library_id=?`, libraryID).Scan(&seriesTitle)
			_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, siblingID).Scan(&siblingMeta)
			_ = db.QueryRow(`SELECT title FROM media WHERE id=?`, mid).Scan(&mediaTitle)
			if task != "running" || step != "running" || seriesTitle != "Before Show" || siblingMeta != `{"sibling":true}` || mediaTitle != "before" {
				t.Fatalf("task=%s step=%s series=%q sibling=%s media=%q", task, step, seriesTitle, siblingMeta, mediaTitle)
			}
			for name, q := range map[string]string{"manifest": `SELECT COUNT(*) FROM scrape_effect_commit WHERE task_id=?`, "history": `SELECT COUNT(*) FROM scrape_history WHERE task_id=?`, "repair": `SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND task_type='poster_repair'`} {
				var n int
				_ = db.QueryRow(q, claim.ID).Scan(&n)
				if n != 0 {
					t.Fatalf("%s=%d", name, n)
				}
			}
		})
	}
}

func TestScrapeEffectManifestIsBoundedAndOmitsProviderText(t *testing.T) {
	huge := "sensitive-provider-text-" + strings.Repeat("x", 200000)
	c := scrapeClaim{ID: 1, Attempts: 2, Generation: sql.NullInt64{Int64: 3, Valid: true}, MediaID: 4}
	credits := make([]scraper.CreditMember, 1000)
	for i := range credits {
		credits[i] = scraper.CreditMember{TMDBPersonID: fmt.Sprint(i), Name: huge + fmt.Sprint(i), Occupation: "actor"}
	}
	raw, _, err := canonicalScrapeEffectManifest(c, &scraper.ScrapeResult{Title: huge, Overview: huge, Extra: map[string]any{"secret": huge}}, scrapeCompletionEffects{Credits: credits, PosterFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 64<<10 || strings.Contains(string(raw), "sensitive-provider-text") {
		t.Fatalf("manifest bytes=%d leaked=%v", len(raw), strings.Contains(string(raw), "sensitive-provider-text"))
	}
}

func TestPrepareSeriesEffectsEmptyScrapePreservesEstablishedFields(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	lid, _ := seedScrapeAcceptanceSeries(t, db, mid)
	_, _ = db.Exec(`UPDATE series SET title='Keep Title',poster='keep.jpg',meta_json='{"scrape":{"overview":"Keep Overview","backdrop":"keep-bg.jpg"}}' WHERE library_id=?`, lid)
	prepared, err := prepareSeriesEffects(context.Background(), db, lid, mid, &scraper.ScrapeResult{Extra: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.DesiredTitle != "Keep Title" || prepared.DesiredPoster != "keep.jpg" || !strings.Contains(prepared.DesiredMeta, "Keep Overview") || !strings.Contains(prepared.DesiredMeta, "keep-bg.jpg") {
		t.Fatalf("prepared=%+v", prepared)
	}
}

func TestSiblingScrapePreservesCommittedPosterPointer(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mid)
	lid, siblingID := seedScrapeAcceptanceSeries(t, db, mid)
	// Simulate a poster task that already committed a local poster pointer on the sibling episode.
	_, _ = db.Exec(`UPDATE media SET meta_json='{"scrape":{"poster":"/uploads/posters/sha.jpg","title":"Show S01E02","backdrop":"/uploads/b.jpg","extra":{"poster":"/uploads/posters/sha.jpg","episode":2,"season":1,"episode_still":"https://x/still.jpg"}}}' WHERE id=?`, siblingID)
	_, _ = db.Exec(`UPDATE library SET type='tv' WHERE id=?; UPDATE media SET file_path='Show S01E01.mkv',publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, lid, mid)
	res := &scraper.ScrapeResult{Title: "After Show", Overview: "Overview", Poster: "https://image.tmdb.org/s.jpg", Extra: map[string]any{"series_title": "After Show", "series_poster": "https://image.tmdb.org/s.jpg"}}
	if err := completeScrapeClaimWithEffects(context.Background(), db, *claim, "auto", "q", "ok", res, scrapeCompletionEffects{LibraryID: lid}); err != nil {
		t.Fatal(err)
	}
	var siblingMeta string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, siblingID).Scan(&siblingMeta)
	var sibling map[string]any
	if err := json.Unmarshal([]byte(siblingMeta), &sibling); err != nil {
		t.Fatal(err)
	}
	scrapeMeta, _ := sibling["scrape"].(map[string]any)
	if scrapeMeta == nil {
		t.Fatalf("sibling scrape missing: %s", siblingMeta)
	}
	if scrapeMeta["poster"] != "/uploads/posters/sha.jpg" {
		t.Fatalf("sibling poster was dropped: %v (meta=%s)", scrapeMeta["poster"], siblingMeta)
	}
	if scrapeMeta["title"] != "Show S01E02" {
		t.Fatalf("sibling episode title was dropped: %v (meta=%s)", scrapeMeta["title"], siblingMeta)
	}
	extra, _ := scrapeMeta["extra"].(map[string]any)
	if extra == nil || extra["poster"] != "/uploads/posters/sha.jpg" {
		t.Fatalf("sibling extra poster was dropped: %v (meta=%s)", extra, siblingMeta)
	}
	if extra == nil || extra["episode"] != float64(2) {
		t.Fatalf("sibling episode field was dropped: %v (meta=%s)", extra, siblingMeta)
	}
	if scrapeMeta["series_title"] != "Before Show" {
		t.Fatalf("series_title not propagated: %v (meta=%s)", scrapeMeta["series_title"], siblingMeta)
	}
	if scrapeMeta["series_poster"] != "https://image.tmdb.org/s.jpg" {
		t.Fatalf("series_poster not propagated: %v (meta=%s)", scrapeMeta["series_poster"], siblingMeta)
	}
}

func TestCompleteScrapePreparationHonorsCallerDeadline(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	claim := seedAndClaimLinkedScrape(t, db, mid)
	lid, _ := seedScrapeAcceptanceSeries(t, db, mid)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := completeScrapeClaimWithEffects(ctx, db, *claim, "auto", "q", "ok", &scraper.ScrapeResult{Title: "x", Extra: map[string]any{}}, scrapeCompletionEffects{LibraryID: lid})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, claim.ID).Scan(&status)
	if status != "running" {
		t.Fatalf("status=%s", status)
	}
}

func TestScrapeClaimsUseUniqueOwnerTokens(t *testing.T) {
	db, mediaID := posterHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,fail_count) VALUES(150,?,'waiting',0),(151,?,'waiting',0)`, mediaID, mediaID); err != nil {
		t.Fatal(err)
	}
	first, err := claimScrapeTaskWithOwner(context.Background(), db, 150)
	if err != nil {
		t.Fatal(err)
	}
	second, err := claimScrapeTaskWithOwner(context.Background(), db, 151)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || second == nil || first.Owner == second.Owner || first.Owner == "scrape" || second.Owner == "scrape" {
		t.Fatalf("owners=%q/%q", first.Owner, second.Owner)
	}
}
