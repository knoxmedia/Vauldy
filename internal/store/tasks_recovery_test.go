package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"knox-media/internal/postingest"
	_ "knox-media/internal/publication"
	"knox-media/internal/store"
)

func TestRestartRecoveryDoesNotLeaveExhaustedScrapeWaiting(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scrape-restart.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	libraryResult, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('scrape','video','/scrape')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := libraryResult.LastInsertId()
	mediaResult, err := db.Exec(`INSERT INTO media(library_id,file_id,file_type,ingest_generation,publication_state) VALUES(?,'scrape-media','video',1,'published')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaResult.LastInsertId()
	runResult, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(?,1,'scan','published','{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runResult.LastInsertId()
	stepResult, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts) VALUES(?,?,1,'scrape',0,'waiting',3)`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := stepResult.LastInsertId()
	taskResult, err := db.Exec(`INSERT INTO scrape_task(media_id,status,fail_count,lease_owner,ingest_run_id,ingest_step_id,generation) VALUES(?,'running',3,'dead',?,?,1)`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := taskResult.LastInsertId()

	store.ResetInterruptedTasks(db)

	var taskStatus, stepStatus string
	if err := db.QueryRow(`SELECT status FROM scrape_task WHERE id=?`, taskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "failed" || stepStatus != "failed" {
		t.Fatalf("exhausted recovery left task=%q step=%q", taskStatus, stepStatus)
	}
}

func TestRestartRecoveryResetInterruptedTasksPreservesResourceControlledScans(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "restart-reset.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	libraryResult, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('restart','video','/restart')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := libraryResult.LastInsertId()

	insertScan := func(status string, cancelled int) int64 {
		t.Helper()
		result, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,cancelled,started_at) VALUES(?,?,?,?,CURRENT_TIMESTAMP)`, libraryID, status, "manual", cancelled)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	activeScan := insertScan("running", 0)
	orphanScan := insertScan("running", 0)
	cancelledScan := insertScan("cancelled", 1)
	if _, err := db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 hour'))`, libraryID, activeScan, "other-process/active"); err != nil {
		t.Fatal(err)
	}

	mediaResult, err := db.Exec(`INSERT INTO media(library_id,file_id,file_type) VALUES(?,'restart-reset-media','video')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaResult.LastInsertId()
	postResult, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts,max_attempts,lease_owner,lease_until) VALUES(?,?,?,'running',1,3,'dead-worker/generation',datetime(CURRENT_TIMESTAMP,'-1 second'))`, mediaID, orphanScan, postingest.TaskPoster)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := postResult.LastInsertId()

	store.ResetInterruptedTasks(db)

	for id, want := range map[int64]struct {
		status    string
		cancelled int
	}{
		activeScan:    {"running", 0},
		orphanScan:    {"running", 0},
		cancelledScan: {"cancelled", 1},
	} {
		var status string
		var cancelled int
		if err := db.QueryRow(`SELECT status,cancelled FROM scan_task WHERE id=?`, id).Scan(&status, &cancelled); err != nil {
			t.Fatal(err)
		}
		if status != want.status || cancelled != want.cancelled {
			t.Fatalf("scan %d status=%s cancelled=%d want %s,%d", id, status, cancelled, want.status, want.cancelled)
		}
	}

	q := postingest.NewQueue(db, "new-worker", nil)
	if recovered, err := q.RecoverExpired(context.Background()); err != nil || recovered != 1 {
		t.Fatalf("RecoverExpired=(%d,%v) want (1,nil)", recovered, err)
	}
	var postStatus postingest.Status
	var owner sql.NullString
	if err := db.QueryRow(`SELECT status,lease_owner FROM post_ingest_task WHERE id=?`, postID).Scan(&postStatus, &owner); err != nil {
		t.Fatal(err)
	}
	if postStatus != postingest.StatusWaiting || owner.Valid {
		t.Fatalf("post status=%s owner=%v want waiting,nil", postStatus, owner)
	}
}

func TestRestartRecoveryResetsLinkedPrepareStepWithTask(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "prepare-restart.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('prepare','video','/prepare')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_type,ingest_generation,publication_state) VALUES(?,'restart-prepare','video',1,'processing')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,1,'scan','processing',0,'{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,lease_owner) VALUES(?,?,1,'prepare',1,'running',1,'dead')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO transcode_task(file_id,status,task_type,media_id,ingest_run_id,ingest_step_id,generation) VALUES('restart-prepare','running','pretranscode',?,?,?,1)`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,lease_owner,lease_until) VALUES(?,1,'360p','running','dead-owner',datetime(CURRENT_TIMESTAMP,'+1 hour'))`, taskID); err != nil {
		t.Fatal(err)
	}
	store.ResetInterruptedTasks(db)
	var task, job, step string
	var owner, jobOwner, jobLease sql.NullString
	_ = db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, taskID).Scan(&task)
	_ = db.QueryRow(`SELECT status,lease_owner,lease_until FROM pretranscode_rendition_job WHERE task_id=?`, taskID).Scan(&job, &jobOwner, &jobLease)
	_ = db.QueryRow(`SELECT status,lease_owner FROM media_ingest_step WHERE id=?`, stepID).Scan(&step, &owner)
	if task != "waiting" || job != "waiting" || step != "waiting" || owner.Valid || jobOwner.Valid || jobLease.Valid {
		t.Fatalf("recovered=%s/%s/%s owner=%v", task, job, step, owner)
	}
}

func TestResetInterruptedPrepareJobMakesAvailabilityCurrent(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,available_at,lease_owner,lease_until) VALUES(999,1,'720p','running','2040-01-01','dead','2040-01-01')`); err != nil {
		t.Fatal(err)
	}
	store.ResetInterruptedTasks(db)
	var status string
	var due int
	if err = db.QueryRow(`SELECT status,COALESCE(available_at,CURRENT_TIMESTAMP)<=CURRENT_TIMESTAMP FROM pretranscode_rendition_job WHERE task_id=999`).Scan(&status, &due); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" || due != 1 {
		t.Fatalf("status=%s due=%d", status, due)
	}
}
