package storage

import (
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
	"knox-media/pkg/hashutil"
)

func TestFindMediaIDByEncryptedPlainPath(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	plain := filepath.Join(dir, "movie.mp4")
	enc := filepath.Join(dir, ".encrypted", "video", "fid.enc")
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status, md5) VALUES (5, 1, 'fid', 't', ?, 'video', 'active', 'abc')`, enc)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (5, ?, 'aa', 'bb', ?, 'encrypted')`, enc, plain)

	id := FindMediaIDByEncryptedPlainPath(db, 1, plain)
	if id != 5 {
		t.Fatalf("id=%d want 5", id)
	}
}

func TestShouldLinkEncryptedPlainPathScan(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	plain := filepath.Join(dir, "photo.jpg")
	enc := filepath.Join(dir, ".encrypted", "image", "fid.enc")
	content := []byte("original-photo-bytes")
	if err := os.WriteFile(plain, content, 0o644); err != nil {
		t.Fatal(err)
	}
	md5, err := hashutil.MD5File(plain)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'photo', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status, md5) VALUES (5, 1, 'fid', 't', ?, 'image', 'active', ?)`, enc, md5)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (5, ?, 'aa', 'bb', ?, 'encrypted')`, enc, plain)

	if !ShouldLinkEncryptedPlainPathScan(db, 5, plain, md5) {
		t.Fatal("expected relink for identical content at plain_path")
	}
	if ShouldLinkEncryptedPlainPathScan(db, 5, plain, "different-md5") {
		t.Fatal("expected no relink when md5 differs")
	}

	newContent := []byte("brand-new-photo-same-name")
	if err := os.WriteFile(plain, newContent, 0o644); err != nil {
		t.Fatal(err)
	}
	newMD5, err := hashutil.MD5File(plain)
	if err != nil {
		t.Fatal(err)
	}
	if ShouldLinkEncryptedPlainPathScan(db, 5, plain, newMD5) {
		t.Fatal("expected no relink for new file reusing encrypted plain_path")
	}

	_, _ = db.Exec(`UPDATE media SET md5 = NULL WHERE id = 5`)
	if err := os.WriteFile(plain, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if !ShouldLinkEncryptedPlainPathScan(db, 5, plain, md5) {
		t.Fatal("expected relink for same plain file when catalog is .enc and stored md5 missing")
	}
}

func TestMediaFileStillPresentAfterEncrypt(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	plain := filepath.Join(dir, "movie.mp4")
	enc := filepath.Join(dir, ".encrypted", "video", "fid.enc")
	if err := os.WriteFile(plain, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (5, 1, 'fid', 't', ?, 'video', 'active')`, enc)
	_, _ = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (5, ?, 'aa', 'bb', ?, 'encrypted')`, enc, plain)

	seen := map[string]struct{}{normalizeScanPath(plain): {}}
	if !MediaFileStillPresentAfterEncrypt(db, 5, enc, seen) {
		t.Fatal("expected encrypted media to be kept when plain path was scanned")
	}
	plainOnlySeen := map[string]struct{}{normalizeScanPath(plain): {}}
	if !MediaFileStillPresentAfterEncrypt(db, 5, enc, plainOnlySeen) {
		t.Fatal("expected encrypted media to be kept when plain file still exists on disk")
	}
}
