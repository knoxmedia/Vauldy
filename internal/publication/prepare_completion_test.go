package publication

import (
	"context"
	"testing"
)

func TestCompletePrepareTxRejectsLinkedPolicyV1StaleOwner(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',1); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,lease_owner) VALUES(30,20,10,1,'prepare',1,'running','new-owner'); INSERT INTO transcode_task(id,file_id,media_id,status,ingest_run_id,ingest_step_id,generation,lease_owner) VALUES(40,'f',10,'running',20,30,1,'new-owner')`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = CompletePrepareTx(context.Background(), tx, PrepareParentIdentity{TaskID: 40, RunID: 20, StepID: 30, MediaID: 10, Generation: 1, Owner: "old-owner"}, true, "")
	if err == nil {
		t.Fatal("linked policy-v1 stale owner accepted")
	}
}

func TestCompletePrepareTxFencesParentIdentity(t *testing.T) {
	db := openEligibilityDB(t)
	_, e := db.Exec(`INSERT INTO library(id,name,type,path)VALUES(1,'l','video','/l');INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state)VALUES(10,1,'f','video',1,'processing');INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json)VALUES(20,10,1,'scan','processing','{}');INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,lease_owner)VALUES(30,20,10,1,'prepare',1,'running','owner');INSERT INTO transcode_task(id,file_id,media_id,status,ingest_run_id,ingest_step_id,generation,lease_owner)VALUES(40,'f',10,'running',20,30,1,'owner')`)
	if e != nil {
		t.Fatal(e)
	}
	bad := []PrepareParentIdentity{{TaskID: 40, RunID: 20, StepID: 30, MediaID: 10, Generation: 1, Owner: "other"}, {TaskID: 40, RunID: 20, StepID: 30, MediaID: 10, Generation: 2, Owner: "owner"}, {TaskID: 40, RunID: 20, StepID: 31, MediaID: 10, Generation: 1, Owner: "owner"}}
	for _, p := range bad {
		tx, _ := db.Begin()
		if e := CompletePrepareTx(context.Background(), tx, p, true, ""); e == nil {
			t.Fatalf("accepted %+v", p)
		}
		_ = tx.Rollback()
	}
	var task, step string
	_ = db.QueryRow(`SELECT t.status,s.status FROM transcode_task t JOIN media_ingest_step s ON s.id=30 WHERE t.id=40`).Scan(&task, &step)
	if task != "running" || step != "running" {
		t.Fatalf("%s/%s", task, step)
	}
}

func TestCompletePrepareTxFinalizesBarrierPlanAndAggregate(t *testing.T) {
	db := openEligibilityDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l'); INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing'); INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',3); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,lease_owner) VALUES(30,20,10,1,'prepare',1,'running',1,'owner'); INSERT INTO transcode_task(id,file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation,retry_round,lease_owner) VALUES(40,'f',10,'running','pretranscode',20,30,1,0,'owner')`)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	SetRetirementBarrierProbeForTest(func(id int64) {
		if id == 20 {
			seen++
		}
	})
	t.Cleanup(ClearRetirementBarrierProbeForTest)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = CompletePrepareTx(context.Background(), tx, PrepareParentIdentity{TaskID: 40, RunID: 20, StepID: 30, MediaID: 10, Generation: 1, RetryRound: 0, Owner: "owner"}, true, ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("retirement barrier calls=%d", seen)
	}
	var all, waiting int
	var task, step, run, pub string
	if err = db.QueryRow(`SELECT all_terminal,waiting_count FROM media_plan_completion WHERE run_id=20`).Scan(&all, &waiting); err != nil {
		t.Fatalf("plan completion missing: %v", err)
	}
	if err = db.QueryRow(`SELECT t.status,s.status,r.status,m.publication_state FROM transcode_task t JOIN media_ingest_step s ON s.id=t.ingest_step_id JOIN media_ingest_run r ON r.id=t.ingest_run_id JOIN media m ON m.id=t.media_id WHERE t.id=40`).Scan(&task, &step, &run, &pub); err != nil {
		t.Fatal(err)
	}
	if task != "done" || step != "done" || all != 1 || waiting != 0 {
		t.Fatalf("task=%s step=%s all=%d waiting=%d", task, step, all, waiting)
	}
	if run != "published" || pub != "published" {
		t.Fatalf("aggregate run=%s media=%s", run, pub)
	}
}
