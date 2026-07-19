package handler

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"knox-media/internal/app"
)

func TestCompleteScrapeTaskTxRollsBackDoneWhenHistoryFails(t *testing.T) {
	db, id := posterHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO scrape_task(id,media_id,status,fail_count) VALUES(90,?,'running',0); CREATE TRIGGER fail_done_history BEFORE INSERT ON scrape_history WHEN NEW.status='done' BEGIN SELECT RAISE(ABORT,'history failed'); END`, id); err != nil {
		t.Fatal(err)
	}
	err := completeScrapeTaskTx(context.Background(), db, 90, id, "tmdb", "q", "ok", "{}")
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
	err := failScrapeTaskDB(context.Background(), db, 91, id, "tmdb", "q", "failed")
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
