package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"knox-media/internal/store"
	"testing"
)

func completionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, e := store.OpenSQLite(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPlanCompletionLifecycleAndCounts(t *testing.T) {
	db := completionTestDB(t)
	_, e := db.Exec(`INSERT INTO library(name,type,path) VALUES('p','video','/p');INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'p','video',1,'published');INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json) VALUES(10,1,1,'repair','published','{}');INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(11,10,1,1,'poster',1,'done'),(12,10,1,1,'preview',0,'waiting'),(13,10,1,1,'subtitle',0,'running'),(14,10,1,1,'atrack',0,'skipped'),(15,10,1,1,'prepare',0,'failed'),(16,10,1,1,'package',0,'cancelled')`)
	if e != nil {
		t.Fatal(e)
	}
	tx, _ := db.Begin()
	if e = RecomputePlanCompletionTx(context.Background(), tx, 10); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var total, term, wait, run, done, skip, fail, cancel int
	var all int
	if e = db.QueryRow(`SELECT total_count,terminal_count,waiting_count,running_count,done_count,skipped_count,failed_count,cancelled_count,all_terminal FROM media_plan_completion WHERE run_id=10`).Scan(&total, &term, &wait, &run, &done, &skip, &fail, &cancel, &all); e != nil {
		t.Fatal(e)
	}
	if total != 6 || term != 4 || wait != 1 || run != 1 || done != 1 || skip != 1 || fail != 1 || cancel != 1 || all != 0 {
		t.Fatalf("%d %d %d %d %d %d %d %d %d", total, term, wait, run, done, skip, fail, cancel, all)
	}
}

func TestPlanCompletionEmptyFailsClosed(t *testing.T) {
	db := completionTestDB(t)
	db.Exec(`INSERT INTO library(name,type,path) VALUES('p','video','/p');INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'p','video',1,'processing');INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json) VALUES(10,1,1,'scan','processing','{}')`)
	tx, _ := db.Begin()
	if e := RecomputePlanCompletionTx(context.Background(), tx, 10); e == nil {
		t.Fatal("expected empty plan error")
	}
	tx.Rollback()
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM media_plan_completion`).Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
}

func seedReopenGraph(t *testing.T, db *sql.DB) {
	t.Helper()
	_, e := db.Exec(`
INSERT INTO library(name,type,path) VALUES('r','video','/r');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'r','video',1,'published');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'repair','published','{}',3);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,retry_round,last_error) VALUES
 (11,10,1,1,'media_visible',0,'done',0,0,''),
 (12,10,1,1,'subtitle_recognize',0,'failed',4,0,'recog-fail'),
 (13,10,1,1,'ai_analysis',0,'skipped',2,0,''),
 (14,10,1,1,'preview',0,'failed',3,0,'preview-fail');
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES
 (12,11,'success'),(13,12,'success'),(13,11,'terminal'),(14,11,'success');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,retry_round,last_error) VALUES
 (112,1,10,12,1,'subtitle_recognize','failed',4,0,'recog-fail'),
 (113,1,10,13,1,'ai_analysis','skipped',2,0,''),
 (114,1,10,14,1,'preview','failed',3,0,'preview-fail');`)
	if e != nil {
		t.Fatal(e)
	}
	reason, e := dependencyReason(context.Background(), db, 13, 12, 1, DependencySuccess, "failed", StepSubtitleRecognize)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`UPDATE media_ingest_step SET last_error=? WHERE id=13; UPDATE post_ingest_task SET last_error=? WHERE id=113`, reason, reason); e != nil {
		t.Fatal(e)
	}
}

func TestReopenPreparePreservesAttemptsAndIncrementsRounds(t *testing.T) {
	db := completionTestDB(t)
	_, e := db.Exec(`
INSERT INTO library(name,type,path) VALUES('rp','video','/rp');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'rp','video',1,'published');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'repair','published','{}',3);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,retry_round,last_error,finished_at) VALUES
 (11,10,1,1,'media_visible',0,'done',0,0,'',CURRENT_TIMESTAMP),
 (15,10,1,1,'prepare',0,'failed',3,0,'prepare-fail',CURRENT_TIMESTAMP);
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(15,11,'success');
INSERT INTO transcode_task(id,file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation,progress,error_message,retry_round,started_at,completed_at) VALUES
 (115,'rp',1,'failed','pretranscode',10,15,1,67,'prepare-fail',0,'2026-01-01','2026-01-01');`)
	if e != nil {
		t.Fatal(e)
	}
	tx, _ := db.Begin()
	if e = RecomputePlanCompletionTx(context.Background(), tx, 10); e != nil {
		t.Fatal(e)
	}
	var allBefore int
	if e = tx.QueryRow(`SELECT all_terminal FROM media_plan_completion WHERE run_id=10`).Scan(&allBefore); e != nil {
		t.Fatal(e)
	}
	if allBefore != 1 {
		t.Fatalf("expected all-terminal before reopen, got %d", allBefore)
	}
	if e = ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 15, ActorID: 7, Target: StepPrepare, Reason: "retry-prepare", ExpectedGeneration: 1, ExpectedRetryRound: 0}); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var status string
	var attempts, round, qRound int
	var completedAt sql.NullString
	if e = db.QueryRow(`SELECT s.status,s.attempts,s.retry_round,q.retry_round,q.completed_at FROM media_ingest_step s JOIN transcode_task q ON q.ingest_step_id=s.id WHERE s.id=15`).Scan(&status, &attempts, &round, &qRound, &completedAt); e != nil {
		t.Fatal(e)
	}
	if status != "waiting" || attempts != 3 || round != 1 || qRound != 1 || completedAt.Valid {
		t.Fatalf("status=%s attempts=%d round=%d/%d completed_at=%v", status, attempts, round, qRound, completedAt)
	}
	var family, typ string
	var taskID, auditRound int64
	if e = db.QueryRow(`SELECT task_id,task_family,task_type,retry_round FROM media_ingest_optional_retry_audit WHERE step_id=15`).Scan(&taskID, &family, &typ, &auditRound); e != nil {
		t.Fatal(e)
	}
	if taskID != 115 || family != "prepare" || typ != "prepare" || auditRound != 1 {
		t.Fatalf("audit task=%d family=%s typ=%s round=%d", taskID, family, typ, auditRound)
	}
	var allAfter int
	db.QueryRow(`SELECT all_terminal FROM media_plan_completion WHERE run_id=10`).Scan(&allAfter)
	if allAfter != 0 {
		t.Fatal("reopen must clear all-terminal")
	}
}

func TestReopenPreservesAttemptsAndIncrementsRounds(t *testing.T) {
	db := completionTestDB(t)
	seedReopenGraph(t, db)
	tx, _ := db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 14, ActorID: 7, Target: StepPreview, Reason: "retry-preview", ExpectedGeneration: 1, ExpectedRetryRound: 0}); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var status string
	var attempts, round, qAttempts, qRound int
	if e := db.QueryRow(`SELECT s.status,s.attempts,s.retry_round,q.attempts,q.retry_round FROM media_ingest_step s JOIN post_ingest_task q ON q.ingest_step_id=s.id WHERE s.id=14`).Scan(&status, &attempts, &round, &qAttempts, &qRound); e != nil {
		t.Fatal(e)
	}
	if status != "waiting" || attempts != 3 || round != 1 || qAttempts != 3 || qRound != 1 {
		t.Fatalf("status=%s attempts=%d/%d round=%d/%d", status, attempts, qAttempts, round, qRound)
	}
	if _, e := db.Exec(`UPDATE media_ingest_step SET status='failed',last_error='preview-fail-2' WHERE id=14; UPDATE post_ingest_task SET status='failed',last_error='preview-fail-2' WHERE id=114`); e != nil {
		t.Fatal(e)
	}
	tx, _ = db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 14, ActorID: 7, Reason: "again", ExpectedRetryRound: 1}); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	if e := db.QueryRow(`SELECT s.attempts,s.retry_round,q.attempts,q.retry_round FROM media_ingest_step s JOIN post_ingest_task q ON q.ingest_step_id=s.id WHERE s.id=14`).Scan(&attempts, &round, &qAttempts, &qRound); e != nil {
		t.Fatal(e)
	}
	if attempts != 3 || round != 2 || qAttempts != 3 || qRound != 2 {
		t.Fatalf("second round attempts=%d/%d round=%d/%d", attempts, qAttempts, round, qRound)
	}
	var all int
	db.QueryRow(`SELECT all_terminal FROM media_plan_completion WHERE run_id=10`).Scan(&all)
	if all != 0 {
		t.Fatal("reopen must clear all-terminal")
	}
}

func TestReopenStaleFenceRejectsWithoutMutation(t *testing.T) {
	db := completionTestDB(t)
	seedReopenGraph(t, db)
	tx, _ := db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 14, ActorID: 7, Reason: "first", ExpectedRetryRound: 0}); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	tx, _ = db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 14, ActorID: 7, Reason: "stale", ExpectedRetryRound: 0}); e == nil {
		t.Fatal("expected stale fence")
	}
	tx.Rollback()
	var round, audits int
	db.QueryRow(`SELECT retry_round FROM media_ingest_step WHERE id=14`).Scan(&round)
	db.QueryRow(`SELECT COUNT(*) FROM media_ingest_optional_retry_audit WHERE step_id=14`).Scan(&audits)
	if round != 1 || audits != 1 {
		t.Fatalf("round=%d audits=%d", round, audits)
	}
}

func TestReopenAuditStoresQueueIdentity(t *testing.T) {
	db := completionTestDB(t)
	seedReopenGraph(t, db)
	tx, _ := db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 14, ActorID: 9, Reason: "audit", ExpectedRetryRound: 0}); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var family, typ, reason, pqs, pss, pqe, pse string
	var taskID, actor, attempts, round int64
	if e := db.QueryRow(`SELECT task_id,task_family,task_type,actor_id,reason,previous_queue_status,previous_step_status,previous_attempts,previous_queue_error,previous_step_error,retry_round FROM media_ingest_optional_retry_audit WHERE step_id=14`).Scan(&taskID, &family, &typ, &actor, &reason, &pqs, &pss, &attempts, &pqe, &pse, &round); e != nil {
		t.Fatal(e)
	}
	if taskID != 114 || family != "post_ingest" || typ != "preview" || actor != 9 || reason != "audit" || pqs != "failed" || pss != "failed" || attempts != 3 || pqe != "preview-fail" || pse != "preview-fail" || round != 1 {
		t.Fatalf("%d %s %s %d %s %s %s %d %s %s %d", taskID, family, typ, actor, reason, pqs, pss, attempts, pqe, pse, round)
	}
}

func TestReopenRecognitionCascadesMatchingAI(t *testing.T) {
	db := completionTestDB(t)
	seedReopenGraph(t, db)
	tx, _ := db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 12, ActorID: 3, Reason: "recog", ExpectedRetryRound: 0}); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var recogStatus, aiStatus string
	var recogRound, aiRound, recogAttempts, aiAttempts int
	if e := db.QueryRow(`SELECT status,retry_round,attempts FROM media_ingest_step WHERE id=12`).Scan(&recogStatus, &recogRound, &recogAttempts); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(`SELECT status,retry_round,attempts FROM media_ingest_step WHERE id=13`).Scan(&aiStatus, &aiRound, &aiAttempts); e != nil {
		t.Fatal(e)
	}
	if recogStatus != "waiting" || recogRound != 1 || recogAttempts != 4 {
		t.Fatalf("recog %s round=%d attempts=%d", recogStatus, recogRound, recogAttempts)
	}
	if aiStatus != "waiting" || aiRound != 1 || aiAttempts != 2 {
		t.Fatalf("ai %s round=%d attempts=%d", aiStatus, aiRound, aiAttempts)
	}
	var audits int
	db.QueryRow(`SELECT COUNT(*) FROM media_ingest_optional_retry_audit WHERE step_id IN (12,13)`).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audits=%d", audits)
	}
	var aiFamily string
	var aiTaskID int64
	db.QueryRow(`SELECT task_id,task_family FROM media_ingest_optional_retry_audit WHERE step_id=13`).Scan(&aiTaskID, &aiFamily)
	if aiTaskID != 113 || aiFamily != "post_ingest" {
		t.Fatalf("ai audit %d %s", aiTaskID, aiFamily)
	}
}

func TestReopenRecognitionLeavesNonMatchingAISkipped(t *testing.T) {
	db := completionTestDB(t)
	seedReopenGraph(t, db)
	if _, e := db.Exec(`UPDATE media_ingest_step SET last_error='other-skip' WHERE id=13; UPDATE post_ingest_task SET last_error='other-skip' WHERE id=113`); e != nil {
		t.Fatal(e)
	}
	tx, _ := db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 12, ActorID: 3, Reason: "recog", ExpectedRetryRound: 0}); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var aiStatus string
	var aiRound int
	db.QueryRow(`SELECT status,retry_round FROM media_ingest_step WHERE id=13`).Scan(&aiStatus, &aiRound)
	if aiStatus != "skipped" || aiRound != 0 {
		t.Fatalf("ai=%s round=%d", aiStatus, aiRound)
	}
	var audits int
	db.QueryRow(`SELECT COUNT(*) FROM media_ingest_optional_retry_audit WHERE step_id=13`).Scan(&audits)
	if audits != 0 {
		t.Fatal(audits)
	}
}

func TestExplicitAIReopenRequiresDoneSuccessPredecessors(t *testing.T) {
	db := completionTestDB(t)
	seedReopenGraph(t, db)
	tx, _ := db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 13, ActorID: 5, Reason: "ai-only", ExpectedRetryRound: 0}); e == nil {
		t.Fatal("expected reject while recognition failed")
	}
	tx.Rollback()
	var status string
	var round, audits int
	db.QueryRow(`SELECT status,retry_round FROM media_ingest_step WHERE id=13`).Scan(&status, &round)
	db.QueryRow(`SELECT COUNT(*) FROM media_ingest_optional_retry_audit`).Scan(&audits)
	if status != "skipped" || round != 0 || audits != 0 {
		t.Fatalf("partial mutation status=%s round=%d audits=%d", status, round, audits)
	}
	if _, e := db.Exec(`UPDATE media_ingest_step SET status='done',last_error='' WHERE id=12; UPDATE post_ingest_task SET status='done',last_error='' WHERE id=112`); e != nil {
		t.Fatal(e)
	}
	tx, _ = db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 13, ActorID: 5, Reason: "ai-only", ExpectedRetryRound: 0}); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	db.QueryRow(`SELECT status,retry_round FROM media_ingest_step WHERE id=13`).Scan(&status, &round)
	if status != "waiting" || round != 1 {
		t.Fatalf("ai=%s round=%d", status, round)
	}
}

func TestExplicitAIReopenRejectsNonTerminalPredecessor(t *testing.T) {
	db := completionTestDB(t)
	seedReopenGraph(t, db)
	if _, e := db.Exec(`UPDATE media_ingest_step SET status='done',last_error='' WHERE id=12; UPDATE post_ingest_task SET status='done',last_error='' WHERE id=112; UPDATE media_ingest_step SET status='waiting' WHERE id=11`); e != nil {
		t.Fatal(e)
	}
	tx, _ := db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 13, ActorID: 5, Reason: "ai-only", ExpectedRetryRound: 0}); e == nil {
		t.Fatal("expected reject for non-terminal media_visible")
	}
	tx.Rollback()
	var status string
	db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=13`).Scan(&status)
	if status != "skipped" {
		t.Fatal(status)
	}
}

func TestReopenLeavesFrozenTopologyImmutable(t *testing.T) {
	db := completionTestDB(t)
	seedReopenGraph(t, db)
	var beforeSteps, beforeDeps int
	db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=10`).Scan(&beforeSteps)
	db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step_dependency`).Scan(&beforeDeps)
	tx, _ := db.Begin()
	if e := ReopenNodeTx(context.Background(), tx, ReopenRequest{RunID: 10, StepID: 12, ActorID: 1, Reason: "topo", ExpectedRetryRound: 0}); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var afterSteps, afterDeps int
	var types string
	db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=10`).Scan(&afterSteps)
	db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step_dependency`).Scan(&afterDeps)
	db.QueryRow(`SELECT GROUP_CONCAT(step_type || ':' || depends_on_step_id || ':' || dependency_kind, ',') FROM (SELECT s.step_type,d.depends_on_step_id,d.dependency_kind FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id ORDER BY d.step_id,d.depends_on_step_id)`).Scan(&types)
	if beforeSteps != afterSteps || beforeDeps != afterDeps {
		t.Fatalf("topology mutated steps %d->%d deps %d->%d", beforeSteps, afterSteps, beforeDeps, afterDeps)
	}
	if types != "subtitle_recognize:11:success,ai_analysis:11:terminal,ai_analysis:12:success,preview:11:success" {
		t.Fatal(types)
	}
}

func TestStartupOrderingPropagateProjectionAggregate(t *testing.T) {
	db := completionTestDB(t)
	_, e := db.Exec(`
INSERT INTO library(name,type,path) VALUES('s','video','/s');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state,published_at) VALUES(1,1,'s','video',1,'published',CURRENT_TIMESTAMP);
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'repair','published','{}',3);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES
 (11,10,1,1,'media_visible',0,'done'),
 (12,10,1,1,'subtitle_recognize',0,'failed'),
 (13,10,1,1,'ai_analysis',0,'waiting');
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(12,11,'success'),(13,12,'success'),(13,11,'terminal');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES
 (112,1,10,12,1,'subtitle_recognize','failed'),
 (113,1,10,13,1,'ai_analysis','waiting');`)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = PropagateImpossibleDependenciesTx(context.Background(), tx, 10); e != nil {
		t.Fatal(e)
	}
	if e = RecomputePlanCompletionTx(context.Background(), tx, 10); e != nil {
		t.Fatal(e)
	}
	if e = AggregateTx(context.Background(), tx, 10); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var aiStatus, pub string
	var all, waiting int
	if e = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=13`).Scan(&aiStatus); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(`SELECT all_terminal,waiting_count FROM media_plan_completion WHERE run_id=10`).Scan(&all, &waiting); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(`SELECT publication_state FROM media WHERE id=1`).Scan(&pub); e != nil {
		t.Fatal(e)
	}
	if aiStatus != "skipped" {
		t.Fatalf("propagate missing ai=%s", aiStatus)
	}
	if all != 1 || waiting != 0 {
		t.Fatalf("projection all=%d waiting=%d", all, waiting)
	}
	if pub != "published" {
		t.Fatalf("aggregate changed publication=%s", pub)
	}
	var reason impossibleDependencyReason
	var raw string
	db.QueryRow(`SELECT last_error FROM media_ingest_step WHERE id=13`).Scan(&raw)
	if e = json.Unmarshal([]byte(raw), &reason); e != nil || reason.PredecessorID != 12 || reason.Code != "dependency_impossible" {
		t.Fatalf("%q %+v", raw, reason)
	}
}
