package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"knox-media/internal/store"
	"testing"
)

func dependencyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, e := store.OpenSQLite(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedDependencyRun(t *testing.T, db *sql.DB) (int64, int64) {
	t.Helper()
	_, e := db.Exec(`INSERT INTO library(name,type,path) VALUES('d','video','/d');INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'f','video',1,'processing');INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json) VALUES(10,1,1,'scan','processing','{}');INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,retry_round) VALUES(11,10,1,1,'poster',0,'failed',0),(12,10,1,1,'subtitle_recognize',0,'waiting',0),(13,10,1,1,'ai_analysis',0,'waiting',0),(14,10,1,1,'media_visible',0,'done',0);INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(12,11,'success'),(13,12,'success'),(13,14,'terminal');INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,last_error) VALUES(112,1,10,12,1,'subtitle_recognize','waiting',''),(113,1,10,13,1,'ai_analysis','waiting','')`)
	if e != nil {
		t.Fatal(e)
	}
	return 10, 1
}

func TestPropagateImpossibleDependenciesFixpointAndReason(t *testing.T) {
	db := dependencyTestDB(t)
	run, _ := seedDependencyRun(t, db)
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = PropagateImpossibleDependenciesTx(context.Background(), tx, run); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var a, b string
	if e = db.QueryRow(`SELECT status,last_error FROM media_ingest_step WHERE id=12`).Scan(&a, &b); e != nil {
		t.Fatal(e)
	}
	if a != "skipped" {
		t.Fatal(a)
	}
	var r impossibleDependencyReason
	if e = json.Unmarshal([]byte(b), &r); e != nil {
		t.Fatal(e)
	}
	if r.PredecessorID != 11 || r.DependencyKind != DependencySuccess || r.PredecessorState != "failed" || r.Code != "dependency_impossible" {
		t.Fatalf("%+v", r)
	}
	if e = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=13`).Scan(&a); e != nil {
		t.Fatal(e)
	}
	if a != "skipped" {
		t.Fatalf("descendant=%s", a)
	}
	before := b
	tx, e = db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = PropagateImpossibleDependenciesTx(context.Background(), tx, run); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	if e = db.QueryRow(`SELECT last_error FROM media_ingest_step WHERE id=12`).Scan(&b); e != nil || b != before {
		t.Fatalf("reason changed %q/%q", before, b)
	}
}

func TestPropagateTerminalDependencyDoesNotSkip(t *testing.T) {
	db := dependencyTestDB(t)
	run, _ := seedDependencyRun(t, db)
	db.Exec(`UPDATE media_ingest_step SET status='waiting' WHERE id=12;UPDATE media_ingest_step SET status='failed' WHERE id=11;UPDATE media_ingest_step_dependency SET dependency_kind='terminal' WHERE step_id=12`)
	tx, _ := db.Begin()
	if e := PropagateImpossibleDependenciesTx(context.Background(), tx, run); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var status string
	db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=12`).Scan(&status)
	if status != "waiting" {
		t.Fatal(status)
	}
}

func TestPropagateRetryableWaitingDoesNotSkip(t *testing.T) {
	db := dependencyTestDB(t)
	run, _ := seedDependencyRun(t, db)
	db.Exec(`UPDATE media_ingest_step SET status='waiting' WHERE id=11`)
	tx, _ := db.Begin()
	if e := PropagateImpossibleDependenciesTx(context.Background(), tx, run); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	for _, id := range []int64{12, 13} {
		var status string
		db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, id).Scan(&status)
		if status != "waiting" {
			t.Fatalf("%d=%s", id, status)
		}
	}
}

func TestPropagateStructuredRecognitionToAI(t *testing.T) {
	db := dependencyTestDB(t)
	run, _ := seedDependencyRun(t, db)
	db.Exec(`UPDATE media_ingest_step SET status='cancelled' WHERE id=12; UPDATE post_ingest_task SET status='cancelled' WHERE id=112; UPDATE media_ingest_step SET status='waiting' WHERE id=13; UPDATE post_ingest_task SET status='waiting' WHERE id=113`)
	tx, _ := db.Begin()
	if e := PropagateImpossibleDependenciesTx(context.Background(), tx, run); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var status, raw string
	db.QueryRow(`SELECT status,last_error FROM media_ingest_step WHERE id=13`).Scan(&status, &raw)
	if status != "skipped" {
		t.Fatal(status)
	}
	var r impossibleDependencyReason
	if e := json.Unmarshal([]byte(raw), &r); e != nil {
		t.Fatal(e)
	}
	if r.PredecessorID != 12 || r.PredecessorType != StepSubtitleRecognize || r.PredecessorState != "cancelled" || r.DependencyKind != DependencySuccess || r.RetryRound != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestPropagateTerminalMatrixAndIdempotence(t *testing.T) {
	for _, terminal := range []string{"done", "skipped", "failed", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			db := dependencyTestDB(t)
			run, _ := seedDependencyRun(t, db)
			db.Exec(`UPDATE media_ingest_step SET status=? WHERE id=14`, terminal)
			db.Exec(`UPDATE media_ingest_step SET status='waiting' WHERE id IN (12,13); UPDATE media_ingest_step SET status='done' WHERE id=11`)
			tx, _ := db.Begin()
			if e := PropagateImpossibleDependenciesTx(context.Background(), tx, run); e != nil {
				t.Fatal(e)
			}
			tx.Commit()
			var status string
			db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=13`).Scan(&status)
			if status != "waiting" {
				t.Fatalf("terminal %s should not skip success-satisfied AI, got %s", terminal, status)
			}
			tx, _ = db.Begin()
			if e := PropagateImpossibleDependenciesTx(context.Background(), tx, run); e != nil {
				t.Fatal(e)
			}
			tx.Commit()
			db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=13`).Scan(&status)
			if status != "waiting" {
				t.Fatal(status)
			}
		})
	}
}

func TestPropagateImpossibleSkipsAlignOwningQueues(t *testing.T) {
	db := dependencyTestDB(t)
	_, e := db.Exec(`
INSERT INTO library(name,type,path) VALUES('q','video','/q');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'q','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json) VALUES(10,1,1,'scan','processing','{}');
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,retry_round) VALUES
 (20,10,1,1,'poster',1,'failed',0),
 (21,10,1,1,'scrape',0,'waiting',0),
 (22,10,1,1,'ai_analysis',0,'waiting',0),
 (23,10,1,1,'prepare',0,'waiting',0),
 (24,10,1,1,'media_visible',1,'waiting',0);
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES
 (21,20,'success'),(22,20,'success'),(23,20,'success'),(24,20,'success');
INSERT INTO scrape_task(id,media_id,status,ingest_run_id,ingest_step_id,generation,message) VALUES(121,1,'waiting',10,21,1,'');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,last_error) VALUES(122,1,10,22,1,'ai_analysis','waiting','');
INSERT INTO transcode_task(id,file_id,status,task_type,media_id,ingest_run_id,ingest_step_id,generation,error_message) VALUES(123,'q','waiting','pretranscode',1,10,23,1,'');`)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = PropagateImpossibleDependenciesTx(context.Background(), tx, 10); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		stepID int64
		table  string
		errCol string
	}{
		{21, "scrape_task", "message"},
		{22, "post_ingest_task", "last_error"},
		{23, "transcode_task", "error_message"},
	} {
		var stepStatus, queueStatus, raw string
		q := `SELECT s.status,q.status,COALESCE(q.` + tc.errCol + `, '') FROM media_ingest_step s JOIN ` + tc.table + ` q ON q.ingest_step_id=s.id WHERE s.id=?`
		if e = db.QueryRow(q, tc.stepID).Scan(&stepStatus, &queueStatus, &raw); e != nil {
			t.Fatalf("%s: %v", tc.table, e)
		}
		if stepStatus != "skipped" || queueStatus != "skipped" {
			t.Fatalf("%s step=%s queue=%s", tc.table, stepStatus, queueStatus)
		}
		var r impossibleDependencyReason
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			t.Fatal(e)
		}
		if r.Code != "dependency_impossible" || r.PredecessorID != 20 {
			t.Fatalf("%s reason=%+v", tc.table, r)
		}
	}
	var visible string
	if e = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=24`).Scan(&visible); e != nil {
		t.Fatal(e)
	}
	if visible != "skipped" {
		t.Fatalf("media_visible=%s", visible)
	}
	var scrapeWrong, prepareWrong, postWrong int
	db.QueryRow(`SELECT COUNT(*) FROM scrape_task WHERE status<>'skipped'`).Scan(&scrapeWrong)
	db.QueryRow(`SELECT COUNT(*) FROM transcode_task WHERE status<>'skipped'`).Scan(&prepareWrong)
	db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE status<>'skipped'`).Scan(&postWrong)
	if scrapeWrong != 0 || prepareWrong != 0 || postWrong != 0 {
		t.Fatalf("cross-family residue scrape=%d prepare=%d post=%d", scrapeWrong, prepareWrong, postWrong)
	}
}

func TestPropagateImpossibleFailsClosedOnMissingQueue(t *testing.T) {
	db := dependencyTestDB(t)
	_, e := db.Exec(`
INSERT INTO library(name,type,path) VALUES('o','video','/o');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'o','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json) VALUES(10,1,1,'scan','processing','{}');
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES
 (30,10,1,1,'poster',1,'failed'),(31,10,1,1,'scrape',0,'waiting');
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(31,30,'success');`)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = PropagateImpossibleDependenciesTx(context.Background(), tx, 10); e == nil {
		tx.Rollback()
		t.Fatal("expected orphan scrape queue failure")
	}
	tx.Rollback()
	var status string
	if e = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=31`).Scan(&status); e != nil {
		t.Fatal(e)
	}
	if status != "waiting" {
		t.Fatalf("partial skip leaked status=%s", status)
	}
}

func TestPropagateImpossiblePostIngestAISkipStillWorks(t *testing.T) {
	db := dependencyTestDB(t)
	run, _ := seedDependencyRun(t, db)
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = PropagateImpossibleDependenciesTx(context.Background(), tx, run); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var stepStatus, queueStatus string
	if e = db.QueryRow(`SELECT s.status,q.status FROM media_ingest_step s JOIN post_ingest_task q ON q.ingest_step_id=s.id WHERE s.id=13`).Scan(&stepStatus, &queueStatus); e != nil {
		t.Fatal(e)
	}
	if stepStatus != "skipped" || queueStatus != "skipped" {
		t.Fatalf("ai step=%s queue=%s", stepStatus, queueStatus)
	}
	var scrapeN, prepareN int
	db.QueryRow(`SELECT COUNT(*) FROM scrape_task`).Scan(&scrapeN)
	db.QueryRow(`SELECT COUNT(*) FROM transcode_task`).Scan(&prepareN)
	if scrapeN != 0 || prepareN != 0 {
		t.Fatalf("unexpected foreign queue rows scrape=%d prepare=%d", scrapeN, prepareN)
	}
}
