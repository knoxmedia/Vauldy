package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

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
	assertNoForeignKeyViolations(t, db)
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
