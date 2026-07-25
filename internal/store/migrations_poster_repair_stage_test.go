package store

import (
	"context"
	"testing"
)

func TestPublicationMigrationCreatesPosterRepairStageIdentity(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='poster_repair_stage'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(880,20,1,'repair','published','{}',2);INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,lease_owner) VALUES(881,20,880,NULL,1,'poster_repair','running',2,'repair-owner');INSERT INTO poster_repair_stage(stage_id,queue_id,media_id,run_id,generation,owner_token,attempt,source_fingerprint,state,staged_path,hashes_sizes_json) VALUES('repair-stage',881,20,880,1,'repair-owner',2,'fp','staged','x','{}')`)
	if err != nil {
		t.Fatalf("repair journal insert: %v ddl=%s", err, ddl)
	}
	if _, err = db.Exec(`INSERT INTO poster_repair_stage(stage_id,queue_id,media_id,run_id,generation,owner_token,attempt,source_fingerprint,state,staged_path,hashes_sizes_json) VALUES('wrong',881,20,880,1,'wrong',2,'fp','staged','x','{}')`); err == nil {
		t.Fatal("duplicate queue repair stage accepted")
	}
}
