package metadatalib

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/scraper"
	"knox-media/internal/store"
)

func seedExactScrapeStageClaim(t *testing.T) (*sql.DB, ScrapeStageClaim) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('stage','movie','/stage')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,title,file_type,status,ingest_generation) VALUES(?,'stage-file','stage.mkv','before','video','active',1)`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,1,'scan','processing','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,lease_owner,lease_until) VALUES(?,?,1,'scrape',1,'running',2,'scrape/exact',datetime(CURRENT_TIMESTAMP,'+5 minutes'))`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,ingest_run_id,ingest_step_id,generation,retry_round,lease_owner,lease_until) VALUES(?,'running',0,?,?,1,3,'scrape/exact',datetime(CURRENT_TIMESTAMP,'+5 minutes'))`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := r.LastInsertId()
	return db, ScrapeStageClaim{TaskID: taskID, MediaID: mediaID, RunID: runID, StepID: stepID, Generation: 1, LeaseOwner: "scrape/exact", Attempt: 2, RetryRound: 3}
}

func stageResultServer(t *testing.T) *scraper.ScrapeResult {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("image")) }))
	t.Cleanup(srv.Close)
	return &scraper.ScrapeResult{Title: "staged", Poster: srv.URL + "/poster.jpg", Extra: map[string]any{}}
}

func TestStageScrapeImagesDurableRejectsStaleOwnerBeforeReservation(t *testing.T) {
	db, claim := seedExactScrapeStageClaim(t)
	stale := claim
	stale.LeaseOwner = "scrape/stale"
	_, err := StageScrapeImagesDurable(context.Background(), db, t.TempDir(), "", stale, "stale-owner", stageResultServer(t))
	if err == nil {
		t.Fatal("stale owner reserved artwork")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_asset_stage_journal WHERE stage_id='stale-owner'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("reservations=%d", n)
	}
}

func TestScrapeArtworkReconcilerDoesNotStarveStaleBehindActiveExactClaims(t *testing.T) {
	db, claim := seedExactScrapeStageClaim(t)
	root := t.TempDir()
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("active-%d", i)
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json,scrape_task_id,scrape_attempt,scrape_retry_round,updated_at) VALUES(?,?,?,?,?,?,?,'scrape_artwork','staged',?,'{}',?,?,?,datetime(CURRENT_TIMESTAMP,'-20 minutes'))`, id, claim.MediaID, claim.RunID, claim.StepID, claim.Generation, claim.LeaseOwner, id, dir, claim.TaskID, claim.Attempt, claim.RetryRound); err != nil {
			t.Fatal(err)
		}
	}
	staleDir := filepath.Join(root, "stale")
	if err := os.MkdirAll(staleDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json,scrape_task_id,scrape_attempt,scrape_retry_round,updated_at) VALUES('z-stale',?,?,?,?,'stale','stale','scrape_artwork','staged',?,'{}',?,?,?,datetime(CURRENT_TIMESTAMP,'-20 minutes'))`, claim.MediaID, claim.RunID, claim.StepID, claim.Generation, staleDir, claim.TaskID, claim.Attempt, claim.RetryRound-1); err != nil {
		t.Fatal(err)
	}
	cleaned, err := ReconcileScrapeArtworkStages(context.Background(), db, root, 1)
	if err != nil || cleaned != 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, err)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale directory retained: %v", err)
	}
}
