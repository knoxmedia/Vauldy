package pretranscode

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"knox-media/internal/publication"
)

func TestLinkedPrepareRetryRoundFencesStaleWorker(t *testing.T) {
	db := newTestDB(t)
	taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
	res, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,lease_owner,lease_until,retry_round) VALUES(?,1,'360p','running',25,'job-owner',datetime(CURRENT_TIMESTAMP,'+90 seconds'),0)`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := res.LastInsertId()
	if _, err = db.Exec(`UPDATE transcode_task SET retry_round=0 WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	stale := exactClaimedJob(t, db, jobID, taskID, "job-owner")
	if stale.Parent.RetryRound != 0 || stale.RetryRound != 0 {
		t.Fatalf("stale identity rounds=%d/%d", stale.Parent.RetryRound, stale.RetryRound)
	}
	if _, err = db.Exec(`UPDATE transcode_task SET retry_round=1 WHERE id=?; UPDATE pretranscode_rendition_job SET retry_round=1 WHERE id=?`, taskID, jobID); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	if err = w.renewJobLease(context.Background(), stale); !errors.Is(err, ErrJobOwnershipLost) {
		t.Fatalf("renew stale err=%v", err)
	}
	if err = w.updateJobProgress(context.Background(), stale, 40); !errors.Is(err, ErrJobOwnershipLost) {
		t.Fatalf("progress stale err=%v", err)
	}
	if terminal, err := w.finalizeJobAndTaskTx(context.Background(), stale, renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "/out", Encoder: "libx264"}); terminal || !errors.Is(err, ErrJobOwnershipLost) {
		t.Fatalf("finalize stale terminal=%v err=%v", terminal, err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "running", "running", "running", "processing", "processing")

	// Parent round matches current identity, but rendition round is stale: poison must still fence.
	poisonStale := exactClaimedJob(t, db, jobID, taskID, "job-owner")
	poisonStale.RetryRound = 0
	if err = w.failPoisonedParent(context.Background(), poisonStale, "stale poison"); !errors.Is(err, ErrJobOwnershipLost) {
		t.Fatalf("poison stale rendition round err=%v", err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "running", "running", "running", "processing", "processing")

	current := exactClaimedJob(t, db, jobID, taskID, "job-owner")
	if current.Parent.RetryRound != 1 || current.RetryRound != 1 {
		t.Fatalf("current rounds=%d/%d", current.Parent.RetryRound, current.RetryRound)
	}
	if err = w.renewJobLease(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err = w.updateJobProgress(context.Background(), current, 55); err != nil {
		t.Fatal(err)
	}
	if terminal, err := w.finalizeJobAndTaskTx(context.Background(), current, renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "/out", Encoder: "libx264"}); err != nil || !terminal {
		t.Fatalf("finalize current terminal=%v err=%v", terminal, err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "done", "done", "done", "published", "published")
}

func seedPublishedOptionalPrepareFailed(t *testing.T, db *sql.DB) (mediaID, runID, stepID, taskID int64) {
	t.Helper()
	mediaID = seedVideo(t, db, t.TempDir(), "fid-opt-prepare-retry", "optional-retry")
	snapshot := `[{"rendition_id":0,"rendition_name":"360p","config_snapshot":{"preset":{"id":1,"name":"x","output_format":"hls","video_codec":"libx264","audio_codec":"aac"},"rendition":{"id":1,"name":"360p","height":360,"video_bitrate":"1k"},"output_path":"immutable","priority":"normal"}}]`
	if _, err := db.Exec(`UPDATE media SET ingest_generation=1,publication_state='published',publication_error='exact media error',published_at='2026-07-01 01:02:03' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,error_message,finished_at) VALUES(?,1,'scan','published','{}','exact run error','2026-07-01 04:05:06')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error,finished_at) VALUES(?,?,1,'prepare',0,'failed',3,3,'old step error','2026-01-01')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO transcode_task(file_id,status,progress,error_message,task_type,media_id,ingest_run_id,ingest_step_id,generation,retry_round,completed_at) VALUES('fid-opt-prepare-retry','failed',67,'old parent error','pretranscode',?,?,?,1,0,'2026-01-01')`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ = res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO pretranscode_task_meta(task_id,preset_id,output_format,encryption_mode,priority,output_path,ingest_jobs_snapshot_json) VALUES(?,1,'hls','none','normal','immutable-root',?)`, taskID, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status,progress,error_message,retry_round) VALUES(?,'mutable-old','failed',88,'old rendition error',0)`, taskID); err != nil {
		t.Fatal(err)
	}
	return
}

func TestOptionalPrepareRetryPublishedRunSupportsRenewProgressFinalize(t *testing.T) {
	db := newTestDB(t)
	mediaID, runID, stepID, taskID := seedPublishedOptionalPrepareFailed(t, db)
	caps := publication.NewCapabilityMatrix([]string{"prepare"})
	if err := publication.RetryOptionalPrepare(context.Background(), db, publication.OptionalPrepareRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 7, Reason: "operator retry"}, caps); err != nil {
		t.Fatal(err)
	}
	var mediaState, mediaErr, mediaPublished, runState, runErr, runFinished string
	if err := db.QueryRow(`SELECT m.publication_state,m.publication_error,m.published_at,r.status,r.error_message,r.finished_at FROM media m JOIN media_ingest_run r ON r.id=? WHERE m.id=?`, runID, mediaID).Scan(&mediaState, &mediaErr, &mediaPublished, &runState, &runErr, &runFinished); err != nil {
		t.Fatal(err)
	}
	if mediaState != "published" || mediaErr != "exact media error" || runState != "published" || runErr != "exact run error" {
		t.Fatalf("precondition terminal media=%s/%q run=%s/%q", mediaState, mediaErr, runState, runErr)
	}

	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1, caps)
	job, _, _, claimedMedia, _, _, _, err := w.claimNextJob()
	if err != nil || job == nil {
		t.Fatalf("claim job=%v err=%v", job, err)
	}
	if claimedMedia != mediaID || job.Parent.RetryRound != 1 || job.RetryRound != 1 || job.TaskID != taskID {
		t.Fatalf("claim identity media=%d task=%d rounds=%d/%d", claimedMedia, job.TaskID, job.Parent.RetryRound, job.RetryRound)
	}
	if err = w.renewJobLease(context.Background(), *job); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if err = w.updateJobProgress(context.Background(), *job, 42); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if terminal, err := w.finalizeJobAndTaskTx(context.Background(), *job, renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "/out", Encoder: "libx264"}); err != nil || !terminal {
		t.Fatalf("finalize terminal=%v err=%v", terminal, err)
	}

	var gotMediaState, gotMediaErr, gotMediaPublished, gotRunState, gotRunErr, gotRunFinished, stepStatus, taskStatus string
	if err = db.QueryRow(`SELECT m.publication_state,m.publication_error,m.published_at,r.status,r.error_message,r.finished_at,s.status,t.status FROM media m JOIN media_ingest_run r ON r.id=? JOIN media_ingest_step s ON s.id=? JOIN transcode_task t ON t.id=? WHERE m.id=?`, runID, stepID, taskID, mediaID).Scan(&gotMediaState, &gotMediaErr, &gotMediaPublished, &gotRunState, &gotRunErr, &gotRunFinished, &stepStatus, &taskStatus); err != nil {
		t.Fatal(err)
	}
	if gotMediaState != mediaState || gotMediaErr != mediaErr || gotMediaPublished != mediaPublished || gotRunState != runState || gotRunErr != runErr || gotRunFinished != runFinished {
		t.Fatalf("terminal fields changed media=%s/%q/%s run=%s/%q/%s", gotMediaState, gotMediaErr, gotMediaPublished, gotRunState, gotRunErr, gotRunFinished)
	}
	if stepStatus != "done" || taskStatus != "done" {
		t.Fatalf("prepare completion step=%s task=%s", stepStatus, taskStatus)
	}
}

func TestCompletePrepareTxRejectsStaleRetryRound(t *testing.T) {
	db := newTestDB(t)
	taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
	parent := publication.PrepareParentIdentity{TaskID: taskID, RunID: runID, StepID: stepID, MediaID: mediaID, Generation: 1, Owner: "test-parent", RetryRound: 0}
	if _, err := db.Exec(`UPDATE transcode_task SET retry_round=1 WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = publication.CompletePrepareTx(context.Background(), tx, parent, true, ""); err == nil {
		t.Fatal("stale round accepted")
	}
	parent.RetryRound = 1
	if err = publication.CompletePrepareTx(context.Background(), tx, parent, true, ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimEligiblePreparePayloadCarriesRetryRound(t *testing.T) {
	db := newTestDB(t)
	taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='waiting',required=0,attempts=0,lease_owner=NULL,lease_until=NULL WHERE id=?; UPDATE transcode_task SET status='waiting',retry_round=3,lease_owner=NULL,lease_until=NULL WHERE id=?; UPDATE media_ingest_run SET status='published' WHERE id=?; UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, stepID, taskID, runID, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status,retry_round) VALUES(?,'360p','waiting',3)`, taskID); err != nil {
		t.Fatal(err)
	}
	payload, err := publication.ClaimEligible(context.Background(), db, publication.ClaimRequest{Family: publication.QueuePrepare, TaskType: "prepare", Owner: "prepare-worker", Registry: publication.NewCapabilityMatrix([]string{"prepare"})})
	if err != nil || payload == nil {
		t.Fatalf("payload=%v err=%v", payload, err)
	}
	if payload.RetryRound != 3 || payload.QueueID != taskID {
		t.Fatalf("retry_round=%d queue=%d", payload.RetryRound, payload.QueueID)
	}
}
