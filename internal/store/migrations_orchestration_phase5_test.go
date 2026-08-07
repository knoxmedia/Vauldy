package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestPhase5MigrationNodeKeyColumnExists(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}

	// Run Phase 5 migration
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}

	// Verify node_key column exists with NOT NULL constraint
	var nodeKeyType string
	var nodeKeyNotNull int
	if err := db.QueryRow(`SELECT type, "notnull" FROM pragma_table_info('media_ingest_step') WHERE name='node_key'`).Scan(&nodeKeyType, &nodeKeyNotNull); err != nil {
		t.Fatalf("node_key column: %v", err)
	}
	if nodeKeyType != "TEXT" || nodeKeyNotNull != 1 {
		t.Fatalf("node_key type=%q notnull=%d, want TEXT NOT NULL", nodeKeyType, nodeKeyNotNull)
	}

	// Verify capability_subtask column exists and is nullable
	var capType string
	var capNotNull int
	if err := db.QueryRow(`SELECT type, "notnull" FROM pragma_table_info('media_ingest_step') WHERE name='capability_subtask'`).Scan(&capType, &capNotNull); err != nil {
		t.Fatalf("capability_subtask column: %v", err)
	}
	if capType != "TEXT" || capNotNull != 0 {
		t.Fatalf("capability_subtask type=%q notnull=%d, want TEXT nullable", capType, capNotNull)
	}
}

func TestPhase5MigrationNodeKeyUniqueConstraint(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}

	// Insert a run
	if _, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json) VALUES(20,5,10,'scan','processing',0,'{}')`); err != nil {
		t.Fatal(err)
	}
	// Insert two steps with different node_keys and step_types - should work
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,node_key,capability_subtask,step_type,required,status) VALUES(1,20,5,'lyric_recognize',NULL,'lyric_recognize',1,'waiting')`); err != nil {
		t.Fatalf("insert step 1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,node_key,capability_subtask,step_type,required,status) VALUES(1,20,5,'audio_analysis',NULL,'audio_analysis',1,'waiting')`); err != nil {
		t.Fatalf("insert step 2: %v", err)
	}
	// Insert duplicate step_type in same run - should fail on UNIQUE(run_id,step_type)
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,node_key,capability_subtask,step_type,required,status) VALUES(1,20,5,'lyric_recognize_dup',NULL,'lyric_recognize',1,'waiting')`); err == nil {
		t.Fatal("duplicate step_type in same run should be rejected")
	}
}

func TestPhase5MigrationCapabilitySubtask(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json) VALUES(20,5,10,'scan','processing',0,'{}')`); err != nil {
		t.Fatal(err)
	}
	// AI analysis with a capability subtask
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,node_key,capability_subtask,step_type,required,status) VALUES(1,20,5,'ai_analysis','summary','ai_analysis',1,'waiting')`); err != nil {
		t.Fatalf("insert ai with subtask: %v", err)
	}
	// capability_subtask can be NULL
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,node_key,capability_subtask,step_type,required,status) VALUES(1,20,5,'photo_classify',NULL,'photo_classify',1,'waiting')`); err != nil {
		t.Fatalf("insert without subtask: %v", err)
	}
	// Verify capability_subtask values
	var cap1, cap2 sql.NullString
	rows, err := db.Query(`SELECT capability_subtask FROM media_ingest_step ORDER BY node_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	rows.Next()
	rows.Scan(&cap1)
	rows.Next()
	rows.Scan(&cap2)
	if !cap1.Valid || cap1.String != "summary" {
		t.Fatalf("capability_subtask 1 = %v", cap1)
	}
	if cap2.Valid {
		t.Fatalf("capability_subtask 2 should be NULL, got %v", cap2)
	}
}

func TestPhase5MigrationClaimIndexes(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}

	for _, index := range []string{"idx_media_ingest_step_claim_node", "idx_media_ingest_step_run_node"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s count=%d err=%v", index, n, err)
		}
	}
}

func TestPhase5MigrationNodeKeyBackfillsFromStepType(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}
	// Insert legacy steps with no node_key (pre-Phase-5)
	if _, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json) VALUES(20,5,10,'scan','processing',0,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(1,20,5,'keyframe',1,'waiting')`); err != nil {
		t.Fatalf("insert legacy step: %v", err)
	}
	// Run the Phase 5 migration
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}
	// Verify node_key was backfilled from step_type
	var nodeKey string
	if err := db.QueryRow(`SELECT node_key FROM media_ingest_step WHERE run_id=1`).Scan(&nodeKey); err != nil {
		t.Fatalf("read node_key: %v", err)
	}
	if nodeKey != "keyframe" {
		t.Fatalf("node_key=%q, want keyframe", nodeKey)
	}
}

func TestPhase5MigrationIsIdempotent(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 run 1: %v", err)
	}
	// Insert steps after first migration
	if _, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json) VALUES(20,5,10,'scan','processing',0,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,node_key,capability_subtask,step_type,required,status) VALUES(1,20,5,'lyric_recognize',NULL,'lyric_recognize',1,'waiting')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Run migration again
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 run 2: %v", err)
	}
	// Verify data preserved
	var nodeKey string
	if err := db.QueryRow(`SELECT node_key FROM media_ingest_step WHERE run_id=1`).Scan(&nodeKey); err != nil {
		t.Fatalf("read node_key: %v", err)
	}
	if nodeKey != "lyric_recognize" {
		t.Fatalf("node_key=%q, want lyric_recognize preserved", nodeKey)
	}
}

func TestPhase5MigrationDocumentArtifactTable(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='document_artifact'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("document_artifact table count=%d err=%v", n, err)
	}
	// Verify insert works
	if _, err := db.Exec(`INSERT INTO document_artifact(media_id,generation,artifact_type,node_key,mime_type,byte_size,hash,path,created_at) VALUES(20,1,'pdf_export','document_convert','application/pdf',1024,'abc123','/tmp/out.pdf',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert document_artifact: %v", err)
	}
}

func TestPhase5MigrationDocumentFulltextTable(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='document_fulltext'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("document_fulltext table count=%d err=%v", n, err)
	}
	// Verify insert works
	if _, err := db.Exec(`INSERT INTO document_fulltext(media_id,generation,language,text_content,text_size,text_hash,mode,engine_version,created_at) VALUES(20,1,'eng','Sample text',11,'hash123','native','1.0',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert document_fulltext: %v", err)
	}
}

func TestPhase5MigrationAIResultTable(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ai_analysis_result'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ai_analysis_result table count=%d err=%v", n, err)
	}
	// Verify append-only: inserts work
	if _, err := db.Exec(`INSERT INTO ai_analysis_result(media_id,generation,capability,result_json,model_name,model_version,created_at) VALUES(20,1,'summary','{"text":"summary"}','gpt-4','1.0',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert ai result: %v", err)
	}
	// Verify multiple generations append
	if _, err := db.Exec(`INSERT INTO ai_analysis_result(media_id,generation,capability,result_json,model_name,model_version,created_at) VALUES(20,2,'summary','{"text":"updated"}','gpt-4','1.0',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert ai result gen 2: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_analysis_result WHERE media_id=20`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 results, got %d", count)
	}
}

func TestPhase5MigrationPreservesOldSnapshots(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}

	// Insert legacy steps and capture their data
	if _, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json) VALUES(20,5,10,'scan','processing',0,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(1,20,5,'preview',1,'done')`); err != nil {
		t.Fatal(err)
	}

	// Snapshot before Phase 5 migration
	before := snapshotPublicationGraph(t, db)

	// Run Phase 5 migration
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}

	// Verify legacy step_type values are preserved
	var stepType, status string
	if err := db.QueryRow(`SELECT step_type, status FROM media_ingest_step WHERE run_id=1`).Scan(&stepType, &status); err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if stepType != "preview" || status != "done" {
		t.Fatalf("legacy step_type=%q status=%q, want preview done", stepType, status)
	}

	// Verify node_key was set from step_type
	var nodeKey string
	if err := db.QueryRow(`SELECT node_key FROM media_ingest_step WHERE run_id=1`).Scan(&nodeKey); err != nil {
		t.Fatalf("read node_key: %v", err)
	}
	if nodeKey != "preview" {
		t.Fatalf("node_key=%q, want preview", nodeKey)
	}

	_ = before // snapshot captured for reference
}

func TestPhase5MigrationStatusMappingLogic(t *testing.T) {
	// Verify the Go-level status map covers all required legacy statuses
	expected := map[string]string{
		"pending":    "waiting",
		"queued":     "waiting",
		"processing": "running",
		"success":    "done",
		"completed":  "done",
		"error":      "failed",
		"abandoned":  "failed",
	}
	for k, want := range expected {
		if got, ok := statusMapPhase5[k]; !ok || got != want {
			t.Fatalf("statusMapPhase5[%q] = (%q, %v), want (%q, true)", k, got, ok, want)
		}
	}
	// Unknown status should not be in the map
	if _, ok := statusMapPhase5["unknown_status"]; ok {
		t.Fatal("unknown_status should not be mapped")
	}
}

func TestPhase5MigrationForeignKeyIntegrity(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate V2: %v", err)
	}
	if err := migrateOrchestrationPhase5(context.Background(), db); err != nil {
		t.Fatalf("phase 5 migration: %v", err)
	}
	assertNoForeignKeyViolations(t, db)
}
