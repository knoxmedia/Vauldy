package taskcontrol

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"knox-media/internal/publication"
	"knox-media/internal/store"
)

func TestScrapeTaskControllerResetAndRemoveLegacyGenerationZero(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scrape-control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
INSERT INTO library(name,type,path) VALUES('l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type) VALUES(1,1,'f','video');
INSERT INTO scrape_task(id,media_id,status,generation,fail_count) VALUES(10,1,'failed',0,3),(11,1,'cancelled',0,1);
INSERT INTO scrape_history(task_id,media_id,status,message) VALUES(11,1,'failed','kept');
INSERT INTO scrape_effect_commit(task_id,attempt,retry_round,generation,manifest_json,manifest_digest) VALUES(11,1,0,0,'{}','digest');
`); err != nil {
		t.Fatal(err)
	}
	controller := NewScrapeTaskController(db)
	ctx := context.Background()
	if err := controller.Reset(ctx, ExternalOperationRequest{ID: 10, Identity: "scrape_task:10", ActorID: 1}); err != nil {
		t.Fatal(err)
	}
	var status string
	var generation *int64
	if err := db.QueryRow(`SELECT status,generation FROM scrape_task WHERE id=10`).Scan(&status, &generation); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" {
		t.Fatalf("reset status=%s", status)
	}
	if err := controller.Remove(ctx, ExternalOperationRequest{ID: 11, Identity: "scrape_task:11", ActorID: 1}); err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{"queue": `SELECT COUNT(*) FROM scrape_task WHERE id=11`, "effect": `SELECT COUNT(*) FROM scrape_effect_commit WHERE task_id=11`, "history": `SELECT COUNT(*) FROM scrape_history WHERE task_id=11`} {
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		want := 0
		if name == "history" {
			want = 1
		}
		if n != want {
			t.Errorf("%s count=%d want=%d", name, n, want)
		}
	}
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_control_audit WHERE task_identity IN ('scrape_task:10','scrape_task:11')`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatal(fmt.Sprintf("audit count=%d", audits))
	}
}

func TestScrapeTaskControllerProtectsLinkedGeneration(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scrape-linked.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation) VALUES(1,1,'f','video',1); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,1,1,'scan','processing','{}',1); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(30,20,1,1,'scrape',0,'failed'); INSERT INTO scrape_task(id,media_id,status,generation,ingest_run_id,ingest_step_id) VALUES(10,1,'failed',1,20,30)`); err != nil {
		t.Fatal(err)
	}
	if err := NewScrapeTaskController(db).Remove(context.Background(), ExternalOperationRequest{ID: 10, Identity: "scrape_task:10"}); err == nil {
		t.Fatal("linked scrape remove unexpectedly succeeded")
	}
}

func TestScrapeTaskControllerLinkedLifecycleControls(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scrape-linked-controls.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
INSERT INTO library(name,type,path) VALUES('l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'f','video',2,'published'),(2,1,'f2','video',1,'published');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version,superseded_at,superseded_by_generation) VALUES(20,1,1,'scan','published','{}',1,CURRENT_TIMESTAMP,2),(21,1,2,'scan','published','{}',1,NULL,NULL),(22,2,1,'scan','published','{}',1,NULL,NULL);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,retry_round) VALUES(30,20,1,1,'scrape',0,'cancelled',0,0),(31,21,1,2,'scrape',0,'failed',3,0),(32,22,2,1,'scrape',0,'running',1,0);
INSERT INTO scrape_task(id,media_id,status,generation,ingest_run_id,ingest_step_id,fail_count,retry_round,lease_owner,message) VALUES(10,1,'cancelled',1,20,30,0,0,NULL,''),(11,1,'failed',2,21,31,3,0,NULL,''),(12,2,'running',1,22,32,1,0,'scrape/test','');
`); err != nil {
		t.Fatal(err)
	}
	controller := NewScrapeTaskController(db, publication.NewCapabilityMatrix([]string{"scrape"}))
	ctx := context.Background()
	if err := controller.Remove(ctx, ExternalOperationRequest{ID: 10, Identity: "scrape_task:10", ActorID: 1, Reason: "stale"}); err != nil {
		t.Fatal(err)
	}
	if err := controller.Reset(ctx, ExternalOperationRequest{ID: 11, Identity: "scrape_task:11", ActorID: 1, Reason: "retry"}); err != nil {
		t.Fatal(err)
	}
	if err := controller.Abort(ctx, ExternalOperationRequest{ID: 12, Identity: "scrape_task:12", ActorID: 1, Reason: "abort"}); err != nil {
		t.Fatal(err)
	}
	var removed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scrape_task WHERE id=10`).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	var resetTask, resetStep, abortTask, abortStep string
	if err := db.QueryRow(`SELECT q.status,s.status FROM scrape_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE q.id=11`).Scan(&resetTask, &resetStep); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT q.status,s.status FROM scrape_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE q.id=12`).Scan(&abortTask, &abortStep); err != nil {
		t.Fatal(err)
	}
	if removed != 0 || resetTask != "waiting" || resetStep != "waiting" || abortTask != "cancelled" || abortStep != "cancelled" {
		t.Fatalf("removed=%d reset=%s/%s abort=%s/%s", removed, resetTask, resetStep, abortTask, abortStep)
	}
}
