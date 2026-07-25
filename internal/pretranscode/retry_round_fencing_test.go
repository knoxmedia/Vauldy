package pretranscode

import (
	"context"
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
