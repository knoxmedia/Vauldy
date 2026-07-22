package handler

import (
	"context"
	"database/sql"

	"errors"
	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/scraper"
	"testing"

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
	run, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,config_snapshot_json) VALUES(?,1,NULL,'manual_retry','processing','{}')`, mediaID)
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
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: postingest.NewQueue(db, "test", nil), scrapeWithConfig: func(string, string, scraper.Config) (*scraper.ScrapeResult, error) {
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
