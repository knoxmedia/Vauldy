package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenSQLiteFreshIngestEntrySchemaMatchesCanonical(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for table, ddl := range map[string]string{
		"filesystem_event_inbox": canonicalFilesystemEventInboxSchema,
		"ingest_item":            canonicalIngestItemSchema,
	} {
		if err := exactPublicationTable(context.Background(), conn, table, ddl); err != nil {
			t.Fatalf("fresh %s: %v", table, err)
		}
	}
	for table, indexes := range ingestEntryManagedIndexes {
		for name, ddl := range indexes {
			unique, partial := 0, 0
			if name == "idx_ingest_item_active_path" {
				unique, partial = 1, 1
			}
			if err := requirePublicationIndex(context.Background(), conn, table, name, ddl, unique, partial); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}
	if err := exactPublicationTable(context.Background(), conn, "media_ingest_run", canonicalMediaIngestRunV2Schema()); err != nil {
		t.Fatal(err)
	}
}

func TestIngestEntryConstraintsAndPreservingForeignKeys(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(900,'l','movie','/l')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO ingest_item(id,submission_key,source,library_id,canonical_path,path_key,state) VALUES(901,'stable','upload',900,'/l/a','/l/a','waiting')`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state) VALUES('stable','upload',900,'/l/b','/l/b','waiting')`,
		`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state) VALUES('other','upload',900,'/l/a','/l/a','running')`,
		`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state,lease_owner) VALUES('owner-only','upload',900,'/l/c','/l/c','running','worker')`,
		`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state,lease_until) VALUES('lease-only','upload',900,'/l/d','/l/d','running',CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("constraint accepted: %s", statement)
		}
	}
	if _, err = db.Exec(`INSERT INTO filesystem_event_inbox(id,library_id,raw_path,event_ops,observed_at,status,consumed_ingest_item_id) VALUES(902,900,'/l/a','write',CURRENT_TIMESTAMP,'consumed',901)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM library WHERE id=900`); err == nil {
		t.Fatal("library deletion destroyed acknowledged work")
	}
	assertNoForeignKeyViolations(t, db)
}

func TestMigrateIngestEntryPreservesLegacyRowsAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entry.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE library(id INTEGER PRIMARY KEY); CREATE TABLE media(id INTEGER PRIMARY KEY); CREATE TABLE scan_task(id INTEGER PRIMARY KEY); CREATE TABLE media_ingest_run(id INTEGER PRIMARY KEY);` +
		strings.Replace(canonicalIngestItemSchema, "CREATE TABLE ingest_item", "CREATE TABLE ingest_item", 1) + `;` +
		strings.Replace(canonicalFilesystemEventInboxSchema, "CREATE TABLE filesystem_event_inbox", "CREATE TABLE filesystem_event_inbox", 1) + `;` +
		`INSERT INTO library(id) VALUES(1); INSERT INTO ingest_item(id,submission_key,source,library_id,canonical_path,path_key,state,last_error) VALUES(5,'legacy','upload',1,'C:/a','c:/a','failed','keep'); INSERT INTO filesystem_event_inbox(id,library_id,raw_path,event_ops,observed_at,status,consumed_ingest_item_id) VALUES(6,1,'C:/a','write',CURRENT_TIMESTAMP,'consumed',5);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	for i := 0; i < 2; i++ {
		opened, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		opened.SetMaxOpenConns(1)
		if _, err = opened.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			t.Fatal(err)
		}
		if err = migrateIngestEntry(context.Background(), opened); err != nil {
			t.Fatal(err)
		}
		var key, last string
		var consumed int64
		if err := opened.QueryRow(`SELECT submission_key,last_error FROM ingest_item WHERE id=5`).Scan(&key, &last); err != nil || key != "legacy" || last != "keep" {
			t.Fatalf("item %q/%q: %v", key, last, err)
		}
		if err := opened.QueryRow(`SELECT consumed_ingest_item_id FROM filesystem_event_inbox WHERE id=6`).Scan(&consumed); err != nil || consumed != 5 {
			t.Fatalf("event link=%d: %v", consumed, err)
		}
		assertNoForeignKeyViolations(t, opened)
		opened.Close()
	}
}

func TestMigrateIngestEntryFaultRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE library(id INTEGER PRIMARY KEY); CREATE TABLE media(id INTEGER PRIMARY KEY); CREATE TABLE scan_task(id INTEGER PRIMARY KEY);`); err != nil {
		t.Fatal(err)
	}
	old := ingestEntryMigrationHook
	ingestEntryMigrationHook = func(stage string) error {
		if stage == "after-create" {
			return fmt.Errorf("injected")
		}
		return nil
	}
	t.Cleanup(func() { ingestEntryMigrationHook = old })
	if err := migrateIngestEntry(context.Background(), db); err == nil {
		t.Fatal("expected injected failure")
	}
	var item int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ingest_item'`).Scan(&item)
	if item != 0 {
		t.Fatal("rollback left ingest item")
	}
	var inbox int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='filesystem_event_inbox'`).Scan(&inbox)
	if inbox != 0 {
		t.Fatal("rollback left inbox")
	}
}

func TestOpenSQLiteConcurrentIngestEntryMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	const n = 6
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := OpenSQLite(path)
			if err == nil {
				err = foreignKeyCheckExecutor(context.Background(), db)
				db.Close()
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
}

func TestMediaIngestRunIngestItemForeignKeyMetadataAndDelete(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`PRAGMA foreign_key_list(media_ingest_run)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if table == "ingest_item" && from == "ingest_item_id" && to == "id" && strings.EqualFold(onDelete, "SET NULL") {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("media_ingest_run.ingest_item_id SET NULL foreign key missing")
	}
	if _, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(920,'l','movie','/l'); INSERT INTO media(id,library_id,file_id) VALUES(921,920,'fk'); INSERT INTO ingest_item(id,submission_key,source,library_id,canonical_path,path_key,state) VALUES(922,'fk','upload',920,'/l/fk','/l/fk','done'); INSERT INTO media_ingest_run(id,media_id,generation,ingest_item_id,reason,status,config_snapshot_json) VALUES(923,921,1,922,'upload','published','{}'); DELETE FROM ingest_item WHERE id=922`); err != nil {
		t.Fatal(err)
	}
	var linked sql.NullInt64
	if err := db.QueryRow(`SELECT ingest_item_id FROM media_ingest_run WHERE id=923`).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked.Valid {
		t.Fatalf("delete did not clear run linkage: %d", linked.Int64)
	}
}

func TestIngestEntryStoredSQLIsByteCanonical(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	objects := map[string]string{
		"ingest_item":            canonicalIngestItemSchema,
		"filesystem_event_inbox": canonicalFilesystemEventInboxSchema,
	}
	for _, indexes := range ingestEntryManagedIndexes {
		for name, ddl := range indexes {
			objects[name] = ddl
		}
	}
	for name, want := range objects {
		var got string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if normalizeSQLiteStoredSQL(got) != normalizeSQLiteStoredSQL(want) {
			t.Fatalf("%s stored SQL drift\nGOT:\n%s\nWANT:\n%s", name, got, want)
		}
	}
}

func TestMigrateIngestEntryRejectsFormattingAndClauseDriftWithoutMutation(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE filesystem_event_inbox; DROP TABLE ingest_item`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(strings.Replace(canonicalIngestItemSchema, "CREATE TABLE ingest_item (", "CREATE TABLE ingest_item (  ", 1)); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestEntry(context.Background(), db); err == nil || !strings.Contains(err.Error(), "stored SQL drift") {
		t.Fatalf("format drift accepted: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ingest_item`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("schema mutated count=%d err=%v", count, err)
	}
	var inbox int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='filesystem_event_inbox'`).Scan(&inbox)
	if inbox != 0 {
		t.Fatal("failure did not roll back inbox creation")
	}
}

func TestIngestItemRejectsBlankPathsAndInvalidRunningLease(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(930,'l','movie','/l')`); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state) VALUES('blank-path','upload',930,' ','key','waiting')`,
		`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state) VALUES('blank-key','upload',930,'/l/a','  ','waiting')`,
		`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state,lease_owner,lease_until) VALUES('blank-owner','upload',930,'/l/a','a','running',' ',CURRENT_TIMESTAMP)`,
		`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state) VALUES('no-lease','upload',930,'/l/b','b','running')`,
		`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state,lease_owner,lease_until) VALUES('waiting-lease','upload',930,'/l/c','c','waiting','worker',CURRENT_TIMESTAMP)`,
	}
	for _, q := range cases {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("invalid item accepted: %s", q)
		}
	}
	if _, err := db.Exec(`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state,lease_owner,lease_until) VALUES('valid-run','upload',930,'/l/d','d','running','worker',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateIngestEntryMalformedLegacyFailsClosedWithoutMutation(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE filesystem_event_inbox; DROP TABLE ingest_item`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ingest_item(id INTEGER PRIMARY KEY, legacy_value TEXT); INSERT INTO ingest_item VALUES(1,'keep')`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestEntry(context.Background(), db); err == nil {
		t.Fatal("malformed schema accepted")
	}
	var value string
	if err := db.QueryRow(`SELECT legacy_value FROM ingest_item WHERE id=1`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("legacy row mutated %q err=%v", value, err)
	}
	var inbox int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='filesystem_event_inbox'`).Scan(&inbox)
	if inbox != 0 {
		t.Fatal("malformed failure created inbox")
	}
}

func TestMigrateIngestEntryUpgradesExactTask1SchemaPreservingRows(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE filesystem_event_inbox; DROP TABLE ingest_item`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'task1','movie','/l'); ` + task1IngestItemSchema + `; INSERT INTO ingest_item(id,submission_key,source,library_id,canonical_path,path_key,size_bytes,mtime_ns,sha256,state,attempts,retry_round,last_error) VALUES(77,'legacy-task1','upload',1,'/l/a','/l/a',10,20,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','failed',2,3,'preserve')`); err != nil {
		t.Fatal(err)
	}
	if err := migrateIngestEntry(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var key, last string
	var attempts, round int
	if err := db.QueryRow(`SELECT submission_key,last_error,attempts,retry_round FROM ingest_item WHERE id=77`).Scan(&key, &last, &attempts, &round); err != nil {
		t.Fatal(err)
	}
	if key != "legacy-task1" || last != "preserve" || attempts != 2 || round != 3 {
		t.Fatalf("preserved %q %q %d %d", key, last, attempts, round)
	}
	if err := migrateIngestEntry(context.Background(), db); err != nil {
		var stored string
		_ = db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='ingest_item'`).Scan(&stored)
		t.Fatalf("reopen migration: %v SQL=%s", err, stored)
	}
	for _, column := range []string{"superseded_owner", "superseded_lease_until", "transition_token"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('ingest_item') WHERE name=?`, column).Scan(&n); err != nil || n != 1 {
			t.Fatalf("column %s n=%d err=%v", column, n, err)
		}
	}
}
