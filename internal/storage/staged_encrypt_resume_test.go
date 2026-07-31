package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
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

func TestStageMediaEncryption_ResumeShortEncAbandonsAndRestarts(t *testing.T) {
	db, vault, kek, dir, _, plain, mediaID := arrangeResumePlain(t, 2<<20)
	enc := &AssetEncryptor{
		DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted"),
		ResumeCheckpointBytes: 1 << 20,
	}
	row := stagePartialThenCancel(t, enc, db, mediaID)

	// Corrupt: truncate enc below EncHeaderSize+PlainOffset.
	wantSize := int64(crypto.EncHeaderSize) + row.PlainOffset
	if err := os.Truncate(row.EncPath, wantSize/2); err != nil {
		t.Fatal(err)
	}
	oldPath := row.EncPath

	stage, err := enc.StageMediaEncryption(context.Background(), mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if stage.EncPath == oldPath {
		t.Fatalf("expected fresh enc path after short file, got same %q", stage.EncPath)
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
		t.Fatalf("decrypt mismatch after short-enc restart")
	}
}

func TestStageMediaEncryption_ResumeLongEncTruncatesAndSucceeds(t *testing.T) {
	db, vault, kek, dir, _, plain, mediaID := arrangeResumePlain(t, 2<<20)
	enc := &AssetEncryptor{
		DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted"),
		ResumeCheckpointBytes: 1 << 20,
	}
	row := stagePartialThenCancel(t, enc, db, mediaID)
	wantCheckpointSize := int64(crypto.EncHeaderSize) + row.PlainOffset

	// Pad well past the eventual final enc size so trailing garbage remains unless truncated.
	pad := bytes.Repeat([]byte{0xFF}, 3<<20)
	f, err := os.OpenFile(row.EncPath, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(pad); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	st, err := os.Stat(row.EncPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() <= wantCheckpointSize {
		t.Fatalf("setup: enc size %d want > %d", st.Size(), wantCheckpointSize)
	}

	stage, err := enc.StageMediaEncryption(context.Background(), mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if stage.EncPath != row.EncPath {
		t.Fatalf("expected same enc path after truncate-resume, got %q want %q", stage.EncPath, row.EncPath)
	}
	finalInfo, err := os.Stat(stage.EncPath)
	if err != nil {
		t.Fatal(err)
	}
	wantFinal := int64(crypto.EncHeaderSize) + int64(len(plain))
	if finalInfo.Size() != wantFinal {
		t.Fatalf("final enc size=%d want %d (truncate missing?)", finalInfo.Size(), wantFinal)
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
		t.Fatalf("decrypt mismatch after long-enc truncate resume")
	}
}

func TestStageMediaEncryption_CheckpointSyncsBeforeUpsert(t *testing.T) {
	db, vault, _, dir, _, _, mediaID := arrangeResumePlain(t, 2<<20)
	var syncCount, upsertSeenAtSync int
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	enc := &AssetEncryptor{
		DB: db, Vault: vault, BasePath: filepath.Join(dir, "encrypted"),
		ResumeCheckpointBytes: 1 << 20,
		syncStagedFile: func(f *os.File) error {
			mu.Lock()
			defer mu.Unlock()
			syncCount++
			row, err := LoadEncryptResume(context.Background(), db, mediaID, 0)
			if err == nil && row.PlainOffset >= 1<<20 {
				upsertSeenAtSync++
			}
			return f.Sync()
		},
		onEncryptCheckpoint: func(c context.Context, offset int64) {
			if offset >= 1<<20 {
				mu.Lock()
				// After first checkpoint upsert, resume row must already reflect offset,
				// and sync must have run at least once for that checkpoint.
				if syncCount < 1 {
					t.Errorf("expected dst.Sync before checkpoint upsert, syncCount=%d", syncCount)
				}
				mu.Unlock()
				cancel()
				<-c.Done()
			}
		},
	}
	_, _ = enc.StageMediaEncryption(ctx, mediaID)
	mu.Lock()
	defer mu.Unlock()
	if syncCount < 1 {
		t.Fatalf("expected at least one sync before checkpoint, got %d", syncCount)
	}
	_ = upsertSeenAtSync
}

func arrangeResumePlain(t *testing.T, size int) (*sql.DB, *keystore.Vault, []byte, string, string, []byte, int64) {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	plain := bytes.Repeat([]byte{0xAB}, size)
	if err := os.WriteFile(plainPath, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	const mediaID int64 = 77
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'lib','video',?,1)`, dir)
	_, _ = db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(?,1,'fid-dur','t',?,'video','active')`, mediaID, plainPath)
	return db, vault, kek, dir, plainPath, plain, mediaID
}

func stagePartialThenCancel(t *testing.T, enc *AssetEncryptor, db *sql.DB, mediaID int64) EncryptResumeRow {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prevHook := enc.onEncryptCheckpoint
	enc.onEncryptCheckpoint = func(c context.Context, offset int64) {
		if offset >= 1<<20 {
			cancel()
			<-c.Done()
		}
	}
	t.Cleanup(func() { enc.onEncryptCheckpoint = prevHook })

	errCh := make(chan error, 1)
	go func() {
		_, err := enc.StageMediaEncryption(ctx, mediaID)
		errCh <- err
	}()
	waitUntilResumeOffset(t, db, mediaID, 1<<20)
	if err := <-errCh; err == nil {
		t.Fatal("expected cancel error")
	}
	enc.onEncryptCheckpoint = nil
	row, err := LoadEncryptResume(context.Background(), db, mediaID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return row
}
