package publication

import (
	"context"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestResetInterruptedScrapeTasksFinalizesFailedWithoutValidateAggregateGap(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scrape-reset-finalize.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
INSERT INTO library(name,type,path) VALUES('scrape','video','/scrape');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'scrape-media','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'scan','processing','{}',3);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts) VALUES
 (11,10,1,1,'poster',1,'done',1,3),
 (12,10,1,1,'scrape',1,'running',3,3);
INSERT INTO scrape_task(id,media_id,status,fail_count,lease_owner,ingest_run_id,ingest_step_id,generation) VALUES(20,1,'running',3,'dead',10,12,1);
`); err != nil {
		t.Fatal(err)
	}

	barrier, aggregates := 0, 0
	SetRetirementBarrierProbeForTest(func(id int64) {
		if id == 10 {
			barrier++
		}
	})
	SetAggregateProbeForTest(func(id int64) {
		if id == 10 {
			aggregates++
		}
	})
	t.Cleanup(func() {
		ClearRetirementBarrierProbeForTest()
		ClearAggregateProbeForTest()
	})

	if err := ResetInterruptedScrapeTasks(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	var taskStatus, stepStatus, runStatus, pub string
	if err := db.QueryRow(`SELECT status FROM scrape_task WHERE id=20`).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=12`).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=10`).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=1`).Scan(&pub); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "failed" || stepStatus != "failed" {
		t.Fatalf("exhausted recovery left task=%q step=%q", taskStatus, stepStatus)
	}
	if runStatus != "failed" || pub != "failed" {
		t.Fatalf("finalize gap: run=%q media=%q before ValidateAggregate", runStatus, pub)
	}
	var failed, waiting, running, all int
	if err := db.QueryRow(`SELECT failed_count,waiting_count,running_count,all_terminal FROM media_plan_completion WHERE run_id=10`).Scan(&failed, &waiting, &running, &all); err != nil {
		t.Fatalf("plan completion missing after scrape reset: %v", err)
	}
	if failed != 1 || waiting != 0 || running != 0 || all != 1 {
		t.Fatalf("plan failed=%d waiting=%d running=%d all=%d", failed, waiting, running, all)
	}
	if barrier != 1 || aggregates != 1 {
		t.Fatalf("expected one finalize barrier=%d aggregate=%d", barrier, aggregates)
	}
}

func TestResetInterruptedScrapeTasksRecomputesWaitingWithoutAggregate(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scrape-reset-waiting.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
INSERT INTO library(name,type,path) VALUES('scrape','video','/scrape');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'scrape-wait','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'scan','processing','{}',3);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts) VALUES
 (11,10,1,1,'poster',1,'done',1,3),
 (12,10,1,1,'scrape',0,'running',1,3);
INSERT INTO scrape_task(id,media_id,status,fail_count,lease_owner,ingest_run_id,ingest_step_id,generation) VALUES(20,1,'running',1,'dead',10,12,1);
`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := RecomputePlanCompletionTx(context.Background(), tx, 10); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	aggregates := 0
	SetAggregateProbeForTest(func(id int64) {
		if id == 10 {
			aggregates++
		}
	})
	t.Cleanup(ClearAggregateProbeForTest)

	if err := ResetInterruptedScrapeTasks(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var waiting, running int
	if err := db.QueryRow(`SELECT waiting_count,running_count FROM media_plan_completion WHERE run_id=10`).Scan(&waiting, &running); err != nil {
		t.Fatal(err)
	}
	if waiting != 1 || running != 0 {
		t.Fatalf("waiting reset counts waiting=%d running=%d", waiting, running)
	}
	if aggregates != 0 {
		t.Fatalf("waiting scrape reset should not aggregate: %d", aggregates)
	}
}

func TestResetInterruptedScrapeTasksCancelsStaleLinkedRows(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scrape-reset-stale.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
INSERT INTO library(name,type,path) VALUES('scrape','video','/scrape');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'stale','video',2,'published');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version,superseded_at,superseded_by_generation) VALUES(10,1,1,'scan','published','{}',3,CURRENT_TIMESTAMP,2),(20,1,2,'scan','published','{}',3,NULL,NULL);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(11,10,1,1,'scrape',0,'waiting');
INSERT INTO scrape_task(id,media_id,status,fail_count,ingest_run_id,ingest_step_id,generation) VALUES(12,1,'waiting',0,10,11,1);
`); err != nil {
		t.Fatal(err)
	}
	if err := ResetInterruptedScrapeTasks(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var task, step, media string
	if err := db.QueryRow(`SELECT q.status,s.status,m.publication_state FROM scrape_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id JOIN media m ON m.id=q.media_id WHERE q.id=12`).Scan(&task, &step, &media); err != nil {
		t.Fatal(err)
	}
	if task != "cancelled" || step != "cancelled" || media != "published" {
		t.Fatalf("task=%s step=%s media=%s", task, step, media)
	}
}
