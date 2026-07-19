package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"knox-media/internal/store"
)

func TestScanLibraryFoldersWithCallbacksUsesPerCallOnFile(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "callbacks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	result, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('callbacks','video',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := result.LastInsertId()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	var shared, perCall atomic.Int64
	s := &Scanner{DB: db, SkipHash: true, OnFile: func(string, error) { shared.Add(1) }}
	_, err = s.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), libraryID, []string{root}, ScanCallbacks{OnFile: func(string, error) { perCall.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	if perCall.Load() != 1 {
		t.Fatalf("per-call OnFile=%d want 1", perCall.Load())
	}
	if shared.Load() != 0 {
		t.Fatalf("shared OnFile=%d want 0", shared.Load())
	}
}
