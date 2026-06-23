package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestIsISOBaseMedia(t *testing.T) {
	cases := map[string]bool{
		"clip.mp4": true,
		"clip.MOV": true,
		"clip.m4v": true,
		"clip.mkv": false,
		"clip.ts":  false,
		"":         false,
	}
	for name, want := range cases {
		if got := isISOBaseMedia(name); got != want {
			t.Fatalf("%q: got %v want %v", name, got, want)
		}
	}
}

func TestIsISOBaseMediaCatalog(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	enc := filepath.Join(dir, "movie.enc")
	plain := filepath.Join(dir, "movie.mp4")
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'video', ?)`, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, format, status) VALUES (9, 1, 'f', 't', ?, 'video', 'mp4', 'active')`, enc); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (9, ?, 'aa', 'bb', ?, 'encrypted')`, enc, plain); err != nil {
		t.Fatal(err)
	}

	if !isISOBaseMediaCatalog(db, 9, enc) {
		t.Fatal("expected ISO from format/plain_path")
	}
	mkvEnc := filepath.Join(dir, "other.enc")
	mkvPlain := filepath.Join(dir, "other.mkv")
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, format, status) VALUES (10, 1, 'g', 't', ?, 'video', 'mkv', 'active')`, mkvEnc); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (10, ?, 'aa', 'bb', ?, 'encrypted')`, mkvEnc, mkvPlain); err != nil {
		t.Fatal(err)
	}
	if isISOBaseMediaCatalog(db, 10, mkvEnc) {
		t.Fatal("mkv format should not be ISO")
	}
}

func TestResolveEncryptSourceRequiresFaststartForVideoMP4(t *testing.T) {
	dir := t.TempDir()
	plain := dir + "/clip.mp4"
	if err := os.WriteFile(plain, []byte("not-mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	enc := &AssetEncryptor{DataDir: dir}
	_, _, _, err := enc.resolveEncryptSource(context.Background(), 1, plain, true)
	if err == nil {
		t.Fatal("expected error when faststart required for invalid mp4")
	}
}

func TestResolveEncryptSourceSkipsInvalidMP4WhenPlainKept(t *testing.T) {
	dir := t.TempDir()
	plain := dir + "/bad.mp4"
	if err := os.WriteFile(plain, []byte("not-mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	enc := &AssetEncryptor{DataDir: dir}
	src, cleanup, remuxed, err := enc.resolveEncryptSource(context.Background(), 1, plain, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer cleanup()
	if remuxed {
		t.Fatal("expected no remux for invalid mp4 when plain kept")
	}
	if src != plain {
		t.Fatalf("src=%q want plain", src)
	}
}
