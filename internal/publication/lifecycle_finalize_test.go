package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func seedLifecycleGraph(t *testing.T, db *sql.DB) (runID, previewStep, aiStep, previewTask, aiTask int64) {
	t.Helper()
	_, e := db.Exec(`
INSERT INTO library(name,type,path) VALUES('lc','video','/lc');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'lc','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'scan','processing','{}',3);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts) VALUES
 (11,10,1,1,'poster',1,'done',1,3),
 (12,10,1,1,'preview',0,'running',3,3),
 (13,10,1,1,'ai_analysis',0,'waiting',0,3);
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES
 (12,11,'success'),(13,12,'success'),(13,11,'terminal');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,lease_owner,lease_until) VALUES
 (112,1,10,12,1,'preview','running',3,3,'owner/tok',datetime(CURRENT_TIMESTAMP,'+60 seconds')),
 (113,1,10,13,1,'ai_analysis','waiting',0,3,NULL,NULL);`)
	if e != nil {
		t.Fatal(e)
	}
	return 10, 12, 13, 112, 113
}

func assertLifecycleSnapshot(t *testing.T, db *sql.DB, runID, previewStep, aiStep, previewTask, aiTask int64, wantPreview, wantAI string, wantAllTerminal int, wantPub string) {
	t.Helper()
	var qPrev, sPrev, qAI, sAI, pub string
	var all, waiting int
	if e := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, previewTask).Scan(&qPrev); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, previewStep).Scan(&sPrev); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, aiTask).Scan(&qAI); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, aiStep).Scan(&sAI); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(`SELECT all_terminal,waiting_count FROM media_plan_completion WHERE run_id=?`, runID).Scan(&all, &waiting); e != nil {
		t.Fatalf("plan completion missing: %v", e)
	}
	if e := db.QueryRow(`SELECT publication_state FROM media WHERE id=1`).Scan(&pub); e != nil {
		t.Fatal(e)
	}
	if qPrev != wantPreview || sPrev != wantPreview || qAI != wantAI || sAI != wantAI {
		t.Fatalf("queue/step preview=%s/%s ai=%s/%s want preview=%s ai=%s", qPrev, sPrev, qAI, sAI, wantPreview, wantAI)
	}
	if all != wantAllTerminal || (wantAllTerminal == 1 && waiting != 0) {
		t.Fatalf("plan all=%d waiting=%d want all=%d", all, waiting, wantAllTerminal)
	}
	if pub != wantPub {
		t.Fatalf("publication=%s want %s", pub, wantPub)
	}
	if wantAI == "skipped" {
		var raw string
		var reason impossibleDependencyReason
		if e := db.QueryRow(`SELECT last_error FROM media_ingest_step WHERE id=?`, aiStep).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if e := json.Unmarshal([]byte(raw), &reason); e != nil || reason.Code != "dependency_impossible" || reason.PredecessorID != previewStep {
			t.Fatalf("ai skip reason %q %+v", raw, reason)
		}
	}
}

func TestFinalizeNodeTransitionTxInvokesRetirementBarrierHook(t *testing.T) {
	db := completionTestDB(t)
	runID, _, _, _, _ := seedLifecycleGraph(t, db)
	seen := make([]int64, 0, 1)
	retirementBarrierProbe = func(id int64) { seen = append(seen, id) }
	t.Cleanup(func() { retirementBarrierProbe = nil })
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = FinalizeNodeTransitionTx(context.Background(), tx, runID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if len(seen) != 1 || seen[0] != runID {
		t.Fatalf("retirement barrier probe=%v", seen)
	}
}

func TestFinalizeNodeTransitionTxAtomicFailRollsBackProjection(t *testing.T) {
	db := completionTestDB(t)
	runID, previewStep, aiStep, previewTask, aiTask := seedLifecycleGraph(t, db)
	if _, e := db.Exec(`UPDATE post_ingest_task SET status='failed',lease_owner=NULL,lease_until=NULL,finished_at=CURRENT_TIMESTAMP WHERE id=?`, previewTask); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`UPDATE media_ingest_step SET status='failed',lease_owner=NULL,lease_until=NULL,finished_at=CURRENT_TIMESTAMP,last_error='boom' WHERE id=?`, previewStep); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`CREATE TRIGGER fail_plan_completion BEFORE INSERT ON media_plan_completion BEGIN SELECT RAISE(ABORT,'plan blocked'); END`); e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = FinalizeNodeTransitionTx(context.Background(), tx, runID); e == nil {
		t.Fatal("expected finalize failure")
	}
	_ = tx.Rollback()
	var qAI, sAI string
	var plans int
	db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, aiTask).Scan(&qAI)
	db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, aiStep).Scan(&sAI)
	db.QueryRow(`SELECT COUNT(*) FROM media_plan_completion WHERE run_id=?`, runID).Scan(&plans)
	if qAI != "waiting" || sAI != "waiting" || plans != 0 {
		t.Fatalf("partial commit ai=%s/%s plans=%d", qAI, sAI, plans)
	}
}

func TestCancelRunTxFinalizesDependencyPlanCompletionAndAggregate(t *testing.T) {
	db := completionTestDB(t)
	runID, previewStep, aiStep, previewTask, aiTask := seedLifecycleGraph(t, db)
	if _, e := db.Exec(`UPDATE post_ingest_task SET status='waiting',lease_owner=NULL,lease_until=NULL,attempts=0 WHERE id=?;
UPDATE media_ingest_step SET status='waiting',lease_owner=NULL,lease_until=NULL,attempts=0 WHERE id=?`, previewTask, previewStep); e != nil {
		t.Fatal(e)
	}
	seen := 0
	retirementBarrierProbe = func(int64) { seen++ }
	t.Cleanup(func() { retirementBarrierProbe = nil })
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	ok, e := CancelRunTx(context.Background(), tx, runID, "operator_cancel")
	if e != nil || !ok {
		t.Fatalf("cancel ok=%v err=%v", ok, e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if seen != 1 {
		t.Fatalf("retirement barrier calls=%d", seen)
	}
	var runStatus, qPrev, sPrev, qAI, sAI, pub string
	var all int
	db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runStatus)
	db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, previewTask).Scan(&qPrev)
	db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, previewStep).Scan(&sPrev)
	db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, aiTask).Scan(&qAI)
	db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, aiStep).Scan(&sAI)
	db.QueryRow(`SELECT all_terminal FROM media_plan_completion WHERE run_id=?`, runID).Scan(&all)
	db.QueryRow(`SELECT publication_state FROM media WHERE id=1`).Scan(&pub)
	if runStatus != "cancelled" || qPrev != "cancelled" || sPrev != "cancelled" {
		t.Fatalf("cancel states run=%s preview=%s/%s", runStatus, qPrev, sPrev)
	}
	if qAI != "cancelled" && qAI != "skipped" {
		t.Fatalf("ai queue=%s step=%s", qAI, sAI)
	}
	if sAI != "cancelled" && sAI != "skipped" {
		t.Fatalf("ai step=%s", sAI)
	}
	if all != 1 || pub != "cancelled" {
		t.Fatalf("plan all=%d pub=%s", all, pub)
	}
}

func TestReopenNodeTxFinalizesPlanCompletionAggregateAndBarrier(t *testing.T) {
	db := completionTestDB(t)
	seedReopenGraph(t, db)
	seen := 0
	retirementBarrierProbe = func(int64) { seen++ }
	t.Cleanup(func() { retirementBarrierProbe = nil })
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = RecomputePlanCompletionTx(context.Background(), tx, 10); e != nil {
		t.Fatal(e)
	}
	if e = ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 14, ActorID: 7, Target: StepPreview, Reason: "retry-preview", ExpectedGeneration: 1, ExpectedRetryRound: 0}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if seen != 1 {
		t.Fatalf("retirement barrier calls=%d", seen)
	}
	var all, waiting int
	var pub string
	if e = db.QueryRow(`SELECT all_terminal,waiting_count FROM media_plan_completion WHERE run_id=10`).Scan(&all, &waiting); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(`SELECT publication_state FROM media WHERE id=1`).Scan(&pub); e != nil {
		t.Fatal(e)
	}
	if all != 0 || waiting < 1 {
		t.Fatalf("projection all=%d waiting=%d", all, waiting)
	}
	if pub != "published" {
		t.Fatalf("aggregate changed publication=%s", pub)
	}
}

func TestPlanCompletionFinalizePropagatesSkipFromFailedPreview(t *testing.T) {
	db := completionTestDB(t)
	runID, previewStep, aiStep, previewTask, aiTask := seedLifecycleGraph(t, db)
	if _, e := db.Exec(`UPDATE post_ingest_task SET status='failed',lease_owner=NULL,lease_until=NULL,finished_at=CURRENT_TIMESTAMP,last_error='preview boom' WHERE id=?`, previewTask); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`UPDATE media_ingest_step SET status='failed',lease_owner=NULL,lease_until=NULL,finished_at=CURRENT_TIMESTAMP,last_error='preview boom' WHERE id=?`, previewStep); e != nil {
		t.Fatal(e)
	}
	seen := 0
	retirementBarrierProbe = func(int64) { seen++ }
	t.Cleanup(func() { retirementBarrierProbe = nil })
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = FinalizeNodeTransitionTx(context.Background(), tx, runID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if seen != 1 {
		t.Fatalf("retirement barrier calls=%d", seen)
	}
	assertLifecycleSnapshot(t, db, runID, previewStep, aiStep, previewTask, aiTask, "failed", "skipped", 1, "published")
}
