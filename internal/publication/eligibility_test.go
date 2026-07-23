package publication

import (
	"context"
	"database/sql"
	"knox-media/internal/store"
	"strings"
	"testing"
)

func openEligibilityDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(t.TempDir() + "/eligibility.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestLinkedClaimEligibilitySQLDependencyMatrix(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,preserve_visibility,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing',0,'{}',2);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'),(31,20,10,1,'encrypt',1,'waiting'),(32,20,10,1,'preview',0,'waiting');
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(31,30,'step_done'),(32,NULL,'media_visible');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,31,1,'encrypt','waiting'),(41,10,20,32,1,'preview','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	eligible := func(id int64) bool {
		var ok bool
		err := db.QueryRowContext(context.Background(), `SELECT `+LinkedClaimEligibilitySQL("p")+` FROM post_ingest_task p WHERE p.id=?`, id).Scan(&ok)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if eligible(40) {
		t.Fatal("encrypt eligible before dependency done")
	}
	db.Exec(`UPDATE media_ingest_step SET status='skipped' WHERE id=30`)
	if !eligible(40) {
		t.Fatal("skipped dependency should satisfy step_done")
	}
	if eligible(41) {
		t.Fatal("media_visible eligible while hidden")
	}
	db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=10; UPDATE media_ingest_run SET status='published' WHERE id=20; UPDATE media_ingest_step SET status='done' WHERE id IN (30,31)`)
	if !eligible(41) {
		t.Fatal("published media should satisfy media_visible")
	}
	db.Exec(`UPDATE media SET publication_state='degraded' WHERE id=10; UPDATE media_ingest_run SET status='degraded' WHERE id=20`)
	if !eligible(41) {
		t.Fatal("visible degraded replacement should satisfy media_visible")
	}
	db.Exec(`UPDATE media SET published_at=NULL WHERE id=10`)
	if eligible(41) {
		t.Fatal("degraded media without prior publication is hidden")
	}
}

func TestLinkedClaimEligibilitySQLFailsClosedForStaleAndMalformedIdentity(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',2,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,preserve_visibility,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing',0,'{}',2),(21,10,2,'scan','processing',0,'{}',2);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'done'),(31,21,10,2,'encrypt',1,'waiting');
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(31,30,'step_done');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'poster','waiting'),(41,10,21,31,2,'encrypt','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{40, 41} {
		var ok bool
		if err := db.QueryRow(`SELECT `+LinkedClaimEligibilitySQL("p")+` FROM post_ingest_task p WHERE p.id=?`, id).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatalf("task %d unexpectedly eligible", id)
		}
	}
}

func TestPostingestClaimReturnsPublicationIdentity(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,scan_task_id,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,NULL,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'poster','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "worker", Registry: NewCapabilityMatrix([]string{"poster"})})
	if err != nil {
		t.Fatal(err)
	}
	if payload == nil || payload.Family != QueuePostIngest || payload.QueueID != 40 || payload.MediaID != 10 || payload.RunID.Int64 != 20 || payload.StepID.Int64 != 30 || payload.Generation.Int64 != 1 || payload.Owner == "" || payload.Attempts != 1 {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestRequiredFirstAcrossPostIngestScrapePrepareRace(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state,published_at) VALUES(10,1,'f','video',1,'published',CURRENT_TIMESTAMP); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'repair','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,created_at) VALUES(30,20,10,1,'poster',1,'waiting','2020-01-01'),(31,20,10,1,'scrape',0,'waiting','2020-01-02'),(32,20,10,1,'prepare',0,'waiting','2020-01-03'); INSERT INTO media_ingest_step_dependency(step_id,dependency_kind) VALUES(31,'media_visible'),(32,'media_visible'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'poster','waiting'); INSERT INTO scrape_task(id,media_id,status,ingest_run_id,ingest_step_id,generation) VALUES(41,10,'waiting',20,31,1); INSERT INTO transcode_task(id,file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation) VALUES(42,'f',10,'waiting','pretranscode',20,32,1)`)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewCapabilityMatrix([]string{"poster", "scrape", "prepare"})
	for _, req := range []ClaimRequest{{Family: QueueScrape, TaskType: "scrape", Owner: "s", Registry: registry}, {Family: QueuePrepare, TaskType: "prepare", Owner: "p", Registry: registry}} {
		got, e := ClaimEligible(context.Background(), db, req)
		if e != nil || got != nil {
			t.Fatalf("optional bypass req=%+v got=%+v err=%v", req, got, e)
		}
	}
	got, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "q", Registry: registry})
	if err != nil || got == nil || got.QueueID != 40 {
		t.Fatalf("required=%+v err=%v", got, err)
	}
}

func TestPrepareClaimsParentOnceBeforeRenditions(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state,published_at) VALUES(10,1,'f','video',1,'published',CURRENT_TIMESTAMP); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'repair','published','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'prepare',0,'waiting'); INSERT INTO media_ingest_step_dependency(step_id,dependency_kind) VALUES(30,'media_visible'); INSERT INTO transcode_task(id,file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation) VALUES(40,'f',10,'waiting','pretranscode',20,30,1)`)
	if err != nil {
		t.Fatal(err)
	}
	req := ClaimRequest{Family: QueuePrepare, TaskType: "prepare", Owner: "worker", Registry: NewCapabilityMatrix([]string{"prepare"})}
	first, err := ClaimEligible(context.Background(), db, req)
	if err != nil || first == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := ClaimEligible(context.Background(), db, req)
	if err != nil || second != nil {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	var status, owner string
	if err = db.QueryRow(`SELECT status,lease_owner FROM transcode_task WHERE id=40`).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner != first.Owner {
		t.Fatalf("status=%s owner=%s payload=%+v", status, owner, first)
	}
}

func TestClaimEligibleCommunitySkipsAbsentUnavailablePrepareTable(t *testing.T) {
	db := openEligibilityDB(t)
	if _, err := db.Exec(`DROP TABLE pretranscode_rendition_job; DROP TABLE pretranscode_task_meta; DROP TABLE transcode_task`); err != nil {
		t.Fatal(err)
	}
	registry := NewCapabilityMatrix([]string{"poster", "scrape"})
	if got, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "community", Registry: registry}); err != nil || got != nil {
		t.Fatalf("claim=%+v err=%v", got, err)
	}
}

func TestClaimEligibleAdvertisedCapabilityMissingTableFailsClosed(t *testing.T) {
	db := openEligibilityDB(t)
	if _, err := db.Exec(`DROP TABLE pretranscode_rendition_job; DROP TABLE pretranscode_task_meta; DROP TABLE transcode_task`); err != nil {
		t.Fatal(err)
	}
	_, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePrepare, TaskType: "prepare", Owner: "enterprise", Registry: NewCapabilityMatrix([]string{"prepare"})})
	if err == nil || !strings.Contains(err.Error(), "advertised capability prepare") {
		t.Fatalf("err=%v", err)
	}
}

func TestClaimByOwnerReturnsCompleteAuthoritativePayload(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,lease_owner,lease_until) VALUES(30,20,10,1,'poster',1,'running','owner/token',datetime(CURRENT_TIMESTAMP,'+90 seconds')); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,lease_owner,lease_until) VALUES(40,10,20,30,1,'poster','running',2,3,'owner/token',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := claimByOwner(context.Background(), db, QueuePostIngest, "owner/token")
	if err != nil {
		t.Fatal(err)
	}
	if p.QueueID != 40 || p.MediaID != 10 || p.RunID.Int64 != 20 || p.StepID.Int64 != 30 || p.Generation.Int64 != 1 || p.TaskType != "poster" || p.Attempts != 2 || p.MaxAttempts != 3 || p.LeaseUntil.IsZero() {
		t.Fatalf("payload=%+v", p)
	}
	_, _ = db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=10`)
	if p, err = claimByOwner(context.Background(), db, QueuePostIngest, "owner/token"); err == nil || p != nil {
		t.Fatalf("stale payload=%+v err=%v", p, err)
	}
}

func TestClaimEligibleLinkedStepCASRequiresExactlyOneTransition(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'poster','waiting'); CREATE TRIGGER invalidate_step_before_queue_claim BEFORE UPDATE OF status ON post_ingest_task WHEN NEW.status='running' BEGIN UPDATE media_ingest_step SET status='cancelled' WHERE id=30; END`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "owner", Registry: NewCapabilityMatrix([]string{"poster"})})
	if err == nil || p != nil {
		t.Fatalf("payload=%+v err=%v", p, err)
	}
	var qs, ss string
	_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=40`).Scan(&qs)
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=30`).Scan(&ss)
	if qs != "waiting" || ss != "waiting" {
		t.Fatalf("states=%s/%s", qs, ss)
	}
}
