package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestNewMediaFinalizerRunsAfterCommitBeforeSuccessCallbacks(t *testing.T) {
	db := newScannerTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	var order []string
	added, err := (&Scanner{DB: db, SkipHash: true}).ScanLibraryFoldersWithContextAndCallbacks(context.Background(), 1, []string{root}, ScanCallbacks{
		OnMediaDiscovered: func(_ context.Context, discovery ScanDiscovery) error {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE id=?`, discovery.MediaID).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Fatalf("media is not committed during finalizer")
			}
			order = append(order, "discovered")
			return nil
		},
		OnMediaAdded: func(context.Context, int64, string, string) error {
			order = append(order, "added")
			return nil
		},
		OnFile: func(_ string, err error) {
			if err == nil {
				order = append(order, "file")
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added=%d want 1", added)
	}
	if got := strings.Join(order, ","); got != "discovered,added,file" {
		t.Fatalf("callback order=%q", got)
	}
}

func TestNewMediaFinalizerFailureSkipsSuccessAndContinues(t *testing.T) {
	db := newScannerTestDB(t)
	root := t.TempDir()
	for _, name := range []string{"bad.mp4", "good.mp4"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	finalizerErr := errors.New("poster finalizer failed")
	var finalizers, mediaAdded int
	var fileErrors []error
	added, err := (&Scanner{DB: db, SkipHash: true}).ScanLibraryFoldersWithContextAndCallbacks(context.Background(), 1, []string{root}, ScanCallbacks{
		OnMediaDiscovered: func(_ context.Context, discovery ScanDiscovery) error {
			finalizers++
			if discovery.Title == "bad" {
				return finalizerErr
			}
			return nil
		},
		OnMediaAdded: func(context.Context, int64, string, string) error {
			mediaAdded++
			return nil
		},
		OnFile: func(_ string, err error) { fileErrors = append(fileErrors, err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || finalizers != 2 || mediaAdded != 1 {
		t.Fatalf("added=%d finalizers=%d OnMediaAdded=%d", added, finalizers, mediaAdded)
	}
	if len(fileErrors) != 2 || !errors.Is(fileErrors[0], finalizerErr) || fileErrors[1] != nil {
		t.Fatalf("OnFile errors=%v", fileErrors)
	}
	var mediaNodes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_node WHERE library_id=1 AND node_type='file'`).Scan(&mediaNodes); err != nil {
		t.Fatal(err)
	}
	if mediaNodes != 1 {
		t.Fatalf("file nodes=%d want 1", mediaNodes)
	}
}

func TestExistingMediaDoesNotRunFinalizer(t *testing.T) {
	db := newScannerTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Scanner{DB: db, SkipHash: true}
	if added, err := s.ScanLibraryFoldersWithContext(context.Background(), 1, []string{root}); err != nil || added != 1 {
		t.Fatalf("initial scan added=%d err=%v", added, err)
	}
	calls := 0
	added, err := s.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), 1, []string{root}, ScanCallbacks{
		OnMediaDiscovered: func(context.Context, ScanDiscovery) error { calls++; return nil },
	})
	if err != nil || added != 0 || calls != 0 {
		t.Fatalf("rescan added=%d calls=%d err=%v", added, calls, err)
	}
}
