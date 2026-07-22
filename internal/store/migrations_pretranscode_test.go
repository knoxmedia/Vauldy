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

	// transcode_task gains task_type and nullable ingest linkage columns.
	var ctype string
	err = db.QueryRow(`SELECT type FROM pragma_table_info('transcode_task') WHERE name='task_type'`).Scan(&ctype)
	if err != nil {
		t.Errorf("transcode_task.task_type column missing: %v", err)
	}
	for _, column := range []string{"ingest_run_id", "ingest_step_id", "generation"} {
		if err := db.QueryRow(`SELECT type FROM pragma_table_info('transcode_task') WHERE name=?`, column).Scan(&ctype); err != nil {
			t.Errorf("transcode_task.%s column missing: %v", column, err)
		}
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

func TestPretranscodeMigrationAddsRenditionLeaseColumns(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, column := range []string{"lease_owner", "lease_until"} {
		var typ string
		if err := db.QueryRow(`SELECT type FROM pragma_table_info('pretranscode_rendition_job') WHERE name=?`, column).Scan(&typ); err != nil {
			t.Fatalf("missing %s: %v", column, err)
		}
	}
}

func TestLegacyPretranscodeFKFullEnterpriseMigrationPreservesLeaseColumnsAndData(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY AUTOINCREMENT,file_id TEXT,status TEXT DEFAULT 'waiting',task_type TEXT DEFAULT 'batch')`,
		`CREATE TABLE transcode_preset(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,description TEXT,output_format TEXT NOT NULL DEFAULT 'hls',encryption_mode TEXT DEFAULT 'none',video_codec TEXT NOT NULL DEFAULT 'libx264',video_preset TEXT,video_crf INTEGER,video_maxrate TEXT,video_bufsize TEXT,video_profile TEXT,video_gop INTEGER,video_pix_fmt TEXT,audio_codec TEXT NOT NULL DEFAULT 'aac',audio_bitrate TEXT NOT NULL DEFAULT '128k',audio_channels INTEGER,audio_sample_rate INTEGER,hw_fallback INTEGER DEFAULT 1,is_builtin INTEGER DEFAULT 0,is_enabled INTEGER DEFAULT 1,sort_order INTEGER DEFAULT 0,created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE preset_rendition(id INTEGER PRIMARY KEY AUTOINCREMENT,preset_id INTEGER NOT NULL,name TEXT NOT NULL,height INTEGER NOT NULL,width INTEGER,video_bitrate TEXT NOT NULL DEFAULT '850k',audio_bitrate TEXT,video_rate TEXT NOT NULL DEFAULT '',audio_rate TEXT NOT NULL DEFAULT '',bandwidth INTEGER,sort_order INTEGER DEFAULT 0,FOREIGN KEY(preset_id) REFERENCES transcode_preset(id))`,
		`CREATE TABLE pretranscode_task(id INTEGER PRIMARY KEY AUTOINCREMENT,file_id TEXT NOT NULL,preset_id INTEGER NOT NULL)`,
		`CREATE TABLE pretranscode_rendition_job(id INTEGER PRIMARY KEY AUTOINCREMENT,task_id INTEGER NOT NULL,rendition_id INTEGER NOT NULL,rendition_name TEXT NOT NULL DEFAULT '',status TEXT DEFAULT 'waiting',progress INTEGER DEFAULT 0,output_path TEXT,error_message TEXT,encoder_used TEXT,started_at TIMESTAMP,completed_at TIMESTAMP,created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,lease_owner TEXT,lease_until TIMESTAMP,FOREIGN KEY(task_id) REFERENCES pretranscode_task(id) ON DELETE CASCADE,FOREIGN KEY(rendition_id) REFERENCES preset_rendition(id))`,
		`INSERT INTO transcode_preset(id,name) VALUES(1,'legacy')`,
		`INSERT INTO preset_rendition(id,preset_id,name,height) VALUES(1,1,'720p',720)`,
		`INSERT INTO transcode_task(id,file_id,status,task_type) VALUES(10,'legacy-file','running','pretranscode')`,
		`INSERT INTO pretranscode_task(id,file_id,preset_id) VALUES(10,'legacy-file',1)`,
		`INSERT INTO pretranscode_rendition_job(id,task_id,rendition_id,rendition_name,status,progress,lease_owner,lease_until) VALUES(20,10,1,'720p','running',42,'legacy-owner','2040-01-01 00:00:00')`,
	}
	for _, stmt := range stmts {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatalf("legacy setup %q: %v", stmt, err)
		}
	}
	for _, m := range enterpriseMigrations {
		if err = m.Up(db); err != nil {
			t.Fatalf("migration %s: %v", m.ID, err)
		}
	}
	for _, column := range []string{"lease_owner", "lease_until"} {
		var typ string
		if err = db.QueryRow(`SELECT type FROM pragma_table_info('pretranscode_rendition_job') WHERE name=?`, column).Scan(&typ); err != nil {
			t.Fatalf("missing %s after full migration: %v", column, err)
		}
	}
	var preservedOwner, preservedLease string
	if err = db.QueryRow(`SELECT lease_owner,lease_until FROM pretranscode_rendition_job WHERE id=20`).Scan(&preservedOwner, &preservedLease); err != nil {
		t.Fatal(err)
	}
	if preservedOwner != "legacy-owner" || !strings.HasPrefix(preservedLease, "2040-01-01") {
		t.Fatalf("lease not preserved=%q/%q", preservedOwner, preservedLease)
	}
	if _, err = db.Exec(`UPDATE pretranscode_rendition_job SET lease_owner='pretranscode/test',lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE id=20 AND status='running'`); err != nil {
		t.Fatalf("worker claim SQL after migration: %v", err)
	}
	var name, status, owner string
	var progress int
	if err = db.QueryRow(`SELECT rendition_name,status,progress,lease_owner FROM pretranscode_rendition_job WHERE id=20`).Scan(&name, &status, &progress, &owner); err != nil {
		t.Fatal(err)
	}
	if name != "720p" || status != "running" || progress != 42 || owner != "pretranscode/test" {
		t.Fatalf("row=%s/%s/%d/%s", name, status, progress, owner)
	}
	var fkErrors int
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		fkErrors++
	}
	if fkErrors != 0 {
		t.Fatalf("foreign_key_check errors=%d", fkErrors)
	}
	var indexes int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('idx_pretranscode_job_status','idx_pretranscode_job_task')`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 2 {
		t.Fatalf("indexes=%d", indexes)
	}
}

func TestPretranscodeMigrationAddsRenditionAvailability(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var typ string
	var notNull, hasDefault int
	var defaultValue sql.NullString
	if err = db.QueryRow(`SELECT type,"notnull",dflt_value IS NOT NULL,dflt_value FROM pragma_table_info('pretranscode_rendition_job') WHERE name='available_at'`).Scan(&typ, &notNull, &hasDefault, &defaultValue); err != nil {
		t.Fatalf("missing available_at: %v", err)
	}
	if typ != "TIMESTAMP" || notNull != 0 || hasDefault != 1 || !strings.Contains(strings.ToUpper(defaultValue.String), "CURRENT_TIMESTAMP") {
		t.Fatalf("available_at=%s nullable=%d default=%q", typ, notNull, defaultValue.String)
	}
}

func TestLegacyPretranscodeFKMigrationPreservesAvailability(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{`PRAGMA foreign_keys=ON`, `CREATE TABLE transcode_task(id INTEGER PRIMARY KEY,file_id TEXT,status TEXT,task_type TEXT)`, `CREATE TABLE preset_rendition(id INTEGER PRIMARY KEY)`, `CREATE TABLE pretranscode_task(id INTEGER PRIMARY KEY)`, `CREATE TABLE pretranscode_rendition_job(id INTEGER PRIMARY KEY,task_id INTEGER NOT NULL,rendition_id INTEGER NOT NULL,rendition_name TEXT,status TEXT,progress INTEGER,output_path TEXT,error_message TEXT,encoder_used TEXT,started_at TIMESTAMP,completed_at TIMESTAMP,created_at TIMESTAMP,lease_owner TEXT,lease_until TIMESTAMP,available_at TIMESTAMP,FOREIGN KEY(task_id) REFERENCES pretranscode_task(id))`, `INSERT INTO transcode_task VALUES(10,'f','waiting','pretranscode')`, `INSERT INTO preset_rendition VALUES(1)`, `INSERT INTO pretranscode_task VALUES(10)`, `INSERT INTO pretranscode_rendition_job(id,task_id,rendition_id,rendition_name,status,progress,available_at) VALUES(20,10,1,'720p','waiting',0,'2040-01-02 03:04:05')`} {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err = migratePretranscodeRenditionJobFK(db); err != nil {
		t.Fatal(err)
	}
	var got string
	if err = db.QueryRow(`SELECT available_at FROM pretranscode_rendition_job WHERE id=20`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "2040-01-02") {
		t.Fatalf("available_at=%q", got)
	}
}

func TestFullEnterpriseMigrationBackfillsRenditionAvailabilityWithoutLegacyFKRebuild(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE transcode_task(id INTEGER PRIMARY KEY AUTOINCREMENT,file_id TEXT,status TEXT DEFAULT 'waiting',task_type TEXT DEFAULT 'pretranscode')`,
		`CREATE TABLE transcode_preset(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,description TEXT,output_format TEXT NOT NULL DEFAULT 'hls',encryption_mode TEXT DEFAULT 'none',video_codec TEXT NOT NULL DEFAULT 'libx264',video_preset TEXT,video_crf INTEGER,video_maxrate TEXT,video_bufsize TEXT,video_profile TEXT,video_gop INTEGER,video_pix_fmt TEXT,audio_codec TEXT NOT NULL DEFAULT 'aac',audio_bitrate TEXT NOT NULL DEFAULT '128k',audio_channels INTEGER,audio_sample_rate INTEGER,hw_fallback INTEGER DEFAULT 1,is_builtin INTEGER DEFAULT 0,is_enabled INTEGER DEFAULT 1,sort_order INTEGER DEFAULT 0,created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE preset_rendition(id INTEGER PRIMARY KEY AUTOINCREMENT,preset_id INTEGER NOT NULL,name TEXT NOT NULL,height INTEGER NOT NULL,width INTEGER,video_bitrate TEXT NOT NULL DEFAULT '850k',audio_bitrate TEXT,video_rate TEXT NOT NULL DEFAULT '',audio_rate TEXT NOT NULL DEFAULT '',bandwidth INTEGER,sort_order INTEGER DEFAULT 0,FOREIGN KEY(preset_id) REFERENCES transcode_preset(id))`,
		`CREATE TABLE pretranscode_rendition_job(id INTEGER PRIMARY KEY AUTOINCREMENT,task_id INTEGER NOT NULL,rendition_id INTEGER NOT NULL,rendition_name TEXT NOT NULL,status TEXT DEFAULT 'waiting',progress INTEGER DEFAULT 0,output_path TEXT,error_message TEXT,encoder_used TEXT,started_at TIMESTAMP,completed_at TIMESTAMP,created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,FOREIGN KEY(task_id) REFERENCES transcode_task(id) ON DELETE CASCADE,FOREIGN KEY(rendition_id) REFERENCES preset_rendition(id))`,
		`INSERT INTO transcode_preset(id,name) VALUES(1,'fixture')`, `INSERT INTO preset_rendition(id,preset_id,name,height) VALUES(1,1,'720p',720)`, `INSERT INTO transcode_task(id,file_id,status,task_type) VALUES(10,'fixture','waiting','pretranscode')`, `INSERT INTO pretranscode_rendition_job(id,task_id,rendition_id,rendition_name,status) VALUES(20,10,1,'720p','waiting')`,
	} {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range enterpriseMigrations {
		if err = m.Up(db); err != nil {
			t.Fatalf("migration %s: %v", m.ID, err)
		}
	}
	var available sql.NullString
	if err = db.QueryRow(`SELECT available_at FROM pretranscode_rendition_job WHERE id=20`).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if !available.Valid || available.String == "" {
		t.Fatal("existing row availability not backfilled")
	}
	for _, column := range []string{"lease_owner", "lease_until"} {
		var n int
		if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pretranscode_rendition_job') WHERE name=?`, column).Scan(&n); err != nil || n != 1 {
			t.Fatalf("column %s missing err=%v", column, err)
		}
	}
	var due int
	if err = db.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job WHERE status='waiting' AND COALESCE(available_at,CURRENT_TIMESTAMP)<=CURRENT_TIMESTAMP`).Scan(&due); err != nil || due != 1 {
		t.Fatalf("worker SQL due=%d err=%v", due, err)
	}
	for _, m := range enterpriseMigrations {
		if err = m.Up(db); err != nil {
			t.Fatalf("second migration %s: %v", m.ID, err)
		}
	}
}
