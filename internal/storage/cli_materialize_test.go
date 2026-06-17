package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestMaterializeCLIFilePlaintextPassthrough(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "thumb.jpg")
	if err := os.WriteFile(plain, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := MaterializeCLIFile(nil, nil, 0, plain)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got != plain {
		t.Fatalf("got %q want %q", got, plain)
	}
}

func TestMaterializeCLIFileDecryptsDerivedEnc(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	kek := bytes.Repeat([]byte{0x11}, 32)
	vault, err := keystore.NewVault(string(kek), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	derived := &DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(dir, ".derived")}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'photo', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (5, 1, 'f', 't', 'a.jpg', 'image', 'active')`)

	plain := filepath.Join(dir, "thumb.jpg")
	payload := []byte("plain-jpeg-bytes")
	if err := os.WriteFile(plain, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	encPath, err := derived.FinalizePath(context.Background(), 5, "photo_thumb", "thumb.jpg", plain)
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncFile(encPath) {
		t.Fatalf("expected enc file at %s", encPath)
	}

	got, cleanup, err := MaterializeCLIFile(db, vault, 5, encPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got == encPath {
		t.Fatal("expected temp plaintext path")
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("payload mismatch: %q", data)
	}
}
