package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestEncryptMediaRoundTrip(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	kek := bytes.Repeat([]byte{0x42}, 32)
	vault, err := keystore.NewVault(string(kek), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(plain, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (10, 1, 'fid-1', 't', ?, 'video', 'active')`, plain)

	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	if err := enc.EncryptMedia(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var encPath string
	if err := db.QueryRow(`SELECT file_path FROM media WHERE id = 10`).Scan(&encPath); err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncFile(encPath) {
		t.Fatalf("expected enc path, got %s", encPath)
	}
	rc, err := OpenPlaintext(db, vault, 10, encPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fake-video-bytes" {
		t.Fatalf("got %q", got)
	}
}
