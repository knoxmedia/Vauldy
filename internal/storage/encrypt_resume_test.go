package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func openEncryptResumeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestEncryptResume_UpsertAndLoad(t *testing.T) {
	db := openEncryptResumeTestDB(t)
	if err := EnsureEncryptResumeSchema(db); err != nil {
		t.Fatal(err)
	}
	row := EncryptResumeRow{
		MediaID:         1,
		Generation:      0,
		StageID:         "s1",
		EncPath:         "/tmp/a.enc",
		SourcePath:      "/tmp/a.mp4",
		SourceIdentity:  "id",
		WrappedDEK:      "aa",
		IV:              "bb",
		PlainOffset:     1 << 20,
		EncBytesWritten: 1 << 20,
		State:           "encrypting",
	}
	if err := UpsertEncryptResume(context.Background(), db, row); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEncryptResume(context.Background(), db, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlainOffset != 1<<20 {
		t.Fatalf("PlainOffset=%d want %d", got.PlainOffset, 1<<20)
	}
	if got.EncBytesWritten != 1<<20 {
		t.Fatalf("EncBytesWritten=%d want %d", got.EncBytesWritten, 1<<20)
	}
	if got.State != "encrypting" {
		t.Fatalf("State=%q want encrypting", got.State)
	}
}

func TestEncryptResume_LoadMissing(t *testing.T) {
	db := openEncryptResumeTestDB(t)
	if err := EnsureEncryptResumeSchema(db); err != nil {
		t.Fatal(err)
	}
	_, err := LoadEncryptResume(context.Background(), db, 99, 0)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err=%v want sql.ErrNoRows", err)
	}
}

func TestEncryptResume_Abandon(t *testing.T) {
	db := openEncryptResumeTestDB(t)
	if err := EnsureEncryptResumeSchema(db); err != nil {
		t.Fatal(err)
	}
	row := EncryptResumeRow{
		MediaID: 1, Generation: 0, StageID: "s1",
		EncPath: "/tmp/a.enc", SourcePath: "/tmp/a.mp4",
		SourceIdentity: "id", WrappedDEK: "aa", IV: "bb",
		State: "encrypting",
	}
	if err := UpsertEncryptResume(context.Background(), db, row); err != nil {
		t.Fatal(err)
	}
	if err := AbandonEncryptResume(context.Background(), db, 1, 0); err != nil {
		t.Fatal(err)
	}
	got, err := LoadEncryptResume(context.Background(), db, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "abandoned" {
		t.Fatalf("State=%q want abandoned", got.State)
	}
}

func TestQuickSourceIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.mp4")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	id1, err := QuickSourceIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := QuickSourceIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("identity not stable: %q vs %q", id1, id2)
	}
	abs, _ := filepath.Abs(path)
	wantPrefix := filepath.Clean(abs) + "|"
	if len(id1) <= len(wantPrefix) || id1[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("identity %q does not start with %q", id1, wantPrefix)
	}
}

func TestEncryptResumeCheckpointBytes(t *testing.T) {
	if EncryptResumeCheckpointBytes != 64<<20 {
		t.Fatalf("EncryptResumeCheckpointBytes=%d want %d", EncryptResumeCheckpointBytes, 64<<20)
	}
}
