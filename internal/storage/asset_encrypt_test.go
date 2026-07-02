package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestEncryptMediaConcurrentSingleOutput(t *testing.T) {
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
	plain := filepath.Join(dir, "clip.mkv")
	payload := bytes.Repeat([]byte("video-bytes"), 4096)
	if err := os.WriteFile(plain, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (10, 1, 'fid-1', 't', ?, 'video', 'active')`, plain)

	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = enc.EncryptMedia(context.Background(), 10)
		}()
	}
	wg.Wait()

	encDir := filepath.Join(dir, ".encrypted", "video")
	entries, err := os.ReadDir(encDir)
	if err != nil {
		t.Fatal(err)
	}
	var encFiles int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".enc") {
			encFiles++
		}
	}
	if encFiles != 1 {
		t.Fatalf("expected 1 .enc file, got %d (%v)", encFiles, entries)
	}
}

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
	plain := filepath.Join(dir, "clip.mkv")
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

func TestEncryptPlainMissingMarksRow(t *testing.T) {
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
	missing := filepath.Join(dir, "gone.mp4")
	_, _ = db.Exec(`INSERT INTO library (id, name, type, path, encrypted_assets_enabled) VALUES (1, 'lib', 'video', ?, 1)`, dir)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (400, 1, 'fid-1', 't', ?, 'video', 'active')`, missing)

	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted")}
	if err := enc.EncryptMedia(context.Background(), 400); err == nil {
		t.Fatal("expected error for missing plain file")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM media_encrypted_assets WHERE media_id = 400`).Scan(&status); err != nil {
		t.Fatalf("missing row: %v", err)
	}
	if status != "plain_missing" {
		t.Fatalf("status=%q want plain_missing", status)
	}
}
