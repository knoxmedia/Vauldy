package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openIngestPublicationMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE library (id INTEGER PRIMARY KEY);
CREATE TABLE scan_task (id INTEGER PRIMARY KEY, library_id INTEGER NOT NULL, FOREIGN KEY(library_id) REFERENCES library(id));
CREATE TABLE media (
 id INTEGER PRIMARY KEY AUTOINCREMENT, library_id INTEGER, file_id TEXT UNIQUE, created_at TIMESTAMP,
 FOREIGN KEY(library_id) REFERENCES library(id)
);
CREATE TABLE post_ingest_task (
 id INTEGER PRIMARY KEY AUTOINCREMENT, media_id INTEGER NOT NULL, scan_task_id INTEGER,
 task_type TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'waiting', attempts INTEGER NOT NULL DEFAULT 0,
 max_attempts INTEGER NOT NULL DEFAULT 3, available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 lease_owner TEXT, lease_until TIMESTAMP, last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
 started_at TIMESTAMP, finished_at TIMESTAMP,
 FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,
 FOREIGN KEY(scan_task_id) REFERENCES scan_task(id) ON DELETE SET NULL,
 UNIQUE(media_id,task_type),
 CHECK(task_type IN ('poster','preview','keyframe','subtitle','atrack','encrypt')),
 CHECK(status IN ('waiting','running','done','failed','cancelled'))
);
CREATE INDEX idx_post_ingest_claim ON post_ingest_task(status,available_at,lease_until,created_at);
CREATE INDEX idx_post_ingest_scan ON post_ingest_task(scan_task_id,status);
INSERT INTO library(id) VALUES(1);
INSERT INTO scan_task(id,library_id) VALUES(10,1);
INSERT INTO media(id,library_id,file_id,created_at) VALUES(20,1,'old-with-time','2020-01-02 03:04:05');
INSERT INTO media(id,library_id,file_id,created_at) VALUES(21,1,'old-no-time',NULL);
INSERT INTO post_ingest_task(id,media_id,scan_task_id,task_type,status) VALUES(30,20,10,'poster','done');`)
	if err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	return db
}

func assertNoForeignKeyViolations(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fkid int
		_ = rows.Scan(&table, &rowid, &parent, &fkid)
		t.Fatalf("foreign key violation: table=%s row=%v parent=%s fk=%d", table, rowid, parent, fkid)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateIngestPublicationBackfillsExistingMediaVisible(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT publication_state,published_at,publication_error,ingest_generation FROM media ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		count++
		var state, published, publicationError string
		var generation int
		if err := rows.Scan(&state, &published, &publicationError, &generation); err != nil {
			t.Fatal(err)
		}
		if state != "published" || published == "" || publicationError != "" || generation != 0 {
			t.Fatalf("backfill row %d = state %q published %q error %q generation %d", count, state, published, publicationError, generation)
		}
		if count == 1 && published != "2020-01-02 03:04:05" && published != "2020-01-02T03:04:05Z" {
			t.Fatalf("published_at=%q want created_at", published)
		}
	}
	if count != 2 {
		t.Fatalf("media rows=%d want 2", count)
	}
	if _, err := db.Exec(`INSERT INTO media(library_id,file_id,publication_state) VALUES(1,'bad','hidden')`); err == nil {
		t.Fatal("invalid publication_state accepted")
	}
	if _, err := db.Exec(`INSERT INTO media(library_id,file_id,ingest_generation) VALUES(1,'negative',-1)`); err == nil {
		t.Fatal("negative ingest_generation accepted")
	}
	assertNoForeignKeyViolations(t, db)
}

func TestMigrateIngestPublicationCreatesRunStepConstraints(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,scan_task_id,reason,status,preserve_visibility,config_snapshot_json) VALUES(20,1,10,'scan','processing',0,'{}')`)
	if err != nil {
		t.Fatalf("insert valid run: %v", err)
	}
	runID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,?,?,?,?)`, runID, 20, 1, "scrape", 1, "waiting"); err != nil {
		t.Fatalf("insert valid step: %v", err)
	}
	rejects := []string{
		`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(20,0,'scan','processing','{}')`,
		`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(20,2,'bad','processing','{}')`,
		`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(20,2,'repair','processing','bad json')`,
		`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(1,20,2,'poster',1,'waiting')`,
		`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(1,20,1,'bad',1,'waiting')`,
	}
	for _, query := range rejects {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("constraint accepted invalid query: %s", query)
		}
	}
	for _, index := range []string{"idx_media_ingest_run_status_updated", "idx_media_ingest_run_scan_status"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s count=%d err=%v", index, n, err)
		}
	}
	var policyVersion int
	var terminalReason string
	if err := db.QueryRow(`SELECT policy_version,terminal_reason FROM media_ingest_run WHERE id=?`, runID).Scan(&policyVersion, &terminalReason); err != nil || policyVersion != 1 || terminalReason != "" {
		t.Fatalf("v2 run fields=%d/%q err=%v", policyVersion, terminalReason, err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,?,?,?,?)`, runID, 20, 1, "thumbnail", 1, "waiting"); err != nil {
		t.Fatalf("thumbnail step rejected: %v", err)
	}
	for _, table := range []string{"media_ingest_step_dependency", "media_ingest_evidence", "media_asset_stage_journal"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s count=%d err=%v", table, n, err)
		}
	}
	assertNoForeignKeyViolations(t, db)
}

func TestMigrateIngestPublicationEvidenceDirectMediaCascade(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,1,'scan','processing','{}',2)`)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,20,1,'thumbnail',1,'done')`, runID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,verified_at,stage_id) VALUES(?,?,20,1,'thumbnail','fp','{}',CURRENT_TIMESTAMP,'stage-1')`, runID, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM media WHERE id=20`); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_evidence`).Scan(&n)
	if n != 0 {
		t.Fatalf("evidence rows after media delete=%d", n)
	}
}

func TestMigrateIngestPublicationV2RejectsAmbiguousTranscodeBackfill(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if _, err := db.Exec(`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT,ingest_run_id INTEGER,ingest_step_id INTEGER,generation INTEGER); INSERT INTO transcode_task VALUES(1,'f',7,99,1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err == nil || !strings.Contains(strings.ToLower(err.Error()), "transcode") {
		t.Fatalf("error=%v", err)
	}
	var fileID string
	if err := db.QueryRow(`SELECT file_id FROM transcode_task WHERE id=1`).Scan(&fileID); err != nil || fileID != "f" {
		t.Fatalf("legacy row=%q err=%v", fileID, err)
	}
}

func TestMigrateIngestPublicationV2PostCommitForeignKeyFailureIsFatal(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	old := publicationMigrationPostCommitValidation
	publicationMigrationPostCommitValidation = func(context.Context, *sql.Conn) error { return fmt.Errorf("injected foreign key failure") }
	t.Cleanup(func() { publicationMigrationPostCommitValidation = old })
	err := migrateIngestPublication(context.Background(), db)
	var fatal *PostCommitMigrationValidationError
	if !errors.As(err, &fatal) {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestOpenSQLiteConcurrentPublicationV2MigrationSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publication.db")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := OpenSQLite(path)
			if db != nil {
				_ = db.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"media_ingest_step_dependency", "media_ingest_evidence", "media_asset_stage_journal"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s count=%d err=%v", table, n, err)
		}
	}
}
func TestMigrateIngestPublicationRebuildsPostIngestGenerationUniqueness(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var count, generation int
	var runID, stepID sql.NullInt64
	if err := db.QueryRow(`SELECT COUNT(*),generation,ingest_run_id,ingest_step_id FROM post_ingest_task WHERE id=30`).Scan(&count, &generation, &runID, &stepID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || generation != 0 || runID.Valid || stepID.Valid {
		t.Fatalf("legacy row count=%d generation=%d run=%v step=%v", count, generation, runID, stepID)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,generation) VALUES(20,'poster',0)`); err == nil {
		t.Fatal("duplicate generation zero accepted")
	}
	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(20,1,'repair','processing','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	newRun, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,20,1,'poster',1,'waiting')`, newRun)
	if err != nil {
		t.Fatal(err)
	}
	newStep, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,generation,ingest_run_id,ingest_step_id) VALUES(20,'poster',1,?,?)`, newRun, newStep); err != nil {
		t.Fatalf("new generation task rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,generation,ingest_run_id,ingest_step_id) VALUES(20,'poster',1,?,?)`, newRun, newStep); err == nil {
		t.Fatal("duplicate generation one accepted")
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,generation,ingest_run_id) VALUES(20,'poster',2,999)`); err == nil {
		t.Fatal("invalid run FK accepted")
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type) VALUES(20,'scrape')`); err == nil {
		t.Fatal("scrape task type unexpectedly accepted")
	}
	var rowsAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task`).Scan(&rowsAfter); err != nil || rowsAfter != 2 {
		t.Fatalf("row count=%d err=%v", rowsAfter, err)
	}
	assertNoForeignKeyViolations(t, db)
}

func TestMigrateIngestPublicationIsIdempotent(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migration run 1: %v", err)
	}
	if _, err := db.Exec(`UPDATE media SET publication_state='degraded', publication_error='preview exhausted', ingest_generation=7 WHERE id=20`); err != nil {
		t.Fatalf("set post-migration publication state: %v", err)
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migration run 2: %v", err)
	}
	var state, publicationError string
	var generation int
	if err := db.QueryRow(`SELECT publication_state,publication_error,ingest_generation FROM media WHERE id=20`).Scan(&state, &publicationError, &generation); err != nil {
		t.Fatal(err)
	}
	if state != "degraded" || publicationError != "preview exhausted" || generation != 7 {
		t.Fatalf("publication state after repeat = (%q,%q,%d), want degraded state preserved", state, publicationError, generation)
	}
	var mediaCount, taskCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 2 || taskCount != 1 {
		t.Fatalf("rows after repeat media=%d tasks=%d", mediaCount, taskCount)
	}
	for _, table := range []string{"media_ingest_run", "media_ingest_step", "post_ingest_task"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s count=%d err=%v", table, n, err)
		}
	}
	assertNoForeignKeyViolations(t, db)
}

func TestMigrateIngestPublicationRebuildsDriftedPostIngestConstraints(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	for _, statement := range []string{
		`ALTER TABLE post_ingest_task ADD COLUMN ingest_run_id INTEGER`,
		`ALTER TABLE post_ingest_task ADD COLUMN ingest_step_id INTEGER`,
		`ALTER TABLE post_ingest_task ADD COLUMN generation INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create drifted schema: %v", err)
		}
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("migrate drifted schema: %v", err)
	}
	for generation := 1; generation <= 2; generation++ {
		res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(20,?,'repair','processing','{}')`, generation)
		if err != nil {
			t.Fatal(err)
		}
		runID, _ := res.LastInsertId()
		res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,20,?,'poster',1,'waiting')`, runID, generation)
		if err != nil {
			t.Fatal(err)
		}
		stepID, _ := res.LastInsertId()
		if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,ingest_run_id,ingest_step_id) VALUES(20,?,'poster',?,?)`, generation, runID, stepID); err != nil {
			t.Fatalf("generation %d task rejected after drift repair: %v", generation, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,ingest_run_id) VALUES(20,2,'preview',1)`); err == nil {
		t.Fatal("post_ingest_task accepted mismatched run generation")
	}
	assertNoForeignKeyViolations(t, db)
}

func TestMigrateIngestPublicationRollbackPreservesLegacyPostIngest(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if _, err := db.Exec(`DROP TABLE post_ingest_task; CREATE TABLE post_ingest_task (
		id INTEGER PRIMARY KEY, media_id INTEGER NOT NULL, scan_task_id INTEGER, task_type TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'waiting', attempts INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL DEFAULT 3,
		available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, lease_owner TEXT, lease_until TIMESTAMP,
		last_error TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, started_at TIMESTAMP, finished_at TIMESTAMP,
		UNIQUE(media_id,task_type));
		CREATE INDEX idx_post_ingest_claim ON post_ingest_task(status,available_at,lease_until,created_at);
		INSERT INTO post_ingest_task(id,media_id,task_type) VALUES(99,20,'incompatible')`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err == nil {
		t.Fatal("migration unexpectedly accepted incompatible legacy row")
	}
	var taskType string
	if err := db.QueryRow(`SELECT task_type FROM post_ingest_task WHERE id=99`).Scan(&taskType); err != nil || taskType != "incompatible" {
		t.Fatalf("legacy row after rollback=%q err=%v", taskType, err)
	}
	var originalIndex, tempTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_post_ingest_claim'`).Scan(&originalIndex); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='post_ingest_task_new'`).Scan(&tempTable); err != nil {
		t.Fatal(err)
	}
	if originalIndex != 1 || tempTable != 0 {
		t.Fatalf("rollback schema index=%d temp_table=%d", originalIndex, tempTable)
	}
}

func TestMigrateIngestPublicationRemovesLegacyBinaryUniqueFromOtherwiseCurrentPostIngest(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("initial migration: %v", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX drift_post_ingest_media_type ON post_ingest_task(media_id,task_type)`); err != nil {
		t.Fatalf("install legacy binary unique: %v", err)
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("repair near-current drift: %v", err)
	}

	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(20,1,'repair','processing','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,20,1,'poster',1,'waiting')`, runID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,ingest_run_id,ingest_step_id) VALUES(20,1,'poster',?,?)`, runID, stepID); err != nil {
		t.Fatalf("generation one poster rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,ingest_run_id,ingest_step_id) VALUES(20,1,'poster',?,?)`, runID, stepID); err == nil {
		t.Fatal("same-generation duplicate poster accepted")
	}

	indexes, err := db.Query(`PRAGMA index_list(post_ingest_task)`)
	if err != nil {
		t.Fatal(err)
	}
	var uniqueNames []string
	for indexes.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := indexes.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			indexes.Close()
			t.Fatal(err)
		}
		if unique == 1 {
			uniqueNames = append(uniqueNames, name)
		}
	}
	if err := indexes.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range uniqueNames {
		rows, err := db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, name)
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			columns = append(columns, column)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if strings.Join(columns, ",") == "media_id,task_type" {
			t.Fatalf("legacy binary unique index remains: %s", name)
		}
	}
}

func TestMigrateIngestPublicationRejectsDriftedRunStepSchema(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if _, err := db.Exec(`
CREATE TABLE media_ingest_run (
 id INTEGER PRIMARY KEY AUTOINCREMENT, media_id INTEGER NOT NULL, generation INTEGER NOT NULL,
 scan_task_id INTEGER, reason TEXT NOT NULL, status TEXT NOT NULL, preserve_visibility INTEGER NOT NULL DEFAULT 0,
 config_snapshot_json TEXT NOT NULL, error_message TEXT NOT NULL DEFAULT '', created_at TIMESTAMP,
 updated_at TIMESTAMP, finished_at TIMESTAMP
);
CREATE TABLE media_ingest_step (
 id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL, media_id INTEGER NOT NULL,
 generation INTEGER NOT NULL, step_type TEXT NOT NULL, required INTEGER NOT NULL, status TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL DEFAULT 3, available_at TIMESTAMP,
 lease_owner TEXT, lease_until TIMESTAMP, last_error TEXT, started_at TIMESTAMP, finished_at TIMESTAMP,
 created_at TIMESTAMP, updated_at TIMESTAMP
);`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err == nil || !strings.Contains(err.Error(), "media_ingest_run schema invariant") {
		t.Fatalf("drifted run/step schema error=%v, want explicit run invariant failure", err)
	}
	var runCount, stepCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='media_ingest_run'`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='media_ingest_step'`).Scan(&stepCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || stepCount != 1 {
		t.Fatalf("drifted tables changed after rejection: run=%d step=%d", runCount, stepCount)
	}
}

func TestScrapeTaskNearCurrentSchemaMissingConstraintsIsRebuilt(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if _, err := db.Exec(`CREATE TABLE scrape_task (id INTEGER PRIMARY KEY AUTOINCREMENT,media_id INTEGER NOT NULL,task_type TEXT DEFAULT 'media',source TEXT DEFAULT 'auto',query TEXT,year INTEGER,status TEXT DEFAULT 'waiting',progress INTEGER DEFAULT 0,fail_count INTEGER DEFAULT 0,available_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,message TEXT,created_by INTEGER DEFAULT 0,ingest_run_id INTEGER,ingest_step_id INTEGER,generation INTEGER,lease_owner TEXT,lease_until TIMESTAMP,created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,started_at TIMESTAMP,finished_at TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	cols, err := ingestPublicationColumns(context.Background(), tx, "scrape_task")
	if err != nil {
		t.Fatal(err)
	}
	current, err := scrapeTaskPublicationSchemaCurrent(context.Background(), tx, cols)
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Fatal("scrape_task constraints remain stale")
	}
}

func TestScrapeTaskWrongNamedIndexesAreRebuiltWithExactColumns(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_scrape_task_claim", "idx_scrape_task_ingest", "idx_scrape_task_media"} {
		if _, err := db.Exec(`DROP INDEX IF EXISTS ` + name); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE INDEX ` + name + ` ON scrape_task(id)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{"idx_scrape_task_claim": "status,lease_until,created_at", "idx_scrape_task_ingest": "ingest_run_id,ingest_step_id,generation", "idx_scrape_task_media": "media_id,created_at"}
	for name, want := range wants {
		rows, err := db.Query(`PRAGMA index_info(` + name + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for rows.Next() {
			var seq, cid int
			var col string
			if err := rows.Scan(&seq, &cid, &col); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, col)
		}
		rows.Close()
		if got := strings.Join(cols, ","); got != want {
			t.Fatalf("%s=%s want=%s", name, got, want)
		}
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateIngestPublicationV2RejectsUnknownStepReferenceBeforeMutation(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE extension_step_ref(id INTEGER PRIMARY KEY,step_id INTEGER REFERENCES media_ingest_step(id)); CREATE TRIGGER extension_task_trigger AFTER INSERT ON post_ingest_task BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	err := migratePublicationV2(context.Background(), db)
	if err == nil || (!strings.Contains(err.Error(), "extension_step_ref") && !strings.Contains(err.Error(), "extension_task_trigger")) {
		t.Fatalf("error=%v", err)
	}
	for _, object := range []string{"extension_step_ref", "extension_task_trigger"} {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, object).Scan(&n)
		if n != 1 {
			t.Fatalf("object %s lost", object)
		}
	}
	var columns int
	_ = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('media_ingest_run') WHERE name='policy_version'`).Scan(&columns)
	if columns != 0 {
		t.Fatal("migration mutated schema before graph rejection")
	}
}

func TestMigrateIngestPublicationV2RejectsUnknownTriggerBeforeMutation(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER extension_step_trigger AFTER INSERT ON media_ingest_step BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	err := migratePublicationV2(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "extension_step_trigger") {
		t.Fatalf("error=%v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='extension_step_trigger'`).Scan(&n)
	if n != 1 {
		t.Fatal("unknown trigger lost")
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('media_ingest_run') WHERE name='policy_version'`).Scan(&n)
	if n != 0 {
		t.Fatal("schema changed before trigger rejection")
	}
}

func TestMigrateIngestPublicationV2TranscodeLinkageCheckIsExact(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := migratePublicationV2(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcode_task(file_id) VALUES('legacy')`); err != nil {
		t.Fatalf("all-null insert: %v", err)
	}
	res, _ := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,1,'scan','processing','{}',2)`)
	runID, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,20,1,'prepare',1,'waiting')`, runID)
	stepID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO transcode_task(file_id,media_id,ingest_run_id,ingest_step_id,generation) VALUES('linked',20,?,?,1)`, runID, stepID); err != nil {
		t.Fatalf("exact linked insert: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO transcode_task(file_id,ingest_run_id,ingest_step_id,generation) VALUES('missing-media',1,1,1)`,
		`INSERT INTO transcode_task(file_id,media_id,ingest_run_id,ingest_step_id,generation) VALUES('wrong-media',21,1,1,1)`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("invalid linkage accepted: %s", q)
		}
	}
	var triggers int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='transcode_task'`).Scan(&triggers)
	if triggers != 0 {
		t.Fatalf("migration trigger workaround remains: %d", triggers)
	}
}

func TestMigrateIngestPublicationV2RejectsMixedSchema(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE media_ingest_run ADD COLUMN policy_version INTEGER NOT NULL DEFAULT 1 CHECK(policy_version IN (1,2))`); err != nil {
		t.Fatal(err)
	}
	err := migratePublicationV2(context.Background(), db)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "mixed") {
		t.Fatalf("error=%v", err)
	}
}

func TestMigrateIngestPublicationV2WaitsForRealWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.db")
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	writer, err := sql.Open("sqlite", appendSQLitePragmas(path))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	conn, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		opened, e := OpenSQLiteContext(ctx, path)
		if opened != nil {
			opened.Close()
		}
		done <- e
	}()
	select {
	case e := <-done:
		t.Fatalf("open did not wait for writer: %v", e)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err = conn.ExecContext(context.Background(), `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if e := <-done; e != nil {
		t.Fatalf("open after release: %v", e)
	}
}

func TestMigrateIngestPublicationV2PreservesUserVersion(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if _, err := db.Exec(`PRAGMA user_version=37`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var got int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil || got != 37 {
		t.Fatalf("user_version=%d err=%v", got, err)
	}
	var temps int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_v2' OR name LIKE 'publication_backup_%'`).Scan(&temps)
	if temps != 0 {
		t.Fatalf("temporary tables=%d", temps)
	}
}

func TestMigrateIngestPublicationV2ExactSchemaRejectsDrift(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_ingest_dependency_visible; CREATE INDEX idx_ingest_dependency_visible ON media_ingest_step_dependency(step_id)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err == nil {
		t.Fatal("drifted v2 schema accepted")
	}
}

func TestMigrateIngestPublicationV2UpgradesKnownD725Trigger(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if _, err := db.Exec(`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER trg_transcode_task_fill_media AFTER INSERT ON transcode_task WHEN NEW.ingest_step_id IS NOT NULL AND NEW.media_id IS NULL BEGIN UPDATE transcode_task SET media_id=(SELECT media_id FROM media_ingest_step WHERE id=NEW.ingest_step_id) WHERE id=NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatalf("d725 upgrade: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='trg_transcode_task_fill_media'`).Scan(&n)
	if n != 0 {
		t.Fatal("legacy trigger remains")
	}
}

func TestMigrateIngestPublicationV2FaultStagesRollbackGraph(t *testing.T) {
	stages := []publicationMigrationStage{publicationStageAfterBackup, publicationStageAfterChildDrop, publicationStageAfterParentCreate, publicationStageAfterChildCreate, publicationStageAfterCopy, publicationStageBeforeSchemaValidate, publicationStageBeforeFKCheck}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			db := openIngestPublicationMigrationTestDB(t)
			if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			installCompletePublicationFaultFixture(t, db)
			before := snapshotPublicationGraph(t, db)
			old := publicationMigrationTestHook
			publicationMigrationTestHook = func(g publicationMigrationStage) error {
				if g == stage {
					return fmt.Errorf("injected %s", stage)
				}
				return nil
			}
			t.Cleanup(func() { publicationMigrationTestHook = old })
			if err := migratePublicationV2(context.Background(), db); err == nil {
				t.Fatal("fault accepted")
			}
			after := snapshotPublicationGraph(t, db)
			if before != after {
				t.Fatalf("graph changed\nbefore=%s\nafter=%s", before, after)
			}
			var temp int
			_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%__publication_v2_backup' OR name LIKE '%_v2'`).Scan(&temp)
			if temp != 0 {
				t.Fatalf("temp objects=%d", temp)
			}
		})
	}
}

func installCompletePublicationFaultFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP TABLE IF EXISTS scrape_task`); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE transcode_preset(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE preset_rendition(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT,status TEXT DEFAULT 'waiting',progress INTEGER DEFAULT 0,error_message TEXT,output_path TEXT,created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,task_type TEXT DEFAULT 'batch',started_at TIMESTAMP,completed_at TIMESTAMP,preset_id INTEGER,ingest_run_id INTEGER,ingest_step_id INTEGER,generation INTEGER,media_id INTEGER,lease_owner TEXT,lease_until TIMESTAMP)`,
		canonicalPretranscodeTaskMetaSchema,
		canonicalPretranscodeRenditionJobSchema,
		`INSERT INTO transcode_preset VALUES(1)`, `INSERT INTO preset_rendition VALUES(2)`,
		`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(20,1,'repair','processing','{"fixture":1}')`,
		`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,available_at,lease_owner,lease_until,last_error,started_at,created_at,updated_at) VALUES(70,1,20,1,'prepare',1,'running',2,4,'2030-01-01','step-owner','2030-01-02','step-error','2029-01-01','2029-01-01','2029-01-02')`,
		`INSERT INTO post_ingest_task(id,media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,available_at,lease_owner,lease_until,last_error,created_at,updated_at,started_at) VALUES(71,20,10,1,70,1,'poster','running',2,4,'2030-01-01','post-owner','2030-01-02','post-error','2029-01-01','2029-01-02','2029-01-03')`,
		`CREATE TABLE scrape_task(id INTEGER PRIMARY KEY AUTOINCREMENT,media_id INTEGER NOT NULL,task_type TEXT DEFAULT 'media',source TEXT DEFAULT 'auto',query TEXT,year INTEGER,status TEXT DEFAULT 'waiting',progress INTEGER DEFAULT 0,fail_count INTEGER DEFAULT 0,available_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,message TEXT,created_by INTEGER DEFAULT 0,ingest_run_id INTEGER,ingest_step_id INTEGER,generation INTEGER,lease_owner TEXT,lease_until TIMESTAMP,created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,started_at TIMESTAMP,finished_at TIMESTAMP)`,
		`INSERT INTO scrape_task VALUES(72,20,'media','fixture','query',2026,'running',37,2,'2030-01-01','scrape-error',9,1,70,1,'scrape-owner','2030-01-02','2029-01-01','2029-01-02',NULL)`,
		`INSERT INTO transcode_task(id,file_id,status,progress,error_message,output_path,task_type,ingest_run_id,ingest_step_id,generation,media_id,lease_owner,lease_until) VALUES(73,'fixture-file','running',55,'transcode-error','output','pretranscode',1,70,1,20,'transcode-owner','2030-01-02')`,
		`INSERT INTO pretranscode_task_meta(task_id,preset_id,output_format,encryption_mode,priority,output_path,ingest_jobs_snapshot_json) VALUES(73,1,'hls','aes128','high','meta-output','{"jobs":[1]}')`,
		`INSERT INTO pretranscode_rendition_job(id,task_id,rendition_id,rendition_name,status,progress,output_path,error_message,encoder_used,started_at,created_at,available_at,lease_owner,lease_until,config_snapshot_json) VALUES(74,73,2,'720p','running',66,'job-output','job-error','nvenc','2029-01-01','2029-01-01','2030-01-01','job-owner','2030-01-02','{"job":1}')`,
		canonicalIngestDependencySchema,
		`INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(70,70,'step_done')`,
		canonicalIngestEvidenceSchema,
		`INSERT INTO media_ingest_evidence(id,run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(75,1,70,20,1,'poster','fp','{"path":"x"}','fixture','2029-01-01','stage-75')`,
		canonicalAssetStageJournalSchema,
		`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,quarantine_path,staged_path,hashes_sizes_json,recovery_error,created_at,updated_at) VALUES('stage-75',20,1,70,1,'journal-owner','fp','poster','staged','original','quarantine','staged','{"size":1}','recovery','2029-01-01','2029-01-02')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("complete fixture %q: %v", stmt, err)
		}
	}
	for table, cols := range map[string]string{"media_ingest_step": "id,status", "post_ingest_task": "id,status", "scrape_task": "id,status", "transcode_task": "id,status", "pretranscode_task_meta": "task_id,priority", "pretranscode_rendition_job": "id,status", "media_ingest_step_dependency": "step_id,dependency_kind", "media_ingest_evidence": "id,kind", "media_asset_stage_journal": "stage_id,state"} {
		if _, err := db.Exec(`CREATE INDEX custom_fault_` + table + ` ON ` + table + `(` + cols + `)`); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotPublicationGraph(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out strings.Builder
	for _, table := range publicationGraphOrder {
		var sqlText sql.NullString
		_ = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&sqlText)
		if !sqlText.Valid {
			continue
		}
		fmt.Fprintf(&out, "%s:%s:", table, sqlText.String)
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		fmt.Fprint(&out, cols)
		for rows.Next() {
			vals := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range vals {
				ptr[i] = &vals[i]
			}
			if err := rows.Scan(ptr...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprint(&out, vals)
		}
		rows.Close()
		idx, _ := db.Query(`SELECT name,sql FROM sqlite_master WHERE type IN ('index','trigger') AND tbl_name=? ORDER BY type,name`, table)
		for idx.Next() {
			var n string
			var q sql.NullString
			_ = idx.Scan(&n, &q)
			fmt.Fprint(&out, n, q.String)
		}
		idx.Close()
	}
	return out.String()
}

func TestMigrateIngestPublicationV2ManagedConstraintsBehavior(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	res, _ := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,1,'scan','processing','{}',2)`)
	runID, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,20,1,'poster',1,'done')`, runID)
	stepID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(21,1,'scan','processing','{}',3)`); err == nil {
		t.Fatal("invalid policy accepted")
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step_dependency(step_id,dependency_kind) VALUES(?,'step_done')`, stepID); err == nil {
		t.Fatal("dependency null edge accepted")
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step_dependency(step_id,dependency_kind) VALUES(?,'media_visible'),(?,'media_visible')`, stepID, stepID); err == nil {
		t.Fatal("duplicate visible dependency accepted")
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,verified_at,stage_id) VALUES(?,?,20,1,'poster','fp','bad',CURRENT_TIMESTAMP,'stage')`, runID, stepID); err == nil {
		t.Fatal("invalid evidence json accepted")
	}
	if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('j',20,?,?,1,'o','fp','poster','bad','p','{}')`, runID, stepID); err == nil {
		t.Fatal("invalid journal state accepted")
	}
	if _, err := db.Exec(`UPDATE media_ingest_run SET superseded_by_generation=1,superseded_at=CURRENT_TIMESTAMP WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err == nil {
		t.Fatal("invalid supersession existing row accepted")
	}
}

func TestMigrateIngestPublicationV2NoOpSchemaByteIdentical(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if _, err := db.Exec(`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	before := snapshotPublicationGraph(t, db)
	for i := 0; i < 10; i++ {
		if err := migrateIngestPublication(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	after := snapshotPublicationGraph(t, db)
	if before != after {
		t.Fatal("complete v2 reentry changed sqlite graph")
	}
}

func TestMigrateIngestPublicationV2RejectsChildTriggerBeforeMutation(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER extension_post_trigger AFTER INSERT ON post_ingest_task BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if err := migratePublicationV2(context.Background(), db); err == nil {
		t.Fatal("child trigger accepted")
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='extension_post_trigger'`).Scan(&n)
	if n != 1 {
		t.Fatal("child trigger lost")
	}
}

func TestMigrateIngestPublicationV2RejectsExactBackupResidue(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if _, err := db.Exec(`CREATE TABLE media_ingest_step__publication_v2_backup(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err == nil || !strings.Contains(err.Error(), "mixed") {
		t.Fatalf("error=%v", err)
	}
}

func TestCanonicalTranscodeSQLPreservesCustomCheckAndDeduplicatesManagedClauses(t *testing.T) {
	original := `CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT CHECK(length(file_id)>0),media_id INTEGER,ingest_run_id INTEGER,ingest_step_id INTEGER,generation INTEGER,lease_owner TEXT,lease_until TIMESTAMP,FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,FOREIGN KEY(ingest_run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation) ON DELETE CASCADE,FOREIGN KEY(ingest_step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation) ON DELETE CASCADE,CHECK((ingest_run_id IS NULL AND ingest_step_id IS NULL AND generation IS NULL AND media_id IS NULL) OR (ingest_run_id IS NOT NULL AND ingest_step_id IS NOT NULL AND generation IS NOT NULL)))`
	got, err := canonicalTranscodeSQL(original)
	if err != nil {
		t.Fatal(err)
	}
	n := normalizePublicationSQL(got)
	if !strings.Contains(n, `check(length(file_id)>0)`) {
		t.Fatal("custom CHECK lost")
	}
	for _, clause := range []string{`foreignkey(media_id)referencesmedia(id)ondeletecascade`, `foreignkey(ingest_run_id,media_id,generation)referencesmedia_ingest_run(id,media_id,generation)ondeletecascade`, `foreignkey(ingest_step_id,media_id,generation)referencesmedia_ingest_step(id,media_id,generation)ondeletecascade`} {
		if strings.Count(n, clause) != 1 {
			t.Fatalf("managed clause count %q=%d", clause, strings.Count(n, clause))
		}
	}
	if strings.Count(n, `check((ingest_run_idisnullandingest_step_idisnullandgenerationisnullandmedia_idisnull)or(ingest_run_idisnotnullandingest_step_idisnotnullandgenerationisnotnullandmedia_idisnotnull))`) != 1 {
		t.Fatalf("strict CHECK not exact once: %s", got)
	}
}

func TestCanonicalTranscodeSQLIsByteIdempotent(t *testing.T) {
	one, err := canonicalTranscodeSQL(`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT CHECK(file_id <> 'x'))`)
	if err != nil {
		t.Fatal(err)
	}
	two, err := canonicalTranscodeSQL(one)
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatalf("canonical SQL changed on second pass\none=%s\ntwo=%s", one, two)
	}
}

func TestPublicationTranscodeSchemaCurrentRejectsWeakCheck(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT,media_id INTEGER,ingest_run_id INTEGER,ingest_step_id INTEGER,generation INTEGER,lease_owner TEXT,lease_until TIMESTAMP,FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,FOREIGN KEY(ingest_run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation) ON DELETE CASCADE,FOREIGN KEY(ingest_step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation) ON DELETE CASCADE,CHECK((ingest_run_id IS NULL AND ingest_step_id IS NULL AND generation IS NULL AND media_id IS NULL) OR (ingest_run_id IS NOT NULL AND ingest_step_id IS NOT NULL AND generation IS NOT NULL)))`)
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := db.Conn(context.Background())
	defer conn.Close()
	if publicationTranscodeSchemaCurrent(context.Background(), conn) {
		t.Fatal("weak CHECK considered current")
	}
}

func TestPublicationTranscodeSchemaCurrentRejectsMissingCheck(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT,media_id INTEGER,ingest_run_id INTEGER,ingest_step_id INTEGER,generation INTEGER,lease_owner TEXT,lease_until TIMESTAMP,FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,FOREIGN KEY(ingest_run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation) ON DELETE CASCADE,FOREIGN KEY(ingest_step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation) ON DELETE CASCADE)`)
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := db.Conn(context.Background())
	defer conn.Close()
	if publicationTranscodeSchemaCurrent(context.Background(), conn) {
		t.Fatal("missing CHECK considered current")
	}
}

const d725TranscodeTaskDDL = `CREATE TABLE transcode_task (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	file_id TEXT,
	quality TEXT,
	status TEXT DEFAULT 'waiting',
	progress INTEGER DEFAULT 0,
	error_message TEXT,
	output_path TEXT,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	task_type TEXT NOT NULL DEFAULT 'batch',
	started_at TIMESTAMP,
	completed_at TIMESTAMP,
	preset_id INTEGER,
	ingest_run_id INTEGER,
	ingest_step_id INTEGER,
	generation INTEGER,
	media_id INTEGER,
	lease_owner TEXT,
	lease_until TIMESTAMP,
	FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,
	FOREIGN KEY(ingest_run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation) ON DELETE CASCADE,
	FOREIGN KEY(ingest_step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation) ON DELETE CASCADE,
	CHECK((ingest_run_id IS NULL AND ingest_step_id IS NULL AND generation IS NULL AND media_id IS NULL) OR (ingest_run_id IS NOT NULL AND ingest_step_id IS NOT NULL AND generation IS NOT NULL)))`

var d725TranscodeExpectedColumns = []string{"id", "file_id", "quality", "status", "progress", "error_message", "output_path", "created_at", "task_type", "started_at", "completed_at", "preset_id", "ingest_run_id", "ingest_step_id", "generation", "media_id", "lease_owner", "lease_until"}

func installD725TranscodeFixture(t *testing.T, db *sql.DB) (int64, int64) {
	t.Helper()
	// Independent supporting schema matching the d725 run/step identities needed by transcode.
	if _, err := db.Exec(`CREATE TABLE media_ingest_run (
		id INTEGER PRIMARY KEY AUTOINCREMENT,media_id INTEGER NOT NULL,generation INTEGER NOT NULL CHECK(generation>0),scan_task_id INTEGER,
		reason TEXT NOT NULL CHECK(reason IN ('scan','repair','manual_retry')),status TEXT NOT NULL CHECK(status IN ('processing','published','degraded','failed','cancelled')),
		preserve_visibility INTEGER NOT NULL DEFAULT 0 CHECK(preserve_visibility IN (0,1)),config_snapshot_json TEXT NOT NULL CHECK(json_valid(config_snapshot_json)),
		error_message TEXT NOT NULL DEFAULT '',created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,finished_at TIMESTAMP,
		FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,FOREIGN KEY(scan_task_id) REFERENCES scan_task(id) ON DELETE SET NULL,UNIQUE(media_id,generation),UNIQUE(id,media_id,generation));
	CREATE TABLE media_ingest_step (
		id INTEGER PRIMARY KEY AUTOINCREMENT,run_id INTEGER NOT NULL,media_id INTEGER NOT NULL,generation INTEGER NOT NULL CHECK(generation>0),
		step_type TEXT NOT NULL CHECK(step_type IN ('poster','scrape','preview','keyframe','subtitle','atrack','encrypt','prepare','thumbnail')),required INTEGER NOT NULL CHECK(required IN (0,1)),
		status TEXT NOT NULL CHECK(status IN ('waiting','running','done','skipped','failed','cancelled')),attempts INTEGER NOT NULL DEFAULT 0,max_attempts INTEGER NOT NULL DEFAULT 3,
		available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,lease_owner TEXT,lease_until TIMESTAMP,last_error TEXT NOT NULL DEFAULT '',started_at TIMESTAMP,finished_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(run_id) REFERENCES media_ingest_run(id) ON DELETE CASCADE,FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,
		FOREIGN KEY(media_id,generation) REFERENCES media_ingest_run(media_id,generation),FOREIGN KEY(run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation),
		UNIQUE(run_id,step_type),UNIQUE(id,media_id,generation));`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json) VALUES(20,1,'scan','processing','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,20,1,'prepare',1,'waiting')`, runID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	if _, err = db.Exec(d725TranscodeTaskDDL); err != nil {
		t.Fatal(err)
	}
	columns, err := publicationColumnNames(context.Background(), db, "transcode_task")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(columns, ",") != strings.Join(d725TranscodeExpectedColumns, ",") {
		t.Fatalf("d725 fixture columns=%v want=%v", columns, d725TranscodeExpectedColumns)
	}
	if _, err = db.Exec(`INSERT INTO transcode_task(id,file_id,quality,status,progress,error_message,output_path,task_type,media_id,ingest_run_id,ingest_step_id,generation,lease_owner,lease_until) VALUES(7,'d725-file','1080p','waiting',17,'legacy-error','legacy-output','pretranscode',NULL,?,?,1,'legacy-owner','2030-01-01')`, runID, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(legacyPublicationFillMediaTriggerSQL); err != nil {
		t.Fatal(err)
	}
	return runID, stepID
}
func TestMigrateIngestPublicationV2UpgradesRealD725TranscodeFixture(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	runID, stepID := installD725TranscodeFixture(t, db)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var fileID, quality, status, errorMessage, outputPath, taskType, owner, until string
	var mediaID, gotRun, gotStep, generation int64
	var progress int
	if err := db.QueryRow(`SELECT file_id,quality,status,progress,error_message,output_path,task_type,media_id,ingest_run_id,ingest_step_id,generation,lease_owner,lease_until FROM transcode_task WHERE id=7`).Scan(&fileID, &quality, &status, &progress, &errorMessage, &outputPath, &taskType, &mediaID, &gotRun, &gotStep, &generation, &owner, &until); err != nil {
		t.Fatal(err)
	}
	if fileID != "d725-file" || quality != "1080p" || status != "waiting" || progress != 17 || errorMessage != "legacy-error" || outputPath != "legacy-output" || taskType != "pretranscode" || mediaID != 20 || gotRun != runID || gotStep != stepID || generation != 1 || owner != "legacy-owner" || (until != "2030-01-01" && until != "2030-01-01T00:00:00Z") {
		t.Fatalf("row changed: file=%q quality=%q status=%q progress=%d error=%q output=%q type=%q media/run/step/gen=%d/%d/%d/%d owner/lease=%q/%q", fileID, quality, status, progress, errorMessage, outputPath, taskType, mediaID, gotRun, gotStep, generation, owner, until)
	}
	var triggers int
	_ = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name='trg_transcode_task_fill_media'`).Scan(&triggers)
	if triggers != 0 {
		t.Fatal("known trigger remains")
	}
}

func TestMigrateIngestPublicationV2RebuildsWeakTranscodeCheck(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	installD725TranscodeFixture(t, db)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcode_task(id,file_id,status,ingest_run_id,ingest_step_id,generation) VALUES(8,'partial','waiting',1,1,1)`); err == nil {
		t.Fatal("partial linked insert accepted")
	}
}

func TestMigrateIngestPublicationV2DigestAcceptsOnlyMediaBackfill(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	installD725TranscodeFixture(t, db)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateIngestPublicationV2DigestRejectsNonMediaMutation(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	installD725TranscodeFixture(t, db)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	graph, err := backupPublicationGraph(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if err = dropPublicationGraph(context.Background(), conn, graph); err != nil {
		t.Fatal(err)
	}
	if err = createPublicationParents(context.Background(), conn, graph); err != nil {
		t.Fatal(err)
	}
	if err = createPublicationChildren(context.Background(), conn, graph); err != nil {
		t.Fatal(err)
	}
	if err = restorePublicationIndexes(context.Background(), conn, graph); err != nil {
		t.Fatal(err)
	}
	if err = copyPublicationGraph(context.Background(), conn, graph); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(context.Background(), `UPDATE transcode_task SET file_id='corrupt' WHERE id=7`); err != nil {
		t.Fatal(err)
	}
	if err = validatePublicationBackups(context.Background(), conn, graph); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("error=%v", err)
	}
}

func TestMigrateIngestPublicationV2PreservesGraphCustomIndexes(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	installD725TranscodeFixture(t, db)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX custom_step_unique ON media_ingest_step(id,step_type); CREATE INDEX custom_transcode_perf ON transcode_task(status,file_id); CREATE UNIQUE INDEX custom_post_unique ON post_ingest_task(id,task_type); CREATE INDEX custom_scrape_perf ON scrape_task(status,created_at)`); err != nil {
		t.Fatal(err)
	}
	if err := migratePublicationV2(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"custom_step_unique", "custom_transcode_perf", "custom_post_unique", "custom_scrape_perf"} {
		var sqlText string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&sqlText); err != nil || sqlText == "" {
			t.Fatalf("index %s sql=%q err=%v", name, sqlText, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(id,media_id,generation,task_type) VALUES(30,20,1,'preview')`); err == nil {
		t.Fatal("restored custom unique not enforced")
	}
	before := snapshotPublicationGraph(t, db)
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	after := snapshotPublicationGraph(t, db)
	if before != after {
		t.Fatal("second migration changed graph")
	}
}

func TestCanonicalTranscodeSQLHandlesSQLiteLexicalForms(t *testing.T) {
	cases := []struct{ name, clause string }{
		{"bracket identifier", `[custom,column] TEXT CHECK([custom,column] <> 'x')`},
		{"line comment", "custom_line TEXT -- comma, paren ) and ' quote\n CHECK(custom_line <> '')"},
		{"block comment", `custom_block TEXT /* comma, paren ) and ' quote */ CHECK(custom_block <> '')`},
		{"quoted punctuation", `custom_quote TEXT CHECK(custom_quote <> '),''still string')`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openIngestPublicationMigrationTestDB(t)
			original := `CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,` + tc.clause + `,media_id INTEGER,ingest_run_id INTEGER,ingest_step_id INTEGER,generation INTEGER,lease_owner TEXT,lease_until TIMESTAMP,` +
				`FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,FOREIGN KEY(ingest_run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation) ON DELETE CASCADE,FOREIGN KEY(ingest_step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation) ON DELETE CASCADE,` + publicationTranscodeStrictCheck + `)`
			if _, err := db.Exec(original); err != nil {
				t.Fatalf("SQLite rejected source fixture: %v\n%s", err, original)
			}
			if _, err := db.Exec(`DROP TABLE transcode_task`); err != nil {
				t.Fatal(err)
			}
			got, err := canonicalTranscodeSQL(original)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(got); err != nil {
				t.Fatalf("SQLite rejected canonical SQL: %v\n%s", err, got)
			}
			if !strings.Contains(got, tc.clause) {
				t.Fatalf("custom clause/comment changed\nwant=%s\ngot=%s", tc.clause, got)
			}
			n := normalizePublicationSQL(got)
			for _, managed := range []string{`foreignkey(media_id)referencesmedia(id)ondeletecascade`, `foreignkey(ingest_run_id,media_id,generation)referencesmedia_ingest_run(id,media_id,generation)ondeletecascade`, `foreignkey(ingest_step_id,media_id,generation)referencesmedia_ingest_step(id,media_id,generation)ondeletecascade`, normalizePublicationSQL(publicationTranscodeStrictCheck)} {
				if strings.Count(n, managed) != 1 {
					t.Fatalf("managed count %q=%d", managed, strings.Count(n, managed))
				}
			}
		})
	}
}

func TestSplitPublicationSQLClausesRejectsUnterminatedLexicalForms(t *testing.T) {
	for _, body := range []string{`id INTEGER, [unterminated`, `id INTEGER, /* unterminated`, `id INTEGER, 'unterminated`, "id INTEGER, `unterminated"} {
		if _, err := splitPublicationSQLClauses(body); err == nil {
			t.Fatalf("accepted malformed body %q", body)
		}
	}
}

func TestMigrateIngestPublicationV2EnterpriseManagedIndexesCreatedOnce(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(legacyPublicationFillMediaTriggerSQL); err != nil {
		t.Fatal(err)
	}
	if err = migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_post_ingest_claim", "idx_post_ingest_scan", "idx_post_ingest_run", "idx_post_ingest_step", "idx_scrape_task_claim", "idx_scrape_task_ingest", "idx_scrape_task_media", "idx_pretranscode_job_status", "idx_pretranscode_job_task", "idx_ingest_dependency_visible", "idx_asset_stage_recovery"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("managed index %s count=%d err=%v", name, n, err)
		}
	}
}

func TestMigrateIngestPublicationV2PreservesCustomIndexOnEveryGraphTableOnce(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	custom := map[string]string{
		"media_ingest_step": "id,step_type", "post_ingest_task": "id,task_type", "scrape_task": "id,status", "transcode_task": "id,status",
		"pretranscode_task_meta": "task_id,priority", "pretranscode_rendition_job": "id,status", "media_ingest_step_dependency": "step_id,dependency_kind",
		"media_ingest_evidence": "id,kind", "media_asset_stage_journal": "stage_id,state",
	}
	for table, cols := range custom {
		if _, err = db.Exec(`CREATE INDEX custom_` + table + ` ON ` + table + `(` + cols + `)`); err != nil {
			t.Fatalf("create %s custom index: %v", table, err)
		}
	}
	if _, err = db.Exec(legacyPublicationFillMediaTriggerSQL); err != nil {
		t.Fatal(err)
	}
	if err = migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for table := range custom {
		name := "custom_" + table
		var n int
		var sqlText string
		if err := db.QueryRow(`SELECT COUNT(*),sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n, &sqlText); err != nil || n != 1 || sqlText == "" {
			t.Fatalf("custom index %s count=%d sql=%q err=%v", name, n, sqlText, err)
		}
	}
}

func TestCanonicalTranscodeSQLDetectsColumnsStructurally(t *testing.T) {
	cases := []struct{ name, decoy string }{
		{"leading comment", `/* media_id INTEGER, ingest_run_id INTEGER, ingest_step_id INTEGER, generation INTEGER, lease_owner TEXT, lease_until TIMESTAMP */ payload TEXT`},
		{"line comment", "-- media_id INTEGER, ingest_run_id INTEGER, ingest_step_id INTEGER, generation INTEGER, lease_owner TEXT, lease_until TIMESTAMP\npayload TEXT"},
		{"string default", `payload TEXT DEFAULT 'media_id ingest_run_id ingest_step_id generation lease_owner lease_until'`},
		{"generated expression", `payload TEXT GENERATED ALWAYS AS ('media_id ingest_run_id ingest_step_id generation lease_owner lease_until') VIRTUAL`},
		{"check expression", `payload TEXT CHECK(payload <> 'media_id ingest_run_id ingest_step_id generation lease_owner lease_until')`},
		{"bracket decoy", `[media_id decoy] TEXT DEFAULT 'ingest_run_id ingest_step_id generation lease_owner lease_until'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openIngestPublicationMigrationTestDB(t)
			original := `CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,` + tc.decoy + `)`
			if _, err := db.Exec(original); err != nil {
				t.Fatalf("source DDL: %v\n%s", err, original)
			}
			if _, err := db.Exec(`DROP TABLE transcode_task`); err != nil {
				t.Fatal(err)
			}
			got, err := canonicalTranscodeSQL(original)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(got); err != nil {
				t.Fatalf("canonical DDL: %v\n%s", err, got)
			}
			cols, err := publicationColumnNames(context.Background(), db, "transcode_task")
			if err != nil {
				t.Fatal(err)
			}
			have := map[string]int{}
			for _, c := range cols {
				have[strings.ToLower(c)]++
			}
			for _, want := range []string{"media_id", "ingest_run_id", "ingest_step_id", "generation", "lease_owner", "lease_until"} {
				if have[want] != 1 {
					t.Fatalf("column %s count=%d cols=%v SQL=%s", want, have[want], cols, got)
				}
			}
		})
	}
}

func TestCanonicalTranscodeSQLRecognizesQuotedManagedColumnsOnce(t *testing.T) {
	for _, quoted := range []struct{ name, value string }{
		{"bracket", `[media_id]`}, {"double", `"INGEST_RUN_ID"`}, {"backtick", "`ingest_step_id`"},
	} {
		t.Run(quoted.name, func(t *testing.T) {
			columns := map[string]string{"media_id": "media_id", "ingest_run_id": "ingest_run_id", "ingest_step_id": "ingest_step_id"}
			for name := range columns {
				if strings.Contains(strings.ToLower(quoted.value), name) {
					columns[name] = quoted.value
				}
			}
			original := `CREATE TABLE transcode_task(` + columns["media_id"] + ` INTEGER,` + columns["ingest_run_id"] + ` INTEGER,` + columns["ingest_step_id"] + ` INTEGER,generation INTEGER,lease_owner TEXT,lease_until TIMESTAMP)`
			one, err := canonicalTranscodeSQL(original)
			if err != nil {
				t.Fatal(err)
			}
			two, err := canonicalTranscodeSQL(one)
			if err != nil {
				t.Fatal(err)
			}
			if one != two {
				t.Fatalf("not idempotent\none=%s\ntwo=%s", one, two)
			}
			db := openIngestPublicationMigrationTestDB(t)
			if _, err = db.Exec(one); err != nil {
				t.Fatal(err)
			}
			cols, _ := publicationColumnNames(context.Background(), db, "transcode_task")
			counts := map[string]int{}
			for _, c := range cols {
				counts[strings.ToLower(c)]++
			}
			for _, want := range []string{"media_id", "ingest_run_id", "ingest_step_id", "generation", "lease_owner", "lease_until"} {
				if counts[want] != 1 {
					t.Fatalf("%s count=%d cols=%v", want, counts[want], cols)
				}
			}
		})
	}
}

func TestCanonicalTranscodeSQLLocatesTableBodyLexically(t *testing.T) {
	cases := []struct{ name, ddl, table string }{
		{"double quoted table", `CREATE TABLE "transcode(task)" /* header ( comment */ (id INTEGER PRIMARY KEY, custom TEXT CHECK(custom <> ')'))`, `transcode(task)`},
		{"backtick table", "CREATE TABLE `transcode(task)` -- header ( comment\n (id INTEGER PRIMARY KEY, custom TEXT)", `transcode(task)`},
		{"bracket table", `CREATE TABLE [transcode(task)] /* ) , ( */ (id INTEGER PRIMARY KEY, custom TEXT)`, `transcode(task)`},
		{"header block comment", `CREATE /* misleading ( ) */ TABLE transcode_task /* actual header ( ) */ (id INTEGER PRIMARY KEY, custom TEXT)`, `transcode_task`},
		{"header line comment", "CREATE TABLE transcode_task -- misleading ( )\n (id INTEGER PRIMARY KEY, custom TEXT)", `transcode_task`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openIngestPublicationMigrationTestDB(t)
			if _, err := db.Exec(tc.ddl); err != nil {
				t.Fatalf("SQLite rejected source: %v\n%s", err, tc.ddl)
			}
			var stored string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, tc.table).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stored, "custom") {
				t.Fatalf("sqlite_master lost custom clause: %s", stored)
			}
			if _, err := db.Exec(`DROP TABLE ` + quoteIdent(tc.table)); err != nil {
				t.Fatal(err)
			}
			got, err := canonicalTranscodeSQL(stored)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, "custom") {
				t.Fatalf("canonical lost custom clause/comment: %s", got)
			}
			if _, err = db.Exec(got); err != nil {
				t.Fatalf("SQLite rejected canonical: %v\n%s", err, got)
			}
			if _, err = db.Exec(`DROP TABLE ` + quoteIdent(tc.table)); err != nil {
				t.Fatal(err)
			}
			two, err := canonicalTranscodeSQL(got)
			if err != nil {
				t.Fatal(err)
			}
			if two != got {
				t.Fatalf("not idempotent\none=%s\ntwo=%s", got, two)
			}
			if _, err = db.Exec(two); err != nil {
				t.Fatalf("SQLite rejected second canonical: %v", err)
			}
		})
	}
}

func TestFindCreateTableBodyRejectsMalformedLexicalHeader(t *testing.T) {
	for _, ddl := range []string{
		`CREATE TABLE "unterminated (id INTEGER)`,
		"CREATE TABLE `unterminated (id INTEGER)",
		`CREATE TABLE [unterminated (id INTEGER)`,
		`CREATE TABLE transcode_task /* unterminated (id INTEGER)`,
		"CREATE TABLE transcode_task -- no body",
		`CREATE TABLE transcode_task`,
		`CREATE TABLE transcode_task (id INTEGER /* unterminated )`,
	} {
		if _, _, err := findCreateTableBody(ddl); err == nil {
			t.Fatalf("accepted malformed DDL %q", ddl)
		}
	}
}

func TestPublicationProductVariantRejectsPartialEnterpriseGraph(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY); CREATE TABLE pretranscode_task_meta(task_id INTEGER PRIMARY KEY REFERENCES transcode_task(id) ON DELETE CASCADE)`); err != nil {
		t.Fatal(err)
	}
	before := snapshotPublicationGraph(t, db)
	err := migratePublicationV2(context.Background(), db)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "partial enterprise") {
		t.Fatalf("error=%v", err)
	}
	if after := snapshotPublicationGraph(t, db); after != before {
		t.Fatal("partial enterprise rejection mutated graph")
	}
}

func TestPublicationProductVariantCommunityHasNoEnterpriseChildren(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if _, err := db.Exec(`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestPublication(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"pretranscode_task_meta", "pretranscode_rendition_job"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("community table %s count=%d err=%v", table, n, err)
		}
	}
}

func TestPublicationEnterpriseChildrenRebuiltCanonical(t *testing.T) {
	db := openIngestPublicationMigrationTestDB(t)
	if err := migrateIngestPublicationV1(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE transcode_preset(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE preset_rendition(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT)`,
		`CREATE TABLE pretranscode_task_meta(task_id INTEGER PRIMARY KEY,preset_id INTEGER NOT NULL,output_format TEXT NOT NULL DEFAULT 'hls',encryption_mode TEXT DEFAULT 'none',priority TEXT DEFAULT 'normal',output_path TEXT,ingest_jobs_snapshot_json TEXT,FOREIGN KEY(task_id) REFERENCES transcode_task(id))`,
		`CREATE TABLE pretranscode_rendition_job(id INTEGER PRIMARY KEY,task_id INTEGER NOT NULL,rendition_id INTEGER,rendition_name TEXT NOT NULL DEFAULT '',status TEXT DEFAULT 'waiting',progress INTEGER DEFAULT 0,output_path TEXT,error_message TEXT,encoder_used TEXT,started_at TIMESTAMP,completed_at TIMESTAMP,created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,available_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,lease_owner TEXT,lease_until TIMESTAMP,config_snapshot_json TEXT,FOREIGN KEY(task_id) REFERENCES transcode_task(id),FOREIGN KEY(rendition_id) REFERENCES preset_rendition(id))`,
		`INSERT INTO transcode_preset VALUES(1)`, `INSERT INTO preset_rendition VALUES(2)`, `INSERT INTO transcode_task VALUES(3,'f')`,
		`INSERT INTO pretranscode_task_meta(task_id,preset_id,ingest_jobs_snapshot_json) VALUES(3,1,'{"jobs":[1]}')`,
		`INSERT INTO pretranscode_rendition_job(id,task_id,rendition_id,rendition_name,status,progress,lease_owner,lease_until,config_snapshot_json) VALUES(4,3,2,'720p','running',41,'owner','2040-01-01','{"snapshot":1}')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	if err := migratePublicationV2(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	conn, _ := db.Conn(context.Background())
	if err := validateEnterprisePublicationChildren(context.Background(), conn); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	var snapshot, owner string
	var progress int
	if err := db.QueryRow(`SELECT config_snapshot_json,lease_owner,progress FROM pretranscode_rendition_job WHERE id=4`).Scan(&snapshot, &owner, &progress); err != nil || snapshot != `{"snapshot":1}` || owner != "owner" || progress != 41 {
		t.Fatalf("preserved=%q/%q/%d err=%v", snapshot, owner, progress, err)
	}
}
