package storage

import (
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestPlaintextSourceAvailablePlainVideo(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "movie.mp4")
	if err := os.WriteFile(plain, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (9, 1, 'f', 't', ?, 'video', 'active')`, plain)

	if !PlaintextSourceAvailable(db, 9, 1, plain) {
		t.Fatal("expected plain video to be available")
	}
}

func TestPlaintextSourceAvailableEncryptedWithPlain(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "movie.mp4")
	enc := filepath.Join(dir, "movie.enc")
	if err := os.WriteFile(plain, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, append([]byte("9527"), []byte("enc")...), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (9, 1, 'f', 't', ?, 'video', 'active')`, enc)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, plain_path, wrapped_dek, iv, status)
		VALUES (9, ?, ?, '00', '00', 'encrypted')`, enc, plain)

	if !PlaintextSourceAvailable(db, 9, 1, enc) {
		t.Fatal("expected encrypted video with plaintext to be available")
	}
}

func TestPlaintextSourceAvailableEncryptedPlainDeleted(t *testing.T) {
	dir := t.TempDir()
	enc := filepath.Join(dir, "movie.enc")
	if err := os.WriteFile(enc, append([]byte("9527"), []byte("enc")...), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (9, 1, 'f', 't', ?, 'video', 'active')`, enc)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, plain_path, wrapped_dek, iv, status)
		VALUES (9, ?, 'C:\\missing.mp4', '00', '00', 'encrypted')`, enc)

	if PlaintextSourceAvailable(db, 9, 1, enc) {
		t.Fatal("expected encrypted video without plaintext to be unavailable")
	}
}
