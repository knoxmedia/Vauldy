package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestEncryptMediaManual_ResumesAfterCancel(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	vault, err := keystore.NewVault(string(bytes.Repeat([]byte{0x42}, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	plainPath := filepath.Join(dir, "large.bin")
	plain := bytes.Repeat([]byte{0xAB}, 2<<20)
	if err := os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}

	const (
		mediaID    int64 = 91
		generation int64 = 7
	)
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'lib','document',?,0)`, dir)
	_, _ = db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,ingest_generation) VALUES(?,1,'fid-manual','t',?,'document','active',?)`, mediaID, plainPath, generation)

	ctx, cancel := context.WithCancel(context.Background())
	enc := &AssetEncryptor{
		DB:                    db,
		Vault:                 vault,
		ResumeCheckpointBytes: 1 << 20,
		onEncryptCheckpoint: func(c context.Context, offset int64) {
			if offset >= 1<<20 {
				cancel()
				<-c.Done()
			}
		},
	}

	firstErr := enc.EncryptMediaManual(ctx, mediaID)
	if !errors.Is(firstErr, context.Canceled) {
		t.Fatalf("first EncryptMediaManual error=%v want context.Canceled", firstErr)
	}

	row, err := LoadEncryptResume(context.Background(), db, mediaID, generation)
	if err != nil {
		t.Fatalf("load manual resume: %v", err)
	}
	if row.PlainOffset < 1<<20 {
		t.Fatalf("PlainOffset=%d want >= 1MiB", row.PlainOffset)
	}
	if _, err := os.Stat(row.EncPath); err != nil {
		t.Fatalf("partial encrypted output missing: %v", err)
	}

	enc.onEncryptCheckpoint = nil
	if err := enc.EncryptMediaManual(context.Background(), mediaID); err != nil {
		t.Fatal(err)
	}

	var encPath, wrappedHex string
	if err := db.QueryRow(`SELECT m.file_path,a.wrapped_dek FROM media m JOIN media_encrypted_assets a ON a.media_id=m.id WHERE m.id=? AND a.status='encrypted'`, mediaID).Scan(&encPath, &wrappedHex); err != nil {
		t.Fatal(err)
	}
	if encPath != row.EncPath {
		t.Fatalf("enc path changed across resume: got %q want %q", encPath, row.EncPath)
	}
	wrapped, err := hex.DecodeString(wrappedHex)
	if err != nil {
		t.Fatal(err)
	}
	got, err := crypto.DecryptFile(encPath, wrapped, kek)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypt mismatch: got %d bytes want %d", len(got), len(plain))
	}
}
