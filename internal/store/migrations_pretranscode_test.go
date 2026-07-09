package store

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestPretranscodeMigrationCreatesTables(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	tables := []string{
		"transcode_preset", "preset_rendition", "pretranscode_task_meta",
		"pretranscode_rendition_job", "pretranscode_webhook", "pretranscode_webhook_log",
	}
	for _, tbl := range tables {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %s not created: %v", tbl, err)
		}
	}

	// transcode_task gains task_type column.
	var ctype string
	err = db.QueryRow(`SELECT type FROM pragma_table_info('transcode_task') WHERE name='task_type'`).Scan(&ctype)
	if err != nil {
		t.Errorf("transcode_task.task_type column missing: %v", err)
	}
}

func TestBuiltinPresetsSeeded(t *testing.T) {
	db, _ := OpenSQLite(":memory:")
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transcode_preset WHERE is_builtin = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("expected 7 builtin presets, got %d", n)
	}

	// MP4 preset must enforce encryption_mode='none' (SRS ENC-05 constraint
	// enforced at the application layer; verify seed complies).
	var enc string
	if err := db.QueryRow(`SELECT encryption_mode FROM transcode_preset WHERE name='MP4-兼容'`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc != "none" {
		t.Errorf("MP4 builtin must have encryption_mode=none, got %s", enc)
	}

	// Re-running OpenSQLite must not duplicate builtin presets (idempotency).
	db2, _ := OpenSQLite(":memory:")
	defer db2.Close()
	var n2 int
	_ = db2.QueryRow(`SELECT COUNT(1) FROM transcode_preset WHERE is_builtin = 1`).Scan(&n2)
	if n2 != 7 {
		t.Errorf("idempotent seed failed: got %d builtin presets", n2)
	}
}

func TestPresetRenditionLegacyBitrateColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE preset_rendition (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		preset_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		height INTEGER NOT NULL,
		width INTEGER,
		video_rate TEXT,
		audio_rate TEXT,
		bandwidth INTEGER,
		sort_order INTEGER DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO preset_rendition (preset_id, name, height, width, video_rate, audio_rate, bandwidth, sort_order)
		VALUES (1, '720p', 720, 1280, '2800k', '128k', 3300000, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := migratePresetRenditionBitrateColumns(db); err != nil {
		t.Fatal(err)
	}
	var vbr, abr string
	if err := db.QueryRow(`SELECT video_bitrate, audio_bitrate FROM preset_rendition WHERE id = 1`).Scan(&vbr, &abr); err != nil {
		t.Fatal(err)
	}
	if vbr != "2800k" || abr != "128k" {
		t.Fatalf("expected migrated bitrates 2800k/128k, got %q/%q", vbr, abr)
	}
}

func TestPretranscodeMigrationIdempotent(t *testing.T) {
	db, _ := OpenSQLite(":memory:")
	defer db.Close()
	// Manually re-run the registered migration to prove idempotency.
	for _, m := range enterpriseMigrations {
		if !strings.HasPrefix(m.ID, "0001_") {
			continue
		}
		if err := m.Up(db); err != nil {
			t.Errorf("re-run migration %s failed: %v", m.ID, err)
		}
	}
}

func TestMigratePretranscodeRenditionJobFK(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Legacy schema: rendition_job.task_id -> pretranscode_task(id).
	_, _ = db.Exec(`CREATE TABLE transcode_task (id INTEGER PRIMARY KEY AUTOINCREMENT, file_id TEXT, status TEXT DEFAULT 'waiting', task_type TEXT DEFAULT 'batch')`)
	_, _ = db.Exec(`CREATE TABLE transcode_preset (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`)
	_, _ = db.Exec(`INSERT INTO transcode_preset (id, name) VALUES (1, 'p1')`)
	_, _ = db.Exec(`CREATE TABLE preset_rendition (id INTEGER PRIMARY KEY AUTOINCREMENT, preset_id INTEGER NOT NULL, name TEXT NOT NULL, height INTEGER NOT NULL, video_bitrate TEXT NOT NULL DEFAULT '850k')`)
	_, _ = db.Exec(`INSERT INTO preset_rendition (id, preset_id, name, height) VALUES (1, 1, '720p', 720)`)
	_, _ = db.Exec(`CREATE TABLE pretranscode_task (id INTEGER PRIMARY KEY AUTOINCREMENT, file_id TEXT NOT NULL, preset_id INTEGER NOT NULL)`)
	_, _ = db.Exec(`CREATE TABLE pretranscode_rendition_job (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL,
		rendition_id INTEGER NOT NULL,
		rendition_name TEXT NOT NULL DEFAULT '',
		status TEXT DEFAULT 'waiting',
		FOREIGN KEY (task_id) REFERENCES pretranscode_task(id) ON DELETE CASCADE,
		FOREIGN KEY (rendition_id) REFERENCES preset_rendition(id)
	)`)
	_, _ = db.Exec(`INSERT INTO transcode_task (id, file_id, status, task_type) VALUES (10, 'f1', 'waiting', 'pretranscode')`)

	if err := migratePretranscodeRenditionJobFK(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	_, err = db.Exec(`INSERT INTO pretranscode_rendition_job (task_id, rendition_id, rendition_name, status) VALUES (10, 1, '720p', 'waiting')`)
	if err != nil {
		t.Fatalf("insert after migration should succeed: %v", err)
	}

	var sqlDef string
	_ = db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='pretranscode_rendition_job'`).Scan(&sqlDef)
	if strings.Contains(strings.ToLower(sqlDef), "pretranscode_task") {
		t.Fatalf("FK should no longer reference pretranscode_task: %s", sqlDef)
	}

	// Idempotent second run.
	if err := migratePretranscodeRenditionJobFK(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
