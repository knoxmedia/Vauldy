package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
)

func TestStageMediaEncryption_ResumesAfterCancel(t *testing.T) {
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
	plain := bytes.Repeat([]byte{0xAB}, 2<<20) // 2MiB
	if err := os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}

	const mediaID int64 = 42
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'lib','video',?,1)`, dir)
	_, _ = db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(?,1,'fid-resume','t',?,'video','active')`, mediaID, plainPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	enc := &AssetEncryptor{
		DB:                    db,
		Vault:                 vault,
		BasePath:              filepath.Join(dir, "encrypted"),
		ResumeCheckpointBytes: 1 << 20, // 1MiB checkpoints for the test
		onEncryptCheckpoint: func(c context.Context, offset int64) {
			if offset >= 1<<20 {
				cancel()
				<-c.Done() // hold the stager until cancel is observed
			}
		},
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := enc.StageMediaEncryption(ctx, mediaID)
		errCh <- err
	}()

	waitUntilResumeOffset(t, db, mediaID, 1<<20)
	firstErr := <-errCh
	if firstErr == nil {
		t.Fatal("expected cancel error from first StageMediaEncryption")
	}

	// Partial stage must remain after cancel past a checkpoint.
	row, err := LoadEncryptResume(context.Background(), db, mediaID, 0)
	if err != nil {
		t.Fatalf("load resume: %v", err)
	}
	if _, err := os.Stat(row.EncPath); err != nil {
		t.Fatalf("partial enc missing after cancel: %v", err)
	}
	if row.PlainOffset < 1<<20 {
		t.Fatalf("PlainOffset=%d want >= 1MiB", row.PlainOffset)
	}

	enc.onEncryptCheckpoint = nil // do not pause on the resume pass
	stage, err := enc.StageMediaEncryption(context.Background(), mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if stage.EncPath == "" || stage.WrappedDEK == "" || stage.IV == "" {
		t.Fatalf("incomplete stage: %+v", stage)
	}
	if stage.EncPath != row.EncPath {
		t.Fatalf("enc path changed across resume: %q vs %q", stage.EncPath, row.EncPath)
	}

	wrapped, err := hex.DecodeString(stage.WrappedDEK)
	if err != nil {
		t.Fatal(err)
	}
	got, err := crypto.DecryptFile(stage.EncPath, wrapped, kek)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypt mismatch: got %d bytes want %d", len(got), len(plain))
	}

	final, err := LoadEncryptResume(context.Background(), db, mediaID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "staged" {
		t.Fatalf("State=%q want staged", final.State)
	}
	if final.EncPath != stage.EncPath {
		t.Fatalf("enc path changed across resume: %q vs %q", final.EncPath, stage.EncPath)
	}
}

func waitUntilResumeOffset(t *testing.T, db *sql.DB, mediaID, minOffset int64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		row, err := LoadEncryptResume(context.Background(), db, mediaID, 0)
		if err == nil && row.PlainOffset >= minOffset {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for resume plain_offset >= %d", minOffset)
}

func TestStageMediaEncryption_ResumeDecryptRoundTripSmall(t *testing.T) {
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
	plainPath := filepath.Join(dir, "small.bin")
	plain := []byte("hello-resume-stage")
	if err := os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'lib','photo',?,1)`, dir)
	_, _ = db.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(11,1,'s1',?,'image','active')`, plainPath)
	enc := &AssetEncryptor{DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted"), ResumeCheckpointBytes: 64}
	stage, err := enc.StageMediaEncryption(context.Background(), 11)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := hex.DecodeString(stage.WrappedDEK)
	if err != nil {
		t.Fatal(err)
	}
	got, err := crypto.DecryptFile(stage.EncPath, wrapped, kek)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q want %q", got, plain)
	}
}
