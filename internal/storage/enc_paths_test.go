package storage

import (
	"context"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestResolveEncBaseModes(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	dir := t.TempDir()
	libRoot := filepath.Join(dir, "movies")
	dataDir := filepath.Join(dir, "data")
	customDir := filepath.Join(dir, "vault")
	plain := filepath.Join(libRoot, "a.mp4")
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_dir_mode, encrypted_assets_custom_dir) VALUES (1, 'lib', 'movie', ?, 'library', '')`, libRoot)
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_dir_mode, encrypted_assets_custom_dir) VALUES (2, 'lib2', 'movie', ?, 'data', '')`, libRoot)
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_dir_mode, encrypted_assets_custom_dir) VALUES (3, 'lib3', 'movie', ?, 'custom', ?)`, libRoot, customDir)

	enc := &AssetEncryptor{DB: db, DataDir: dataDir}

	base, err := enc.ResolveEncBase(context.Background(), 1, plain)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(libRoot, ".encrypted"); base != want {
		t.Fatalf("library base=%q want %q", base, want)
	}

	base, err = enc.ResolveEncBase(context.Background(), 2, plain)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dataDir, ".encrypted"); base != want {
		t.Fatalf("data base=%q want %q", base, want)
	}

	base, err = enc.ResolveEncBase(context.Background(), 3, plain)
	if err != nil {
		t.Fatal(err)
	}
	if base != customDir {
		t.Fatalf("custom base=%q want %q", base, customDir)
	}
}
