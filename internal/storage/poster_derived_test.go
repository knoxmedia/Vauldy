package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestFinalizeLocalPosterEncrypts(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	kek := []byte("01234567890123456789012345678901")
	vault, err := keystore.NewVault(string(kek), "")
	if err != nil {
		t.Fatal(err)
	}
	derived := &DerivedAssetStore{
		DB:      db,
		Vault:   vault,
		BaseDir: filepath.Join(dir, ".derived"),
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (9, 1, 'f', 't', 'v.mp4', 'video', 'active')`)

	plain := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(plain, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	url, err := FinalizeLocalPoster(context.Background(), derived, db, 9, plain)
	if err != nil {
		t.Fatal(err)
	}
	if url != DerivedPosterAPIPath(9) {
		t.Fatalf("url=%q", url)
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Fatal("plaintext poster should be removed")
	}
	enc, ok := LookupEncPath(db, 9, derivedPosterKind, derivedPosterLogicalName)
	if !ok {
		t.Fatal("missing derived poster row")
	}
	if _, err := os.Stat(enc); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeLocalPosterPlainLibrary(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 0)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (9, 1, 'f', 't', 'v.mp4', 'video', 'active')`)
	plain := filepath.Join(dir, "poster.jpg")
	_ = os.WriteFile(plain, []byte("jpeg"), 0o644)
	url, err := FinalizeLocalPoster(context.Background(), nil, db, 9, plain)
	if err != nil {
		t.Fatal(err)
	}
	if url != PlainPosterURL(9) {
		t.Fatalf("url=%q", url)
	}
}
