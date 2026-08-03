package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenSQLiteAddsDRMColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite err: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	assertColumn := func(table, col string) {
		t.Helper()
		var n int
		err := db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('`+table+`') WHERE name = ?`, col).Scan(&n)
		if err != nil {
			t.Fatalf("check column %s.%s err=%v", table, col, err)
		}
		if n != 1 {
			t.Fatalf("missing column %s.%s", table, col)
		}
	}

	assertColumn("library", "drm_enabled")
	assertColumn("library", "encryption_mode")
	assertColumn("library", "encrypted_assets_enabled")
	assertColumn("library", "encrypted_assets_cleanup_plaintext")
	assertColumn("library", "encrypted_assets_dir_mode")
	assertColumn("library", "encrypted_assets_custom_dir")
	assertColumn("library", "cleanup_local_source_after_package")
	assertColumn("package_task", "pipeline_type")
	assertColumn("drm_asset", "kid")
	assertColumn("drm_license_audit", "drm_type")
	assertColumn("drm_key_material", "key_hex")
}

func TestRecoverStalePhotoTasksResetsRunning(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite err: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
		INSERT INTO library (name, type, path) VALUES ('photos', 'photo', '/tmp/p')
	`)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	var libraryID int64
	if err := db.QueryRow(`SELECT id FROM library LIMIT 1`).Scan(&libraryID); err != nil {
		t.Fatalf("library id: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO media (library_id, file_path, file_type, status) VALUES (?, '/a.jpg', 'image', 'active')
	`, libraryID)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	var mediaID int64
	if err := db.QueryRow(`SELECT id FROM media LIMIT 1`).Scan(&mediaID); err != nil {
		t.Fatalf("media id: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO photo_face_task (media_id, library_id, status, started_at)
		VALUES (?, ?, 'running', CURRENT_TIMESTAMP)
	`, mediaID, libraryID)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	recoverStalePhotoTasks(db)

	var status string
	if err := db.QueryRow(`SELECT status FROM photo_face_task WHERE media_id = ?`, mediaID).Scan(&status); err != nil {
		t.Fatalf("select status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
}

func TestOpenSQLiteDoesNotSeedScheduledTasks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "db.sqlite")
	for i := 0; i < 3; i++ {
		db, err := OpenSQLite(dbPath)
		if err != nil {
			t.Fatalf("OpenSQLite run %d: %v", i+1, err)
		}
		_ = db.Close()
	}
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite final: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM scheduled_task`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("scheduled_task count = %d, want 0 (no auto-seed on startup)", n)
	}
}

func TestOpenSQLiteCreatesResourceControlSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "resource-control.sqlite")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite err: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	assertColumns := func(table string, want []string) {
		t.Helper()
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("columns %s: %v", table, err)
		}
		defer rows.Close()
		got := make(map[string]bool)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan columns %s: %v", table, err)
			}
			got[name] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read columns %s: %v", table, err)
		}
		for _, name := range want {
			if !got[name] {
				t.Errorf("missing column %s.%s", table, name)
			}
		}
	}

	assertColumns("scan_lease", []string{
		"library_id", "scan_task_id", "owner_id", "lease_until", "created_at", "updated_at",
	})
	assertColumns("post_ingest_task", []string{
		"id", "media_id", "scan_task_id", "task_type", "status", "attempts", "max_attempts",
		"available_at", "lease_owner", "lease_until", "last_error", "created_at", "updated_at",
		"started_at", "finished_at",
	})

	for _, index := range []string{"idx_scan_lease_until", "idx_post_ingest_claim", "idx_post_ingest_scan"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&n); err != nil {
			t.Fatalf("check index %s: %v", index, err)
		}
		if n != 1 {
			t.Errorf("missing index %s", index)
		}
	}

	res, err := db.Exec(`INSERT INTO library (name, type, path) VALUES ('library', 'video', '/media')`)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task (library_id) VALUES (?)`, libraryID)
	if err != nil {
		t.Fatalf("insert scan task: %v", err)
	}
	scanTaskID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media (library_id, file_id) VALUES (?, 'invalid-check-test')`, libraryID)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	mediaID, _ := res.LastInsertId()

	if _, err := db.Exec(`INSERT INTO scan_lease (library_id, scan_task_id, owner_id, lease_until) VALUES (?, ?, 'test-owner', CURRENT_TIMESTAMP)`, libraryID, scanTaskID); err != nil {
		t.Fatalf("insert scan lease: %v", err)
	}
	for _, typ := range []string{"poster", "preview", "keyframe", "subtitle", "atrack", "encrypt"} {
		if _, err := db.Exec(`INSERT INTO post_ingest_task (media_id, scan_task_id, task_type) VALUES (?, ?, ?)`, mediaID, scanTaskID, typ); err != nil {
			t.Fatalf("valid task_type %q rejected: %v", typ, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task (media_id, scan_task_id, task_type) VALUES (?, ?, 'invalid')`, mediaID, scanTaskID); err == nil {
		t.Error("invalid task_type was not rejected")
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task (media_id, scan_task_id, task_type, status) VALUES (?, ?, "poster", "invalid")`, mediaID, scanTaskID); err == nil {
		t.Error("invalid status was not rejected")
	}
}

func TestOpenSQLiteVerifiesPragmasAndPool(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pragmas.sqlite")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite err: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout, foreignKeys int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if busyTimeout != 30000 {
		t.Errorf("busy_timeout = %d, want 30000", busyTimeout)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	if got := db.Stats().MaxOpenConnections; got != 8 {
		t.Errorf("MaxOpenConnections = %d, want 8", got)
	}
}

func TestOpenSQLiteMemoryUsesSingleConnection(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite err: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stats := db.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
	if stats.Idle != 1 {
		t.Fatalf("Idle = %d, want 1", stats.Idle)
	}
	if _, err := db.Exec(`INSERT INTO library (name, type, path) VALUES ('memory', 'video', '/memory')`); err != nil {
		t.Fatalf("insert through pooled handle: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM library`).Scan(&count); err != nil {
		t.Fatalf("query through pooled handle: %v", err)
	}
	if count != 1 {
		t.Fatalf("library count = %d, want 1", count)
	}
}

func TestOpenSQLiteConfiguresPragmasOnEveryFileConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "connection-pragmas.sqlite")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite err: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	connections := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		connections = append(connections, conn)
	}
	for i, conn := range connections {
		var busyTimeout, foreignKeys, synchronous int
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil {
			t.Fatalf("connection %d synchronous: %v", i, err)
		}
		if busyTimeout != 30000 || foreignKeys != 1 || synchronous != 1 {
			t.Errorf("connection %d pragmas = busy_timeout:%d foreign_keys:%d synchronous:%d, want 30000/1/1", i, busyTimeout, foreignKeys, synchronous)
		}
	}
}

func TestOpenSQLitePreservesExistingFileURIParameters(t *testing.T) {
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "existing-query.sqlite"))
	dsn := "file:" + dbPath + "?mode=rwc&cache=private"
	db, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("OpenSQLite file URI: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := db.Stats().MaxOpenConnections; got != 8 {
		t.Errorf("MaxOpenConnections = %d, want 8", got)
	}
	var journalMode string
	var busyTimeout, foreignKeys, synchronous int
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if journalMode != "wal" || busyTimeout != 30000 || foreignKeys != 1 || synchronous != 1 {
		t.Fatalf("pragmas = journal:%s busy:%d foreign:%d sync:%d, want wal/30000/1/1", journalMode, busyTimeout, foreignKeys, synchronous)
	}
}

func TestOpenSQLiteMigratesMediaSortColumnsAndPlans(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "media-sort-open.sqlite")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','/photos')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_type,created_at,meta_json,created_at_sort,photo_taken_at,photo_place_id) VALUES(1,1,'image','2026-07-18 01:02:03','{"photo":{"taken_at":"2026-07-18T01:00:00Z","place_id":"p"}}','2026-07-18T01:02:03.000000Z','2026-07-18T01:00:00.000000Z','p')`); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenSQLite(dbPath)
	defer db.Close()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var created, taken, place string
	if err := db.QueryRow(`SELECT created_at_sort,photo_taken_at,photo_place_id FROM media WHERE id=1`).Scan(&created, &taken, &place); err != nil {
		t.Fatal(err)
	}
	if created == "" || taken == "" || place != "p" {
		t.Fatalf("sort columns=(%q,%q,%q)", created, taken, place)
	}
	plans := []struct{ name, query, want string }{
		{"created", `EXPLAIN QUERY PLAN SELECT id FROM media WHERE library_id=1 ORDER BY created_at_sort DESC,id DESC LIMIT 20`, "idx_media_library_created_id"},
		{"typed-created", `EXPLAIN QUERY PLAN SELECT id FROM media WHERE library_id=1 AND file_type='image' ORDER BY created_at_sort DESC,id DESC LIMIT 20`, "idx_media_library_type_created_id"},
		{"taken", `EXPLAIN QUERY PLAN SELECT id FROM media WHERE library_id=1 AND file_type='image' ORDER BY photo_taken_at DESC,id DESC LIMIT 20`, "idx_media_library_type_photo_taken_id"},
		{"place", `EXPLAIN QUERY PLAN SELECT id FROM media WHERE library_id=1 AND file_type='image' AND photo_place_id='p' ORDER BY photo_taken_at DESC,id DESC LIMIT 20`, "idx_media_library_type_photo_place_taken_id"},
	}
	for _, tt := range plans {
		rows, err := db.Query(tt.query)
		if err != nil {
			t.Fatal(err)
		}
		detail := ""
		for rows.Next() {
			var id, parent, unused int
			var line string
			if err := rows.Scan(&id, &parent, &unused, &line); err != nil {
				t.Fatal(err)
			}
			detail += line
		}
		rows.Close()
		if !strings.Contains(detail, tt.want) || strings.Contains(detail, "USE TEMP B-TREE") {
			t.Errorf("%s plan=%q want index %s without temp sort", tt.name, detail, tt.want)
		}
	}
}

func TestOpenSQLiteContextHonorsPreCancelledMigration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db, err := OpenSQLiteContext(ctx, filepath.Join(t.TempDir(), "cancelled.sqlite"))
	if db != nil {
		_ = db.Close()
		t.Fatal("expected nil database")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenSQLiteContextCancelsWhileDatabaseLocked(t *testing.T) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("knox-open-cancel-%d.sqlite", time.Now().UnixNano()))
	defer os.Remove(path)
	seed, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	locker, err := sql.Open("sqlite", appendSQLitePragmas(path))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	started := time.Now()
	db, err := OpenSQLiteContext(ctx, path)
	if db != nil {
		_ = db.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
	_ = conn.Close()
	_ = locker.Close()
}

func TestIsMemorySQLitePathVariants(t *testing.T) {
	for _, path := range []string{":memory:", "file::memory:?cache=shared", "FILE:Named?MODE=MEMORY&CACHE=shared", "file:name?cache=shared&mode=memory"} {
		if !isMemorySQLitePath(path) {
			t.Errorf("isMemorySQLitePath(%q)=false", path)
		}
	}
	for _, path := range []string{"db.sqlite", "file:db.sqlite?mode=rwc", "file:name?mode=memoryish"} {
		if isMemorySQLitePath(path) {
			t.Errorf("isMemorySQLitePath(%q)=true", path)
		}
	}
}

func TestOpenSQLiteURIMemoryPreservesSchemaAndUsesOneConnection(t *testing.T) {
	for _, dsn := range []string{"file::memory:?cache=shared", "file:task2-memory?mode=memory&cache=shared"} {
		db, err := OpenSQLite(dsn)
		if err != nil {
			t.Fatalf("%s: %v", dsn, err)
		}
		if db.Stats().MaxOpenConnections != 1 {
			t.Errorf("%s max=%d", dsn, db.Stats().MaxOpenConnections)
		}
		if _, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('kept','photo','/')`); err != nil {
			t.Fatalf("%s insert: %v", dsn, err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM library WHERE name='kept'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s schema/data lost n=%d err=%v", dsn, n, err)
		}
		_ = db.Close()
	}
}

func TestOpenSQLiteConcurrentFreshDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.sqlite")
	start := make(chan struct{})
	dbs := make(chan *sql.DB, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; db, err := OpenSQLiteContext(context.Background(), path); dbs <- db; errs <- err }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		db := <-dbs
		err := <-errs
		if err != nil {
			t.Errorf("open %d: %v", i, err)
		}
		if db != nil {
			_ = db.Close()
		}
	}
}

func TestOpenSQLiteRetriesBootstrapWritesUntilLockReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-retry.sqlite")
	seed, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	locker, err := sql.Open("sqlite", appendSQLitePragmas(path))
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	conn, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(400 * time.Millisecond)
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		close(released)
	}()
	db, err := OpenSQLiteContext(context.Background(), path)
	<-released
	if err != nil {
		t.Fatal(err)
	}
	if db != nil {
		_ = db.Close()
	}
}

func TestOpenSQLiteReturnsOnlyAfterMediaSortInvariantComplete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "startup-invariant.sqlite")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','/photos')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_type,created_at,meta_json,created_at_sort,photo_taken_at) VALUES(1,1,'image','2026-07-18 01:02:03','{}',NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenSQLiteContext(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var completed, nulls int
	if err := db.QueryRow(`SELECT completed FROM media_sort_migration_state WHERE version=1`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE created_at_sort IS NULL OR (file_type='image' AND photo_taken_at IS NULL)`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if completed != 1 || nulls != 0 {
		t.Fatalf("OpenSQLite returned before invariant: completed=%d nulls=%d", completed, nulls)
	}
	var indexCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_media_library_type_photo_timeline_id'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatalf("timeline index count=%d", indexCount)
	}
}

func TestOpenSQLiteUpgradesLegacyPhotoFaceRepairStateColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-repair-state.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE photo_face_thumb_repair_state(name TEXT PRIMARY KEY,phase TEXT NOT NULL,last_person_id INTEGER NOT NULL DEFAULT 0,last_face_id INTEGER NOT NULL DEFAULT 0,updated_at TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, column := range []string{"completed_at", "next_audit_at"} {
		var n int
		if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('photo_face_thumb_repair_state') WHERE name=?`, column).Scan(&n); err != nil || n != 1 {
			t.Fatalf("column %s count=%d err=%v", column, n, err)
		}
	}
}

func TestEnsureColumnContextPropagatesAlterError(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "alter-error.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE base(id INTEGER); CREATE VIEW broken AS SELECT id FROM base`); err != nil {
		t.Fatal(err)
	}
	err = ensureColumnContext(context.Background(), db, "broken", "new_col", "TEXT")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "view") {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenSQLiteContextUpgradesLegacyPostIngestSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-post-ingest.sqlite")
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("create current fixture: %v", err)
	}
	if _, err := db.Exec(`
DROP INDEX idx_post_ingest_run;
DROP INDEX idx_post_ingest_step;
ALTER TABLE post_ingest_task RENAME TO post_ingest_task_current;
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
DROP TABLE post_ingest_task_current;
CREATE INDEX idx_post_ingest_claim ON post_ingest_task(status,available_at,lease_until,created_at);
CREATE INDEX idx_post_ingest_scan ON post_ingest_task(scan_task_id,status);`); err != nil {
		_ = db.Close()
		t.Fatalf("install legacy post_ingest_task: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = OpenSQLiteContext(context.Background(), path)
	if err != nil {
		t.Fatalf("upgrade legacy database: %v", err)
	}
	defer db.Close()
	for _, column := range []string{"ingest_run_id", "ingest_step_id", "generation"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('post_ingest_task') WHERE name=?`, column).Scan(&count); err != nil || count != 1 {
			t.Fatalf("column %s count=%d err=%v", column, count, err)
		}
	}
}

func TestEnsureLibraryProcessingColumnAlterFailureRecheck(t *testing.T) {
	alterErr := errors.New("duplicate column name: subtitle_extract")
	recheckErr := errors.New("pragma recheck failed")
	compatible := libraryProcessingColumnInfo{typ: "INTEGER", notNull: 1, defaultValue: sql.NullString{String: "0", Valid: true}}
	incompatible := libraryProcessingColumnInfo{typ: "TEXT", notNull: 1, defaultValue: sql.NullString{String: "0", Valid: true}}

	for _, tc := range []struct {
		name        string
		metadata    []libraryProcessingColumnInfo
		metadataErr []error
		wantErr     []string
	}{
		{name: "compatible competing addition", metadata: []libraryProcessingColumnInfo{{}, compatible}, metadataErr: []error{sql.ErrNoRows, nil}},
		{name: "incompatible competing addition", metadata: []libraryProcessingColumnInfo{{}, incompatible}, metadataErr: []error{sql.ErrNoRows, nil}, wantErr: []string{"incompatible library processing column subtitle_extract"}},
		{name: "column remains absent", metadata: []libraryProcessingColumnInfo{{}, {}}, metadataErr: []error{sql.ErrNoRows, sql.ErrNoRows}, wantErr: []string{"duplicate column name", "column still absent"}},
		{name: "metadata recheck fails", metadata: []libraryProcessingColumnInfo{{}, {}}, metadataErr: []error{sql.ErrNoRows, recheckErr}, wantErr: []string{"duplicate column name", "recheck metadata", "pragma recheck failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadataCalls := 0
			alterCalls := 0
			err := ensureLibraryProcessingColumnWith(
				context.Background(),
				"subtitle_extract",
				func(context.Context, string) (libraryProcessingColumnInfo, error) {
					i := metadataCalls
					metadataCalls++
					return tc.metadata[i], tc.metadataErr[i]
				},
				func(context.Context, string) error { alterCalls++; return alterErr },
			)
			if metadataCalls != 2 || alterCalls != 1 {
				t.Fatalf("metadata calls=%d alter calls=%d", metadataCalls, alterCalls)
			}
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error=%q missing %q", err, want)
				}
			}
		})
	}
}
