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

func TestDerivedAssetRoundTrip(t *testing.T) {
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
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (10, 1, 'fid-1', 't', 'x.enc', 'video', 'active')`)

	store := &DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(dir, ".derived")}
	encPath, err := store.Write(context.Background(), 10, "preview_vtt", "thumbs.vtt", bytes.NewReader([]byte("WEBVTT\n")))
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.IsEncFile(encPath) {
		t.Fatalf("expected enc path, got %s", encPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "preview", "thumbs.vtt")); !os.IsNotExist(err) {
		t.Fatalf("plaintext should not be written by Write()")
	}
	seeker, err := OpenDerivedSeeker(db, vault, 10, encPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seeker.Close()
	got, err := io.ReadAll(seeker)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "WEBVTT\n" {
		t.Fatalf("got %q", got)
	}
}

func TestDerivedFinalizePathPlainLibrary(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	plain := filepath.Join(dir, "sprite.jpg")
	if err := os.WriteFile(plain, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 0)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_path, file_type, status) VALUES (10, 1, ?, 'video', 'active')`, plain)

	store := &DerivedAssetStore{DB: db, Vault: nil, BaseDir: filepath.Join(dir, ".derived")}
	out, err := store.FinalizePath(context.Background(), 10, "preview_sprite", "sprite.jpg", plain)
	if err != nil {
		t.Fatal(err)
	}
	if out != plain {
		t.Fatalf("expected plain path unchanged, got %s", out)
	}
}

func TestResolveDerivedEncPathFallbackLayout(t *testing.T) {
	dir := t.TempDir()
	derivedBase := filepath.Join(dir, ".derived")
	enc := filepath.Join(derivedBase, "10", "doc_cover", "cover.jpg.enc")
	if err := os.MkdirAll(filepath.Dir(enc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("enc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := ResolveDerivedEncPath(nil, derivedBase, 10, "doc_cover", "cover.jpg")
	if !ok || got != enc {
		t.Fatalf("ResolveDerivedEncPath() = (%q, %v), want (%q, true)", got, ok, enc)
	}
}

func TestLookupDerivedWrappedDEKByKind(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'docs', 'document', '/x')`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, file_type, status) VALUES (10, 1, 'f', 'a.pdf', 'document', 'active')`)
	enc := `F:\data\.derived\10\doc_cover\cover.jpg.enc`
	if _, err := db.Exec(`
		INSERT INTO media_derived_assets (media_id, artifact_kind, logical_name, enc_path, wrapped_dek, iv)
		VALUES (10, 'doc_cover', 'cover.jpg', ?, '6161', '6262')`, enc); err != nil {
		t.Fatal(err)
	}
	wh, err := lookupDerivedWrappedDEK(db, 10, `f:/data/.derived/10/doc_cover/cover.jpg.enc`, "doc_cover", "cover.jpg")
	if err != nil || wh != "6161" {
		t.Fatalf("lookupDerivedWrappedDEK() = (%q, %v)", wh, err)
	}
}

func TestNeedsDerivedEncryption(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', '/x', 1)`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_path, file_type, status) VALUES (10, 1, '/x/a.mp4', 'video', 'active')`)
	if !NeedsDerivedEncryption(db, 10) {
		t.Fatal("expected true")
	}
	if NeedsDerivedEncryption(db, 0) {
		t.Fatal("expected false for invalid id")
	}
}
