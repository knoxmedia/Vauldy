package store

import (
	"path/filepath"
	"testing"
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
