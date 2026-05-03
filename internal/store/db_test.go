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
	assertColumn("library", "cleanup_local_source_after_package")
	assertColumn("package_task", "pipeline_type")
	assertColumn("drm_asset", "kid")
	assertColumn("drm_license_audit", "drm_type")
	assertColumn("drm_key_material", "key_hex")
}
