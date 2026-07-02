package storage

import (
	"testing"

	"knox-media/internal/store"
)

func TestLibraryEncryptEnabled(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', '/x', 1)`)
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (2, 'lib2', 'video', '/y', 0)`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (10, 1, 'a', 't', '/x/a.mp4', 'video', 'active')`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (11, 2, 'b', 't', '/y/b.mp4', 'video', 'active')`)

	if !LibraryEncryptEnabled(db, 10) {
		t.Fatal("expected library 1 encrypt enabled")
	}
	if LibraryEncryptEnabled(db, 11) {
		t.Fatal("expected library 2 encrypt disabled")
	}
}
