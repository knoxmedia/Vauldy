package publication

import (
	"context"
	"database/sql"
	"knox-media/internal/store"
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
	db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=10; UPDATE media_ingest_run SET status='published' WHERE id=20`)
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
