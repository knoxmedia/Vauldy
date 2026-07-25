package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateIngestPublicationRebuildsOptionalRetryAuditPrepareFamily(t *testing.T) {
	if !storeEnterprisePrepareReady(t) {
		t.Skip("enterprise prepare tables unavailable in community build")
	}
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json) VALUES(1,20,1,'scan','published','{}'); INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts) VALUES(70,1,20,1,'scrape',0,'failed',3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE media_ingest_optional_retry_audit; CREATE TABLE media_ingest_optional_retry_audit(
 id INTEGER PRIMARY KEY AUTOINCREMENT,media_id INTEGER NOT NULL,run_id INTEGER NOT NULL,step_id INTEGER NOT NULL,generation INTEGER NOT NULL,
 task_family TEXT NOT NULL CHECK(task_family IN ('post_ingest','scrape')),task_type TEXT NOT NULL,actor_id INTEGER NOT NULL,reason TEXT NOT NULL,
 previous_queue_status TEXT NOT NULL,previous_step_status TEXT NOT NULL,previous_attempts INTEGER NOT NULL,previous_queue_error TEXT NOT NULL,previous_step_error TEXT NOT NULL,
 retry_round INTEGER NOT NULL CHECK(retry_round > 0),created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,
 FOREIGN KEY(run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation) ON DELETE CASCADE,
 FOREIGN KEY(step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation) ON DELETE CASCADE,
 UNIQUE(step_id,retry_round)); INSERT INTO media_ingest_optional_retry_audit(media_id,run_id,step_id,generation,task_family,task_type,actor_id,reason,previous_queue_status,previous_step_status,previous_attempts,previous_queue_error,previous_step_error,retry_round) VALUES(20,1,70,1,'scrape','scrape',1,'legacy','failed','failed',3,'q','s',1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_optional_retry_audit(media_id,run_id,step_id,generation,task_family,task_type,actor_id,reason,previous_queue_status,previous_step_status,previous_attempts,previous_queue_error,previous_step_error,retry_round) VALUES(20,1,70,1,'prepare','prepare',1,'prepare retry','failed','failed',3,'q','s',2)`); err != nil {
		t.Fatal(err)
	}
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='media_ingest_optional_retry_audit'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "'prepare'") {
		t.Fatalf("prepare family missing from %s", sqlText)
	}
}

func TestOpenSQLiteEnsuresPrepareRetryRoundColumns(t *testing.T) {
	if !storeEnterprisePrepareReady(t) {
		t.Skip("enterprise prepare tables unavailable in community build")
	}
	path := filepath.Join(t.TempDir(), "prepare-retry-round.sqlite")
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"transcode_task", "pretranscode_rendition_job"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name='retry_round'`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s.retry_round n=%d err=%v", table, n, err)
		}
	}
}




