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
