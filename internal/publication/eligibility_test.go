package publication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"knox-media/internal/scheduler"
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
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(33,20,10,1,'media_visible',0,'done'); INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(31,30,'success'),(32,33,'success');
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
	if eligible(40) {
		t.Fatal("skipped dependency must not satisfy success")
	}
	if eligible(41) {
		t.Fatal("optional work remains blocked until publication is visible")
	}
	db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=10; UPDATE media_ingest_run SET status='published' WHERE id=20; UPDATE media_ingest_step SET status='done' WHERE id IN (30,31)`)
	if !eligible(41) {
		t.Fatal("published media should preserve visibility eligibility")
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
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(31,30,'success');
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

func TestPostIngestEligibleTaskTypesPreservesRequestOrder(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',0,'processing');
INSERT INTO post_ingest_task(id,media_id,generation,task_type,status) VALUES(40,10,0,'subtitle','waiting'),(41,10,0,'poster','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	types, err := PostIngestEligibleTaskTypes(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskTypes: []string{"preview", "subtitle", "poster"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(types, ","), "subtitle,poster"; got != want {
		t.Fatalf("eligible types=%q want %q", got, want)
	}
}

func TestPostIngestEligibleTaskTypesUsesCanonicalLinkedEligibility(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'),(31,20,10,1,'encrypt',1,'waiting');
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(31,30,'success');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,31,1,'encrypt','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	req := ClaimRequest{Family: QueuePostIngest, TaskTypes: []string{"encrypt"}}
	types, err := PostIngestEligibleTaskTypes(context.Background(), db, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 0 {
		t.Fatalf("blocked required linked type hinted eligible: %v", types)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='done' WHERE id=30`); err != nil {
		t.Fatal(err)
	}
	types, err = PostIngestEligibleTaskTypes(context.Background(), db, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 || types[0] != "encrypt" {
		t.Fatalf("eligible required linked type missing: %v", types)
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
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state,published_at) VALUES(10,1,'f','video',1,'published',CURRENT_TIMESTAMP); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'repair','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,created_at) VALUES(30,20,10,1,'poster',1,'waiting','2020-01-01'),(31,20,10,1,'scrape',0,'waiting','2020-01-02'),(32,20,10,1,'prepare',0,'waiting','2020-01-03'),(33,20,10,1,'media_visible',0,'done','2020-01-01'); INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(31,33,'success'),(32,33,'success'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'poster','waiting'); INSERT INTO scrape_task(id,media_id,status,ingest_run_id,ingest_step_id,generation) VALUES(41,10,'waiting',20,31,1); INSERT INTO transcode_task(id,file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation) VALUES(42,'f',10,'waiting','pretranscode',20,32,1)`)
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

func TestClaimEligibleAnySkipsUnclaimableOldestRequiredPoster(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES
 (10,1,'f10','video',1,'processing'),
 (11,1,'f11','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES
 (20,10,1,'scan','processing','{}',2),
 (21,11,1,'scan','processing','{}',2);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES
 (30,20,10,1,'poster',1,'waiting'),
 (31,21,11,1,'encrypt',1,'waiting');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,available_at,created_at) VALUES
 (40,10,20,30,1,'poster','waiting','2020-01-01','2020-01-01'),
 (41,11,21,31,1,'encrypt','waiting','2020-01-02','2020-01-02')`)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewCapabilityMatrix([]string{"poster", "encrypt"})
	// Poster slots full: dispatcher only offers non-poster types.
	got, err := ClaimEligibleAny(context.Background(), db, ClaimRequest{
		Family: QueuePostIngest, TaskTypes: []string{"encrypt"}, Owner: "worker", Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.QueueID != 41 || got.TaskType != "encrypt" {
		t.Fatalf("want encrypt queue 41, got=%+v", got)
	}
	var posterStatus string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=40`).Scan(&posterStatus); err != nil {
		t.Fatal(err)
	}
	if posterStatus != "waiting" {
		t.Fatalf("unclaimable oldest poster status=%s want waiting", posterStatus)
	}
}

func TestPrepareClaimsParentOnceBeforeRenditions(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state,published_at) VALUES(10,1,'f','video',1,'published',CURRENT_TIMESTAMP); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'repair','published','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'prepare',0,'waiting'),(31,20,10,1,'media_visible',0,'done'); INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(30,31,'success'); INSERT INTO transcode_task(id,file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation) VALUES(40,'f',10,'waiting','pretranscode',20,30,1)`)
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
	dropEnterprisePrepareTablesIfPresent(t, db)
	if _, err := db.Exec(`DROP TABLE transcode_task`); err != nil {
		t.Fatal(err)
	}
	registry := NewCapabilityMatrix([]string{"poster", "scrape"})
	if got, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "community", Registry: registry}); err != nil || got != nil {
		t.Fatalf("claim=%+v err=%v", got, err)
	}
}

func TestClaimEligibleAdvertisedCapabilityMissingTableFailsClosed(t *testing.T) {
	db := openEligibilityDB(t)
	dropEnterprisePrepareTablesIfPresent(t, db)
	if _, err := db.Exec(`DROP TABLE transcode_task`); err != nil {
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

func TestGlobalRequiredOrderingBlocksYoungerFamily(t *testing.T) {
	db := openEligibilityDB(t)
	seedThreeRequiredClaims(t, db)
	registry := NewCapabilityMatrix([]string{"poster", "scrape", "prepare"})
	if got, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "post", Registry: registry}); err != nil || got != nil {
		t.Fatalf("younger post claim=%+v err=%v", got, err)
	}
	got, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueueScrape, TaskType: "scrape", Owner: "scrape", Registry: registry})
	if err != nil || got == nil || got.Family != QueueScrape || got.QueueID != 41 {
		t.Fatalf("oldest scrape=%+v err=%v", got, err)
	}
}

func TestRequiredFirstAcrossPostIngestScrapePrepareSimultaneousBarrier(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		db := openEligibilityDB(t)
		seedThreeRequiredClaims(t, db)
		if _, err := db.Exec(`UPDATE media_ingest_step SET required=0 WHERE id IN (30,32)`); err != nil {
			t.Fatal(err)
		}
		registry := NewCapabilityMatrix([]string{"poster", "scrape", "prepare"})
		start := make(chan struct{})
		results := make(chan *ClaimPayload, 3)
		errs := make(chan error, 3)
		for _, req := range []ClaimRequest{{Family: QueuePostIngest, TaskType: "poster", Owner: "p", Registry: registry}, {Family: QueueScrape, TaskType: "scrape", Owner: "s", Registry: registry}, {Family: QueuePrepare, TaskType: "prepare", Owner: "t", Registry: registry}} {
			req := req
			go func() { <-start; p, e := ClaimEligible(context.Background(), db, req); results <- p; errs <- e }()
		}
		close(start)
		var claims []*ClaimPayload
		for i := 0; i < 3; i++ {
			if e := <-errs; e != nil {
				t.Fatal(e)
			}
			if p := <-results; p != nil {
				claims = append(claims, p)
			}
		}
		if len(claims) != 1 || claims[0].Family != QueueScrape || claims[0].QueueID != 41 {
			t.Fatalf("iteration=%d claims=%+v", iteration, claims)
		}
	}
}

func seedThreeRequiredClaims(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'),(31,20,10,1,'scrape',1,'waiting'),(32,20,10,1,'prepare',1,'waiting'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,available_at,created_at) VALUES(40,10,20,30,1,'poster','waiting','2020-01-02','2019-01-01'); INSERT INTO scrape_task(id,media_id,status,ingest_run_id,ingest_step_id,generation,available_at,created_at) VALUES(41,10,'waiting',20,31,1,'2020-01-01','2020-01-03'); INSERT INTO transcode_task(id,file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation,created_at) VALUES(42,'f',10,'waiting','pretranscode',20,32,1,'2020-01-04')`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestClaimEligibleUncertainCommitReconcilesFullPayloadOnce(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,max_attempts) VALUES(40,10,20,30,1,'poster','waiting',4)`)
	if err != nil {
		t.Fatal(err)
	}
	lost := errors.New("response lost")
	calls := 0
	p, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "worker", Registry: NewCapabilityMatrix([]string{"poster"}), afterCommit: func() error { calls++; return lost }})
	if err != nil || p == nil || p.QueueID != 40 || p.MediaID != 10 || p.RunID.Int64 != 20 || p.StepID.Int64 != 30 || p.Generation.Int64 != 1 || p.Attempts != 1 || p.MaxAttempts != 4 || p.LeaseUntil.IsZero() {
		t.Fatalf("payload=%+v err=%v", p, err)
	}
	if calls != 1 {
		t.Fatalf("afterCommit calls=%d", calls)
	}
	var running int
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE status='running'`).Scan(&running)
	if running != 1 {
		t.Fatalf("running=%d", running)
	}
}

func TestClaimEligibleUncertainCommitReconcilesLegacyGenerationZero(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,publication_state) VALUES(10,1,'f','video','processing'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,max_attempts) VALUES(40,10,NULL,NULL,0,'subtitle','waiting',4)`)
	if err != nil {
		t.Fatal(err)
	}
	lost := errors.New("response lost")
	p, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "subtitle", Owner: "worker", Registry: NewCapabilityMatrix([]string{"subtitle"}), afterCommit: func() error { return lost }})
	if err != nil || p == nil || p.QueueID != 40 || p.MediaID != 10 || p.RunID.Valid || p.StepID.Valid || p.Generation.Int64 != 0 || p.TaskType != "subtitle" || p.Attempts != 1 || p.MaxAttempts != 4 || p.LeaseUntil.IsZero() {
		t.Fatalf("payload=%+v err=%v", p, err)
	}
	var status, owner string
	if err = db.QueryRow(`SELECT status,lease_owner FROM post_ingest_task WHERE id=40`).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner != p.Owner {
		t.Fatalf("status=%s owner=%s payload=%+v", status, owner, p)
	}
}

func TestClaimEligibleUncertainCommitReconcilesPosterRepair(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state,published_at) VALUES(10,1,'f','video',1,'published',CURRENT_TIMESTAMP); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'repair','published','{}',2); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,max_attempts) VALUES(40,10,20,NULL,1,'poster_repair','waiting',4)`)
	if err != nil {
		t.Fatal(err)
	}
	lost := errors.New("response lost")
	p, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster_repair", Owner: "worker", Registry: NewCapabilityMatrix([]string{"poster_repair"}), afterCommit: func() error { return lost }})
	if err != nil || p == nil || p.QueueID != 40 || p.MediaID != 10 || p.RunID.Int64 != 20 || p.StepID.Valid || p.Generation.Int64 != 1 || p.TaskType != "poster_repair" || p.Attempts != 1 || p.MaxAttempts != 4 || p.LeaseUntil.IsZero() {
		t.Fatalf("payload=%+v err=%v", p, err)
	}
	var status, owner string
	if err = db.QueryRow(`SELECT status,lease_owner FROM post_ingest_task WHERE id=40`).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner != p.Owner {
		t.Fatalf("status=%s owner=%s payload=%+v", status, owner, p)
	}
}

func TestClaimEligibilityStatusDependencyMatrixAllFamilies(t *testing.T) {
	cases := []struct {
		name, run, media   string
		published          bool
		required           bool
		depKind, depStatus string
		want               bool
	}{
		{"required processing", "processing", "processing", false, true, "", "", true}, {"required failed", "failed", "processing", false, true, "", "", false}, {"required cancelled", "cancelled", "processing", false, true, "", "", false},
		{"optional published independent", "published", "published", true, false, "media_visible", "", true}, {"optional degraded independent", "degraded", "degraded", true, false, "media_visible", "", true},
		{"optional processing preserve visibility", "processing", "published", true, false, "media_visible", "", true}, {"optional processing degraded visibility", "processing", "degraded", true, false, "media_visible", "", true}, {"optional processing not yet visible", "processing", "processing", false, false, "media_visible", "", false},
		{"optional failed", "failed", "published", true, false, "media_visible", "", false}, {"optional cancelled", "cancelled", "published", true, false, "media_visible", "", false},
		{"optional degraded explicit failed dep", "degraded", "degraded", true, false, "success", "failed", false}, {"optional degraded explicit done dep", "degraded", "degraded", true, false, "success", "done", true},
	}
	for _, family := range []QueueFamily{QueuePostIngest, QueueScrape, QueuePrepare} {
		for _, tc := range cases {
			t.Run(string(family)+"/"+tc.name, func(t *testing.T) {
				db := openEligibilityDB(t)
				published := "NULL"
				if tc.published {
					published = "CURRENT_TIMESTAMP"
				}
				required := 0
				if tc.required {
					required = 1
				}
				typ := map[QueueFamily]string{QueuePostIngest: "thumbnail", QueueScrape: "scrape", QueuePrepare: "prepare"}[family]
				q := fmt.Sprintf(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state,published_at) VALUES(10,1,'f','video',1,'%s',%s); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','%s','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(29,20,10,1,'poster',1,'%s'),(30,20,10,1,'%s',%d,'waiting');`, tc.media, published, tc.run, func() string {
					if tc.depStatus == "" {
						return "done"
					}
					return tc.depStatus
				}(), typ, required)
				if tc.depKind == "media_visible" {
					q += `INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(31,20,10,1,'media_visible',0,'done'); INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(30,31,'success');`
				} else if tc.depKind == "success" {
					q += `INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(30,29,'success');`
				}
				switch family {
				case QueuePostIngest:
					q += `INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'thumbnail','waiting');`
				case QueueScrape:
					q += `INSERT INTO scrape_task(id,media_id,status,ingest_run_id,ingest_step_id,generation) VALUES(40,10,'waiting',20,30,1);`
				case QueuePrepare:
					q += `INSERT INTO transcode_task(id,file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation) VALUES(40,'f',10,'waiting','pretranscode',20,30,1);`
				}
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
				got, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: family, TaskType: typ, Owner: "worker", Registry: NewCapabilityMatrix([]string{typ})})
				if err != nil {
					t.Fatal(err)
				}
				if (got != nil) != tc.want {
					t.Fatalf("claim=%+v want=%v", got, tc.want)
				}
			})
		}
	}
}

func TestCommunityAbsentPrepareTableRequiredAvailabilityMatrix(t *testing.T) {
	for _, advertised := range []bool{false, true} {
		t.Run(fmt.Sprintf("advertised=%v", advertised), func(t *testing.T) {
			db := openEligibilityDB(t)
			dropEnterprisePrepareTablesIfPresent(t, db)
			if _, err := db.Exec(`DROP TABLE transcode_task`); err != nil {
				t.Fatal(err)
			}
			_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'poster','waiting')`)
			if err != nil {
				t.Fatal(err)
			}
			steps := []string{"poster"}
			if advertised {
				steps = append(steps, "prepare")
			}
			p, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "worker", Registry: NewCapabilityMatrix(steps)})
			if advertised {
				if err == nil || !strings.Contains(err.Error(), "missing table") {
					t.Fatalf("payload=%+v err=%v", p, err)
				}
			} else if err != nil || p == nil {
				t.Fatalf("payload=%+v err=%v", p, err)
			}
		})
	}
}

func TestSelectFamilyCandidateUsesNormalizedDueOrdering(t *testing.T) {
	for _, family := range []QueueFamily{QueuePostIngest, QueueScrape, QueuePrepare} {
		t.Run(string(family), func(t *testing.T) {
			db := openEligibilityDB(t)
			typ := map[QueueFamily]string{QueuePostIngest: "poster", QueueScrape: "scrape", QueuePrepare: "prepare"}[family]
			_, err := db.Exec(fmt.Sprintf(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'),(11,1,'g','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2),(21,11,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'%[1]s',1,'waiting'),(31,21,11,1,'%[1]s',1,'waiting');`, typ))
			if err != nil {
				t.Fatal(err)
			}
			switch family {
			case QueuePostIngest:
				_, err = db.Exec(`INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,available_at,created_at) VALUES(40,10,20,30,1,'poster','waiting','2020-01-02','2019-01-01'),(41,11,21,31,1,'poster','waiting','2020-01-01','2020-01-01')`)
			case QueueScrape:
				_, err = db.Exec(`INSERT INTO scrape_task(id,media_id,status,ingest_run_id,ingest_step_id,generation,available_at,created_at) VALUES(40,10,'waiting',20,30,1,'2020-01-02','2019-01-01'),(41,11,'waiting',21,31,1,'2020-01-01','2020-01-01')`)
			case QueuePrepare:
				_, err = db.Exec(`INSERT INTO transcode_task(id,file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation,created_at) VALUES(40,'f',10,'waiting','pretranscode',20,30,1,'2020-01-02'),(41,'g',11,'waiting','pretranscode',21,31,1,'2020-01-01')`)
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: family, TaskType: typ, Owner: "worker", Registry: NewCapabilityMatrix([]string{typ})})
			if err != nil || p == nil || p.QueueID != 41 {
				t.Fatalf("winner=%+v err=%v", p, err)
			}
		})
	}
}

func TestLinkedClaimEligibilityRejectsPartialLegacyIdentityAllFamilies(t *testing.T) {
	for _, family := range []QueueFamily{QueuePostIngest, QueueScrape, QueuePrepare} {
		t.Run(string(family), func(t *testing.T) {
			db := openEligibilityDB(t)
			_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type) VALUES(10,1,'f','video')`)
			if err != nil {
				t.Fatal(err)
			}
			switch family {
			case QueuePostIngest:
				_, err = db.Exec(`INSERT INTO post_ingest_task(id,media_id,generation,task_type,status) VALUES(40,10,1,'poster','waiting')`)
			case QueueScrape:
				_, err = db.Exec(`INSERT INTO scrape_task(id,media_id,status,generation) VALUES(40,10,'waiting',1)`)
			case QueuePrepare:
				_, err = db.Exec(`PRAGMA ignore_check_constraints=ON; INSERT INTO transcode_task(id,file_id,status,task_type,generation) VALUES(40,'f','waiting','pretranscode',1)`)
			}
			if err != nil {
				t.Fatal(err)
			}
			typ := map[QueueFamily]string{QueuePostIngest: "poster", QueueScrape: "scrape", QueuePrepare: "prepare"}[family]
			p, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: family, TaskType: typ, Owner: "w", Registry: NewCapabilityMatrix([]string{typ})})
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Fatalf("partial claimed=%+v", p)
			}
		})
	}
}

// TestAgingEffectivePriorityClaimOrder verifies that aging boosts a lower
// source-class task past a younger higher-class task.  Without aging, the
// higher priority always wins.  With aging, the older lower-priority task
// accrues enough effective priority to be claimed first.
func TestAgingEffectivePriorityClaimOrder(t *testing.T) {
	db := openEligibilityDB(t)
	now := "datetime(CURRENT_TIMESTAMP)"
	old := "datetime(CURRENT_TIMESTAMP, '-36000 seconds')" // 10 hours ago
	_, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
		INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state)
			VALUES(10,1,'f10','video',1,'processing'),(11,1,'f11','video',1,'processing');
		INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version)
			VALUES(20,10,1,'scan','processing','{}',2),(21,11,1,'scan','processing','{}',2);
		INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status)
			VALUES(30,20,10,1,'encrypt',1,'waiting'),(31,21,11,1,'encrypt',1,'waiting');
		INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,
			available_at,created_at,priority)
			VALUES
			 (40,10,20,30,1,'encrypt','waiting',` + now + `,` + now + `,50),
			 (41,11,21,31,1,'encrypt','waiting',` + old + `,` + old + `,0);
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	policy := scheduler.PolicyDefaults()
	registry := NewCapabilityMatrix([]string{"encrypt"})
	got, err := ClaimEligible(context.Background(), db, ClaimRequest{
		Family: QueuePostIngest, TaskType: "encrypt", Owner: "worker", Registry: registry,
		SchedulerPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got == nil {
		t.Fatal("expected a claim")
	}
	// With aging (interval=300s, step=1): task 41 is 10h old → aging_boost=120
	// Task 41 effective=120 beats task 40 raw priority=50.
	if got.QueueID != 41 {
		t.Fatalf("aging claim: got queue %d, want 41 (older task wins via aging boost)", got.QueueID)
	}
}

// TestEffectivePriorityNoAgingCap verifies that aging has no cap and can grow
// unbounded, guaranteeing that every source class eventually makes progress.
func TestEffectivePriorityNoAgingCap(t *testing.T) {
	db := openEligibilityDB(t)
	now := "datetime(CURRENT_TIMESTAMP)"
	veryOld := "datetime(CURRENT_TIMESTAMP, '-31536000 seconds')" // ~1 year ago
	_, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
		INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state)
			VALUES(10,1,'f10','video',1,'processing'),(11,1,'f11','video',1,'processing');
		INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version)
			VALUES(20,10,1,'scan','processing','{}',2),(21,11,1,'scan','processing','{}',2);
		INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status)
			VALUES(30,20,10,1,'encrypt',1,'waiting'),(31,21,11,1,'encrypt',1,'waiting');
		INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,
			available_at,created_at,priority,source_class,base_priority)
			VALUES
			 (40,10,20,30,1,'encrypt','waiting',` + now + `,` + now + `,0,400,400),
			 (41,11,21,31,1,'encrypt','waiting',` + veryOld + `,` + veryOld + `,0,100,100);
	`)
	if err != nil {
		t.Fatal(err)
	}
	policy := scheduler.PolicyDefaults()
	registry := NewCapabilityMatrix([]string{"encrypt"})
	got, err := ClaimEligible(context.Background(), db, ClaimRequest{
		Family: QueuePostIngest, TaskType: "encrypt", Owner: "worker", Registry: registry,
		SchedulerPolicy: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a claim")
	}
	// Task 41 (source_class=100, 1 year old) should overtake task 40
	// (source_class=400, brand new) because aging is uncapped.
	if got.QueueID != 41 {
		t.Fatalf("uncapped aging claim: got queue %d, want 41 (old task wins via uncapped aging)", got.QueueID)
	}
}

// TestClaimOrderStableTieBreakers verifies that when effective priorities are
// equal, stable tie-breaking uses available_at, created_at, and ID.
func TestClaimOrderStableTieBreakers(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`
		INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
		INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state)
			VALUES(10,1,'f10','video',1,'processing'),(11,1,'f11','video',1,'processing');
		INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version)
			VALUES(20,10,1,'scan','processing','{}',2),(21,11,1,'scan','processing','{}',2);
		INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status)
			VALUES(30,20,10,1,'encrypt',1,'waiting'),(31,21,11,1,'encrypt',1,'waiting');
		-- Same source_class and base_priority, but different available_at.
		-- Task 41 has earlier available_at → should be claimed first.
		INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,
			available_at,created_at,priority,source_class,base_priority)
			VALUES
			 (40,10,20,30,1,'encrypt','waiting','2020-01-02T00:00:00','2020-01-01T00:00:00',0,300,300),
			 (41,11,21,31,1,'encrypt','waiting','2020-01-01T00:00:00','2020-01-01T00:00:00',0,300,300);
	`)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewCapabilityMatrix([]string{"encrypt"})
	policy := scheduler.PolicyDefaults()
	got, err := ClaimEligible(context.Background(), db, ClaimRequest{
		Family: QueuePostIngest, TaskType: "encrypt", Owner: "worker", Registry: registry,
		SchedulerPolicy: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a claim")
	}
	// Task 41 should win because available_at is earlier (stable tie-breaker).
	if got.QueueID != 41 {
		t.Fatalf("stable tie claim: got queue %d, want 41 (earlier available_at wins equal priority)", got.QueueID)
	}
}

// ============================================================================
// ClaimWithAdmission tests - RED phase
// ============================================================================

func seedAdmissionPolicy(t *testing.T, db *sql.DB, concurrency map[string]int, resources map[string]int) {
	t.Helper()
	concurrencyJSON := "{}"
	if len(concurrency) > 0 {
		parts := make([]string, 0, len(concurrency))
		for k, v := range concurrency {
			parts = append(parts, fmt.Sprintf("%q:%d", k, v))
		}
		concurrencyJSON = "{" + strings.Join(parts, ",") + "}"
	}
	resourcesJSON := "{}"
	if len(resources) > 0 {
		parts := make([]string, 0, len(resources))
		for k, v := range resources {
			parts = append(parts, fmt.Sprintf("%q:%d", k, v))
		}
		resourcesJSON = "{" + strings.Join(parts, ",") + "}"
	}
	policyJSON := fmt.Sprintf(`{"type_concurrency":%s,"resource_capacity":%s,"provider_capacity":{},"aging_interval_sec":300,"aging_step":1,"run_now_amount":100,"run_now_ttl_sec":600}`, concurrencyJSON, resourcesJSON)
	if _, err := db.Exec(`INSERT INTO scheduler_policy_revision(schema_version,policy_json,author,reason,validation_hash,is_active,activated_at) VALUES(1,?,'test','admission test','hash',1,CURRENT_TIMESTAMP)`, policyJSON); err != nil {
		t.Fatalf("insert policy: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO scheduler_control(task_type,state) VALUES('poster','running'),('thumbnail','running'),('encrypt','running'),('ai_analysis','running')`); err != nil {
		t.Fatalf("insert control: %v", err)
	}
}

func TestClaimWithAdmissionCreatesReservation(t *testing.T) {
	db := openEligibilityDB(t)
	seedAdmissionPolicy(t, db, map[string]int{"poster": 5}, map[string]int{"cpu": 10})
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'poster','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["poster"] = 5
	req := ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "worker", Registry: NewCapabilityMatrix([]string{"poster"}), SchedulerPolicy: &policy}
	payload, result, blockers, err := ClaimWithAdmission(context.Background(), db, req)
	if err != nil {
		t.Fatalf("ClaimWithAdmission: %v", err)
	}
	if payload == nil {
		t.Fatalf("expected payload, got nil (blockers=%+v)", blockers)
	}
	if result == nil {
		t.Fatalf("expected AdmissionResult, got nil")
	}
	if result.ReservationID == 0 {
		t.Fatal("expected non-zero reservation ID")
	}
	if result.ExecutionID == "" {
		t.Fatal("expected non-empty execution ID")
	}
	if result.QueueID != 40 {
		t.Fatalf("expected queue 40, got %d", result.QueueID)
	}
	// Verify reservation was created in DB
	var resCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scheduler_reservation WHERE execution_id=? AND status='active'`, result.ExecutionID).Scan(&resCount); err != nil {
		t.Fatal(err)
	}
	if resCount != 1 {
		t.Fatalf("expected 1 active reservation, got %d", resCount)
	}
}

func TestClaimWithAdmissionTypeLimitBlocks(t *testing.T) {
	db := openEligibilityDB(t)
	seedAdmissionPolicy(t, db, map[string]int{"poster": 1}, map[string]int{"cpu": 10})
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'),(11,1,'g','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2),(21,11,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'),(31,21,11,1,'poster',1,'waiting'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'poster','waiting'),(41,11,21,31,1,'poster','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["poster"] = 1
	req := ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "worker", Registry: NewCapabilityMatrix([]string{"poster"}), SchedulerPolicy: &policy}

	// First claim should succeed
	payload1, result1, blockers1, err1 := ClaimWithAdmission(context.Background(), db, req)
	if err1 != nil || payload1 == nil || result1 == nil {
		t.Fatalf("first claim: err=%v payload=%+v result=%+v blockers=%+v", err1, payload1, result1, blockers1)
	}

	// Second claim should be blocked (concurrency=1)
	payload2, result2, blockers2, err2 := ClaimWithAdmission(context.Background(), db, req)
	if err2 != nil {
		t.Fatalf("second claim: %v", err2)
	}
	if payload2 != nil || result2 != nil {
		t.Fatalf("second claim should be nil, got payload=%+v result=%+v", payload2, result2)
	}
	if len(blockers2) == 0 {
		t.Fatal("expected type limit blockers")
	}
	foundTypeLimit := false
	for _, b := range blockers2 {
		if strings.Contains(strings.ToLower(b.Reason), "concurrency") {
			foundTypeLimit = true
		}
	}
	if !foundTypeLimit {
		t.Fatalf("expected concurrency blocker, got %+v", blockers2)
	}
}

func TestClaimWithAdmissionResourceBudgetBlocks(t *testing.T) {
	db := openEligibilityDB(t)
	seedAdmissionPolicy(t, db, map[string]int{"encrypt": 5}, map[string]int{"cpu": 1})
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'),(11,1,'g','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2),(21,11,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'encrypt',1,'waiting'),(31,21,11,1,'encrypt',1,'waiting'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'encrypt','waiting'),(41,11,21,31,1,'encrypt','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	policy := scheduler.PolicyDefaults()
	policy.TypeConcurrency["encrypt"] = 5
	policy.ResourceCapacity[scheduler.CPU] = 1
	req := ClaimRequest{Family: QueuePostIngest, TaskType: "encrypt", Owner: "worker", Registry: NewCapabilityMatrix([]string{"encrypt"}), SchedulerPolicy: &policy}

	// First claim succeeds
	payload1, _, _, err1 := ClaimWithAdmission(context.Background(), db, req)
	if err1 != nil || payload1 == nil {
		t.Fatalf("first encrypt claim: err=%v payload=%+v", err1, payload1)
	}

	// Second claim blocked by CPU budget (encrypt uses 1 CPU, capacity=1)
	payload2, _, blockers2, err2 := ClaimWithAdmission(context.Background(), db, req)
	if err2 != nil {
		t.Fatalf("second encrypt claim: %v", err2)
	}
	if payload2 != nil {
		t.Fatalf("second encrypt claim should be nil, got %+v", payload2)
	}
	foundResource := false
	for _, b := range blockers2 {
		if strings.Contains(strings.ToLower(b.Reason), "resource") {
			foundResource = true
		}
	}
	if !foundResource {
		t.Fatalf("expected resource blocker, got %+v", blockers2)
	}
}

func TestClaimWithAdmissionNoPolicyFallsThrough(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',2); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,10,1,'poster',1,'waiting'); INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(40,10,20,30,1,'poster','waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	req := ClaimRequest{Family: QueuePostIngest, TaskType: "poster", Owner: "worker", Registry: NewCapabilityMatrix([]string{"poster"})}
	payload, result, _, err := ClaimWithAdmission(context.Background(), db, req)
	if err != nil {
		t.Fatalf("ClaimWithAdmission without policy: %v", err)
	}
	if payload == nil {
		t.Fatal("expected payload with nil policy")
	}
	if result != nil {
		t.Fatalf("expected nil AdmissionResult without policy, got %+v", result)
	}
}

func TestScrapeLegacyGenerationZeroEligibleAndLinkedProtected(t *testing.T) {
	db := openEligibilityDB(t)
	if _, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing');
INSERT INTO scrape_task(id,media_id,status,generation) VALUES(40,10,'waiting',0),(41,10,'waiting',1)`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   int64
		want bool
	}{{40, true}, {41, false}} {
		var got bool
		if err := db.QueryRow(`SELECT `+LinkedClaimEligibilitySQL("q")+` FROM scrape_task q WHERE q.id=?`, tc.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("task %d eligible=%v want=%v", tc.id, got, tc.want)
		}
	}
	claim, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueueScrape, TaskType: "scrape", Owner: "legacy", QueueID: func() *int64 { v := int64(40); return &v }(), Registry: NewCapabilityMatrix([]string{"scrape"})})
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	if claim.RunID.Valid || claim.StepID.Valid || !claim.Generation.Valid || claim.Generation.Int64 != 0 {
		t.Fatalf("legacy claim identity=%+v", claim)
	}
}
