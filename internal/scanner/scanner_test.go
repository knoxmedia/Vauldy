package scanner

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func newScannerTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "scanner-test.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(5)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
CREATE TABLE media (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER,
    file_id TEXT UNIQUE,
    title TEXT,
    file_path TEXT,
    file_mtime INTEGER DEFAULT 0,
    file_type TEXT,
    duration INTEGER,
    width INTEGER,
    height INTEGER,
    bitrate INTEGER,
    md5 TEXT,
    format TEXT,
    meta_json TEXT,
    status TEXT DEFAULT 'active'
);
CREATE TABLE library_node (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER NOT NULL,
    parent_path TEXT,
    node_path TEXT NOT NULL,
    node_name TEXT NOT NULL,
    node_type TEXT NOT NULL,
    media_id INTEGER
);
CREATE UNIQUE INDEX idx_library_node_unique ON library_node(library_id, node_path);
`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func TestScanLibraryFoldersWithContextNoValidRoots(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	s := &Scanner{DB: db, SkipHash: true}

	missing := filepath.Join(t.TempDir(), "definitely-missing")
	_, err := s.ScanLibraryFoldersWithContext(context.Background(), 1, []string{missing})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestScanLibraryFoldersAddsMediaAndNodes(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	root := t.TempDir()
	moviePath := filepath.Join(root, "Movie.Name.2025.mp4")
	if err := os.WriteFile(moviePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write movie: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("doc"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	addedCalls := 0
	s := &Scanner{
		DB:       db,
		SkipHash: true,
		OnMediaAdded: func(mediaID int64, title string, fileType string) {
			addedCalls++
		},
	}

	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 9, []string{root})
	if err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if added != 1 {
		t.Fatalf("added=%d want 1", added)
	}
	if addedCalls != 1 {
		t.Fatalf("OnMediaAdded calls=%d want 1", addedCalls)
	}

	var mediaCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = ?`, 9).Scan(&mediaCount); err != nil {
		t.Fatalf("query media count: %v", err)
	}
	if mediaCount != 1 {
		t.Fatalf("media count=%d want 1", mediaCount)
	}

	var fileType string
	if err := db.QueryRow(`SELECT file_type FROM media WHERE library_id = ? LIMIT 1`, 9).Scan(&fileType); err != nil {
		t.Fatalf("query file_type: %v", err)
	}
	if fileType != "video" {
		t.Fatalf("file_type=%q want video", fileType)
	}

	var nodeCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM library_node WHERE library_id = ?`, 9).Scan(&nodeCount); err != nil {
		t.Fatalf("query node count: %v", err)
	}
	if nodeCount < 2 {
		t.Fatalf("node count=%d want >= 2", nodeCount)
	}
}

func TestScanLibraryFoldersDedupOverlappingRoots(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	audioPath := filepath.Join(sub, "song.mp3")
	if err := os.WriteFile(audioPath, []byte("audio"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	s := &Scanner{DB: db, SkipHash: true}

	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 7, []string{root, sub})
	if err != nil {
		t.Fatalf("first scan error: %v", err)
	}
	if added != 1 {
		t.Fatalf("first added=%d want 1", added)
	}

	var mediaCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = ?`, 7).Scan(&mediaCount); err != nil {
		t.Fatalf("query media count after first scan: %v", err)
	}
	if mediaCount != 1 {
		t.Fatalf("media count after first scan=%d want 1", mediaCount)
	}
}

func TestScanLibraryFoldersWithContextCanceled(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "clip.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Scanner{DB: db, SkipHash: true}
	_, err := s.ScanLibraryFoldersWithContext(ctx, 11, []string{root})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
