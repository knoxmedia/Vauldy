package scanner

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"

	"knox-media/internal/publication"
	"knox-media/internal/store"
	"knox-media/pkg/ffprobe"
	"knox-media/pkg/hashutil"
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
    status TEXT DEFAULT 'active',
    created_at_sort TEXT,
    photo_taken_at TEXT,
    photo_place_id TEXT
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
` + deleteCatalogTestDDL())
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

func TestScanNewVideoRollsBackMediaWhenPlanFails(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan-plan-rollback.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('videos','video',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	planRejected := errors.New("plan rejected")
	s := &Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		return &ffprobe.Summary{}, nil
	}}
	_, err = s.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), libraryID, []string{root}, ScanCallbacks{
		OnMediaDiscoveredTx: func(ctx context.Context, tx *sql.Tx, discovery ScanDiscovery) error {
			_, planErr := publication.NewPlanner(publication.PlanOptions{}).PlanNewMediaTx(ctx, tx, publication.NewMedia{MediaID: discovery.MediaID, ScanTaskID: taskID, FileType: discovery.FileType})
			if planErr != nil {
				return planErr
			}
			return planRejected
		},
	})
	if !errors.Is(err, planRejected) {
		t.Fatalf("scan error=%v want %v", err, planRejected)
	}
	for _, table := range []string{"media", "media_ingest_run", "media_ingest_step", "post_ingest_task"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows=%d want 0", table, count)
		}
	}
}

func TestScanNewVideoCommitsMediaAndPlanTogether(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan-plan-commit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path,preview_extract) VALUES('videos','video',?,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	planner := publication.NewPlanner(publication.PlanOptions{})
	s := &Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		return &ffprobe.Summary{}, nil
	}}
	added, err := s.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), libraryID, []string{root}, ScanCallbacks{
		OnMediaDiscoveredTx: func(ctx context.Context, tx *sql.Tx, discovery ScanDiscovery) error {
			_, err := planner.PlanNewMediaTx(ctx, tx, publication.NewMedia{MediaID: discovery.MediaID, ScanTaskID: taskID, FileType: discovery.FileType})
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added=%d want 1", added)
	}
	for table, want := range map[string]int{"media": 1, "media_ingest_run": 1, "media_ingest_step": 4, "post_ingest_task": 2} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s rows=%d want %d", table, count, want)
		}
	}
}

func TestScannerDiscoveryCarriesExistingProbeDiagnostics(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan-metadata-diagnostics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('videos','video',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()

	probeCalls := 0
	s := &Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		probeCalls++
		return &ffprobe.Summary{DurationSec: 95, Width: 1920, Height: 1080, Format: "matroska", RawJSON: `{"format":{"duration":"95"}}`}, errors.New(strings.Repeat("probe partial ", 100))
	}}
	planner := publication.NewPlanner(publication.PlanOptions{})
	var got ScanDiscovery
	added, err := s.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), libraryID, []string{root}, ScanCallbacks{
		OnMediaDiscoveredTx: func(ctx context.Context, tx *sql.Tx, discovery ScanDiscovery) error {
			got = discovery
			_, err := planner.PlanNewMediaTx(ctx, tx, publication.NewMedia{
				MediaID: discovery.MediaID, ScanTaskID: taskID, FileType: discovery.FileType,
				MetadataAttempt: publication.MetadataAttempt{
					Attempted: discovery.MetadataAttempt.Attempted,
					Fields:    append([]string(nil), discovery.MetadataAttempt.Fields...),
					Errors:    []publication.MetadataDiagnostic{{Source: discovery.MetadataAttempt.Errors[0].Source, Message: discovery.MetadataAttempt.Errors[0].Message}},
				},
			})
			return err
		},
	})
	if err != nil || added != 1 {
		t.Fatalf("scan added=%d err=%v", added, err)
	}
	if probeCalls != 1 {
		t.Fatalf("probe calls=%d want 1", probeCalls)
	}
	if !got.MetadataAttempt.Attempted {
		t.Fatal("metadata attempt not recorded")
	}
	for _, field := range []string{"duration", "width", "height", "format", "meta_json"} {
		if !containsString(got.MetadataAttempt.Fields, field) {
			t.Fatalf("fields=%v missing %q", got.MetadataAttempt.Fields, field)
		}
	}
	if len(got.MetadataAttempt.Errors) != 1 || got.MetadataAttempt.Errors[0].Source != "probe" || len(got.MetadataAttempt.Errors[0].Message) > 512 {
		t.Fatalf("bounded probe diagnostics=%+v", got.MetadataAttempt.Errors)
	}
	var duration, width, height int
	var format, raw, snapshotJSON string
	if err := db.QueryRow(`SELECT duration,width,height,format,meta_json FROM media WHERE id=?`, got.MediaID).Scan(&duration, &width, &height, &format, &raw); err != nil {
		t.Fatal(err)
	}
	if duration != 95 || width != 1920 || height != 1080 || format != "matroska" || raw == "" {
		t.Fatalf("stored metadata duration=%d size=%dx%d format=%q raw=%q", duration, width, height, format, raw)
	}
	if err := db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE media_id=?`, got.MediaID).Scan(&snapshotJSON); err != nil {
		t.Fatal(err)
	}
	var snapshot publication.ConfigSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Metadata.Attempted || len(snapshot.Metadata.Errors) != 1 || snapshot.Metadata.Errors[0].Source != "probe" {
		t.Fatalf("snapshot metadata=%+v", snapshot.Metadata)
	}
}

func TestScannerDiscoveryCarriesFailedProbeDiagnostics(t *testing.T) {
	db := newScannerTestDB(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := &Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		calls++
		return nil, errors.New("invalid media")
	}}
	var got ScanDiscovery
	added, err := s.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), 1, []string{root}, ScanCallbacks{
		OnMediaDiscoveredTx: func(_ context.Context, _ *sql.Tx, discovery ScanDiscovery) error { got = discovery; return nil },
	})
	if err != nil || added != 1 {
		t.Fatalf("scan added=%d err=%v", added, err)
	}
	if calls != 1 || !got.MetadataAttempt.Attempted || len(got.MetadataAttempt.Fields) != 0 || len(got.MetadataAttempt.Errors) != 1 {
		t.Fatalf("calls=%d diagnostics=%+v", calls, got.MetadataAttempt)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestScannerCallbackRegressionUsesCallbacksField(t *testing.T) {
	db := newScannerTestDB(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := &Scanner{DB: db, SkipHash: true, OnMediaAdded: func(int64, string, string) {
		panic("scanner field callback must not run")
	}}
	_, err := s.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), 1, []string{root}, ScanCallbacks{
		OnMediaAdded: func(context.Context, int64, string, string) error {
			calls++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("argument callback calls=%d want 1", calls)
	}
}

func TestScanLibraryFoldersSkipsPretranscodeOutput(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	root := t.TempDir()
	sourcePath := filepath.Join(root, "Movie.2025.mp4")
	if err := os.WriteFile(sourcePath, []byte("source-video"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	pretranscodeDir := filepath.Join(root, "Movie.2025.pretranscode", "preset1", "720p")
	if err := os.MkdirAll(pretranscodeDir, 0o755); err != nil {
		t.Fatalf("mkdir pretranscode output: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pretranscodeDir, "720p.m3u8"), []byte("#EXTM3U"), 0o644); err != nil {
		t.Fatalf("write playlist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pretranscodeDir, "seg000.ts"), []byte("segment"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}

	addedCalls := 0
	s := &Scanner{
		DB:       db,
		SkipHash: true,
		OnMediaAdded: func(int64, string, string) {
			addedCalls++
		},
	}

	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 12, []string{root})
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
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = ?`, 12).Scan(&mediaCount); err != nil {
		t.Fatalf("query media count: %v", err)
	}
	if mediaCount != 1 {
		t.Fatalf("media count=%d want 1", mediaCount)
	}

	var storedPath string
	if err := db.QueryRow(`SELECT file_path FROM media WHERE library_id = ? LIMIT 1`, 12).Scan(&storedPath); err != nil {
		t.Fatalf("query file_path: %v", err)
	}
	if normalizeMediaPath(storedPath) != normalizeMediaPath(sourcePath) {
		t.Fatalf("stored path=%q want %q", storedPath, sourcePath)
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

func TestScanLibraryFoldersUpdatesPathWhenMD5Matches(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	root := t.TempDir()
	oldRel := "movies/old-name.mp4"
	newRel := "movies/renamed.mp4"
	content := []byte("same video content for md5 match")
	oldPath := filepath.Join(root, filepath.FromSlash(oldRel))
	newPath := filepath.Join(root, filepath.FromSlash(newRel))
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatalf("mkdir movies: %v", err)
	}
	if err := os.WriteFile(newPath, content, 0o644); err != nil {
		t.Fatalf("write renamed file: %v", err)
	}
	md5, err := hashutil.MD5File(newPath)
	if err != nil {
		t.Fatalf("md5 file: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO media (library_id, file_id, title, file_path, file_type, md5, status, file_mtime)
		VALUES (?, ?, ?, ?, 'video', ?, 'active', 0)
	`, 3, uuid.NewString(), "Old Name", oldPath, md5)
	if err != nil {
		t.Fatalf("insert existing media: %v", err)
	}

	addedCalls := 0
	s := &Scanner{
		DB:       db,
		SkipHash: false,
		OnMediaAdded: func(int64, string, string) {
			addedCalls++
		},
	}

	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 3, []string{root})
	if err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if added != 0 {
		t.Fatalf("added=%d want 0", added)
	}
	if addedCalls != 0 {
		t.Fatalf("OnMediaAdded calls=%d want 0", addedCalls)
	}

	var mediaCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = ?`, 3).Scan(&mediaCount); err != nil {
		t.Fatalf("query media count: %v", err)
	}
	if mediaCount != 1 {
		t.Fatalf("media count=%d want 1", mediaCount)
	}

	var storedPath string
	if err := db.QueryRow(`SELECT file_path FROM media WHERE library_id = ? LIMIT 1`, 3).Scan(&storedPath); err != nil {
		t.Fatalf("query file_path: %v", err)
	}
	if normalizeMediaPath(storedPath) != normalizeMediaPath(newPath) {
		t.Fatalf("file_path=%q want %q", storedPath, newPath)
	}

	var linkedMediaID sql.NullInt64
	if err := db.QueryRow(`
		SELECT media_id FROM library_node
		WHERE library_id = ? AND node_type = 'file' AND node_name = 'renamed.mp4'
		LIMIT 1
	`, 3).Scan(&linkedMediaID); err != nil {
		t.Fatalf("query library_node media_id: %v", err)
	}
	if !linkedMediaID.Valid || linkedMediaID.Int64 <= 0 {
		t.Fatalf("library_node media_id not linked")
	}
}

func TestScanLibraryFoldersInsertsDuplicateWhenMD5MatchesAndOldPathExists(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	root := t.TempDir()
	content := []byte("duplicate video content")
	pathA := filepath.Join(root, "copy-a.mp4")
	pathB := filepath.Join(root, "subdir", "copy-b.mp4")
	if err := os.MkdirAll(filepath.Dir(pathB), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.WriteFile(pathA, content, 0o644); err != nil {
		t.Fatalf("write copy-a: %v", err)
	}
	if err := os.WriteFile(pathB, content, 0o644); err != nil {
		t.Fatalf("write copy-b: %v", err)
	}
	md5, err := hashutil.MD5File(pathA)
	if err != nil {
		t.Fatalf("md5 file: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO media (library_id, file_id, title, file_path, file_type, md5, status, file_mtime)
		VALUES (?, ?, ?, ?, 'video', ?, 'active', 0)
	`, 4, uuid.NewString(), "Copy A", pathA, md5)
	if err != nil {
		t.Fatalf("insert existing media: %v", err)
	}

	addedCalls := 0
	s := &Scanner{
		DB:       db,
		SkipHash: false,
		OnMediaAdded: func(int64, string, string) {
			addedCalls++
		},
	}

	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 4, []string{root})
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
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = ?`, 4).Scan(&mediaCount); err != nil {
		t.Fatalf("query media count: %v", err)
	}
	if mediaCount != 2 {
		t.Fatalf("media count=%d want 2", mediaCount)
	}

	var pathCount int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT lower(file_path)) FROM media WHERE library_id = ?`, 4).Scan(&pathCount); err != nil {
		t.Fatalf("query distinct paths: %v", err)
	}
	if pathCount != 2 {
		t.Fatalf("distinct paths=%d want 2", pathCount)
	}
}

func TestScanLibraryFoldersSamePathDifferentLibraries(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	root := t.TempDir()
	moviePath := filepath.Join(root, "shared.mp4")
	if err := os.WriteFile(moviePath, []byte("shared video"), 0o644); err != nil {
		t.Fatalf("write movie: %v", err)
	}

	s := &Scanner{DB: db, SkipHash: true}

	addedA, err := s.ScanLibraryFoldersWithContext(context.Background(), 1, []string{root})
	if err != nil {
		t.Fatalf("scan library A: %v", err)
	}
	if addedA != 1 {
		t.Fatalf("library A added=%d want 1", addedA)
	}

	addedB, err := s.ScanLibraryFoldersWithContext(context.Background(), 2, []string{root})
	if err != nil {
		t.Fatalf("scan library B: %v", err)
	}
	if addedB != 1 {
		t.Fatalf("library B added=%d want 1", addedB)
	}

	var countA, countB int
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = 1`).Scan(&countA); err != nil {
		t.Fatalf("count library A: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = 2`).Scan(&countB); err != nil {
		t.Fatalf("count library B: %v", err)
	}
	if countA != 1 || countB != 1 {
		t.Fatalf("media count A=%d B=%d want 1 each", countA, countB)
	}

	var mediaIDA, mediaIDB int64
	if err := db.QueryRow(`SELECT id FROM media WHERE library_id = 1 LIMIT 1`).Scan(&mediaIDA); err != nil {
		t.Fatalf("query media A: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM media WHERE library_id = 2 LIMIT 1`).Scan(&mediaIDB); err != nil {
		t.Fatalf("query media B: %v", err)
	}
	if mediaIDA == mediaIDB {
		t.Fatalf("libraries should have distinct media records, got same id=%d", mediaIDA)
	}

	var linkedB sql.NullInt64
	if err := db.QueryRow(`
		SELECT media_id FROM library_node
		WHERE library_id = 2 AND node_type = 'file' AND node_name = 'shared.mp4'
		LIMIT 1
	`).Scan(&linkedB); err != nil {
		t.Fatalf("query library B node: %v", err)
	}
	if !linkedB.Valid || linkedB.Int64 != mediaIDB {
		t.Fatalf("library B node media_id=%v want %d", linkedB, mediaIDB)
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

func TestScanLibraryFoldersSkipsEncryptedPlainDuplicate(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	_, err := db.Exec(`
CREATE TABLE media_encrypted_assets (
	media_id INTEGER PRIMARY KEY,
	enc_path TEXT NOT NULL,
	wrapped_dek TEXT NOT NULL,
	iv TEXT NOT NULL,
	plain_path TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'encrypted',
	updated_at TEXT DEFAULT CURRENT_TIMESTAMP
);`)
	if err != nil {
		t.Fatalf("create encrypted assets table: %v", err)
	}

	root := t.TempDir()
	plain := filepath.Join(root, "Movie.mp4")
	if err := os.WriteFile(plain, []byte("video-bytes"), 0o644); err != nil {
		t.Fatalf("write plain: %v", err)
	}
	md5, err := hashutil.MD5File(plain)
	if err != nil {
		t.Fatalf("md5 plain: %v", err)
	}
	enc := filepath.Join(root, ".encrypted", "video", "fid-1.enc")
	if err := os.MkdirAll(filepath.Dir(enc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("enc"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status, md5) VALUES (42, 7, 'fid-1', 'Movie', ?, 'video', 'active', ?)`, enc, md5)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	_, err = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (42, ?, 'aa', 'bb', ?, 'encrypted')`, enc, plain)
	if err != nil {
		t.Fatalf("insert encrypted asset: %v", err)
	}

	addedCalls := 0
	s := &Scanner{
		DB:       db,
		SkipHash: true,
		OnMediaAdded: func(int64, string, string) {
			addedCalls++
		},
	}
	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 7, []string{root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if added != 0 {
		t.Fatalf("added=%d want 0", added)
	}
	if addedCalls != 0 {
		t.Fatalf("OnMediaAdded=%d want 0", addedCalls)
	}
	var mediaCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = 7`).Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 1 {
		t.Fatalf("media count=%d want 1", mediaCount)
	}
	var keptID int64
	if err := db.QueryRow(`SELECT id FROM media WHERE library_id = 7`).Scan(&keptID); err != nil {
		t.Fatal(err)
	}
	if keptID != 42 {
		t.Fatalf("kept id=%d want 42", keptID)
	}
}

func TestScanMusicLibrarySkipsEncryptedPlainDuplicateSkipHash(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	_, err := db.Exec(`
CREATE TABLE media_encrypted_assets (
	media_id INTEGER PRIMARY KEY,
	enc_path TEXT NOT NULL,
	wrapped_dek TEXT NOT NULL,
	iv TEXT NOT NULL,
	plain_path TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'encrypted',
	updated_at TEXT DEFAULT CURRENT_TIMESTAMP
);`)
	if err != nil {
		t.Fatalf("create encrypted assets table: %v", err)
	}

	root := t.TempDir()
	plain := filepath.Join(root, "Artist - Song.mp3")
	if err := os.WriteFile(plain, []byte("fake-mp3-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	enc := filepath.Join(root, ".encrypted", "audio", "fid-audio.enc")
	if err := os.MkdirAll(filepath.Dir(enc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("enc"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (42, 3, 'fid-audio', 'Song', ?, 'audio', 'active')`, enc)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	_, err = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (42, ?, 'aa', 'bb', ?, 'encrypted')`, enc, plain)
	if err != nil {
		t.Fatalf("insert encrypted asset: %v", err)
	}

	addedCalls := 0
	s := &Scanner{
		DB:       db,
		SkipHash: true,
		OnMediaAdded: func(int64, string, string) {
			addedCalls++
		},
	}
	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 3, []string{root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if added != 0 {
		t.Fatalf("added=%d want 0", added)
	}
	if addedCalls != 0 {
		t.Fatalf("OnMediaAdded=%d want 0", addedCalls)
	}
	var mediaCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = 3`).Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 1 {
		t.Fatalf("media count=%d want 1", mediaCount)
	}
}

func TestScanLibraryFoldersAddsWhenEncryptedPlainPathReused(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	_, err := db.Exec(`
CREATE TABLE media_encrypted_assets (
	media_id INTEGER PRIMARY KEY,
	enc_path TEXT NOT NULL,
	wrapped_dek TEXT NOT NULL,
	iv TEXT NOT NULL,
	plain_path TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'encrypted',
	updated_at TEXT DEFAULT CURRENT_TIMESTAMP
);`)
	if err != nil {
		t.Fatalf("create encrypted assets table: %v", err)
	}

	root := t.TempDir()
	plain := filepath.Join(root, "Movie.mp4")
	original := []byte("original-video-content")
	replacement := []byte("replacement-video-content")
	if err := os.WriteFile(plain, replacement, 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	sum := md5.Sum(original)
	origMD5 := hex.EncodeToString(sum[:])
	enc := filepath.Join(root, ".encrypted", "video", "fid-video.enc")
	if err := os.MkdirAll(filepath.Dir(enc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("enc"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status, md5) VALUES (42, 7, 'fid-video', 'Movie', ?, 'video', 'active', ?)`, enc, origMD5)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	plainStored := filepath.Join(root, "Movie.mp4")
	_, err = db.Exec(`INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status) VALUES (42, ?, 'aa', 'bb', ?, 'encrypted')`, enc, plainStored)
	if err != nil {
		t.Fatalf("insert encrypted asset: %v", err)
	}

	addedCalls := 0
	s := &Scanner{
		DB:       db,
		SkipHash: true,
		OnMediaAdded: func(int64, string, string) {
			addedCalls++
		},
	}
	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 7, []string{root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if added != 1 {
		t.Fatalf("added=%d want 1", added)
	}
	if addedCalls != 1 {
		t.Fatalf("OnMediaAdded=%d want 1", addedCalls)
	}
	var mediaCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = 7`).Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 2 {
		t.Fatalf("media count=%d want 2", mediaCount)
	}
}

func deleteCatalogTestDDL() string {
	return `
CREATE TABLE IF NOT EXISTS favorite (media_id INTEGER);
CREATE TABLE IF NOT EXISTS favorite_folder_item (media_id INTEGER);
CREATE TABLE IF NOT EXISTS playlist_item (media_id INTEGER);
CREATE TABLE IF NOT EXISTS scrape_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS scrape_history (media_id INTEGER);
CREATE TABLE IF NOT EXISTS media_subtitle (media_id INTEGER);
CREATE TABLE IF NOT EXISTS subtitle_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS lyric_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS atrack_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS keyframe_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS preview_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS media_derived_assets (media_id INTEGER, enc_path TEXT);
CREATE TABLE IF NOT EXISTS package_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS drm_license_audit (media_id INTEGER);
CREATE TABLE IF NOT EXISTS drm_key_material (media_id INTEGER);
CREATE TABLE IF NOT EXISTS drm_asset (media_id INTEGER);
CREATE TABLE IF NOT EXISTS music_track (media_id INTEGER);
CREATE TABLE IF NOT EXISTS episode_media (media_id INTEGER);
CREATE TABLE IF NOT EXISTS photo_face (id INTEGER PRIMARY KEY, media_id INTEGER, person_id INTEGER);
CREATE TABLE IF NOT EXISTS photo_face_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS photo_classify_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS photo_location_task (media_id INTEGER);
CREATE TABLE IF NOT EXISTS transcode_task (file_id TEXT);
CREATE TABLE IF NOT EXISTS play_progress (file_id TEXT);
`
}

func TestScanLibraryFoldersRemovesMissingFiles(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	root := t.TempDir()
	moviePath := filepath.Join(root, "gone.mp4")
	if err := os.WriteFile(moviePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write movie: %v", err)
	}

	s := &Scanner{DB: db, SkipHash: true}
	if _, err := s.ScanLibraryFoldersWithContext(context.Background(), 5, []string{root}); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	if err := os.Remove(moviePath); err != nil {
		t.Fatalf("remove movie: %v", err)
	}

	removed := 0
	s.OnMediaRemoved = func(mediaID int64, filePath string) {
		removed++
	}
	if _, err := s.ScanLibraryFoldersWithContext(context.Background(), 5, []string{root}); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	var mediaCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = ?`, 5).Scan(&mediaCount); err != nil {
		t.Fatalf("count media: %v", err)
	}
	if mediaCount != 0 {
		t.Fatalf("media count=%d want 0 after file deleted", mediaCount)
	}
	if removed != 1 {
		t.Fatalf("OnMediaRemoved calls=%d want 1", removed)
	}
}

func TestScanLibraryFoldersAdoptsRenamedFileByMetadata(t *testing.T) {
	t.Parallel()

	db := newScannerTestDB(t)
	root := t.TempDir()
	oldPath := filepath.Join(root, "old-name.mp4")
	newPath := filepath.Join(root, "new-name.mp4")
	content := []byte("video-bytes")
	if err := os.WriteFile(oldPath, content, 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	sum := md5.Sum(content)
	md5hex := hex.EncodeToString(sum[:])

	fileID := uuid.NewString()
	_, err := db.Exec(`INSERT INTO media (library_id, file_id, title, file_path, file_type, duration, width, height, md5, status)
		VALUES (?, ?, 'Old', ?, 'video', 120, 1920, 1080, ?, 'active')`, 6, fileID, oldPath, md5hex)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("rename: %v", err)
	}

	s := &Scanner{DB: db, SkipHash: false}
	addedCalls := 0
	s.OnMediaAdded = func(int64, string, string) { addedCalls++ }
	if _, err := s.ScanLibraryFoldersWithContext(context.Background(), 6, []string{root}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if addedCalls != 0 {
		t.Fatalf("OnMediaAdded=%d want 0 (rename should reuse row)", addedCalls)
	}

	var count int
	var path string
	if err := db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = ?`, 6).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("media count=%d want 1", count)
	}
	if err := db.QueryRow(`SELECT file_path FROM media WHERE library_id = ?`, 6).Scan(&path); err != nil {
		t.Fatalf("path: %v", err)
	}
	if normalizeMediaPath(path) != normalizeMediaPath(newPath) {
		t.Fatalf("file_path=%q want %q", path, newPath)
	}
}

func TestScannerMaintainsPhotoTakenAt(t *testing.T) {
	db := newScannerTestDB(t)
	root := t.TempDir()
	img := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(img, []byte("not-an-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE library (id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT,type TEXT,path TEXT,scan_recursive INTEGER DEFAULT 1,scan_exclude_patterns TEXT DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('photos','photo',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	scanner := &Scanner{DB: db, SkipHash: true}
	if _, err := scanner.ScanLibraryFoldersWithContext(context.Background(), libraryID, []string{root}); err != nil {
		t.Fatal(err)
	}
	var created, taken sql.NullString
	if err := db.QueryRow(`SELECT created_at_sort,photo_taken_at FROM media WHERE library_id=?`, libraryID).Scan(&created, &taken); err != nil {
		t.Fatal(err)
	}
	if !created.Valid || !taken.Valid || len(created.String) != 27 || len(taken.String) != 27 {
		t.Fatalf("created=%v taken=%v", created, taken)
	}
}

func TestUpdateMediaMetaAndPhotoTimeGenericWriterPreservesPhotoDerivedFields(t *testing.T) {
	db := newScannerTestDB(t)
	if _, err := db.Exec(`INSERT INTO media(id,file_type,created_at_sort,photo_taken_at,photo_place_id,meta_json) VALUES(98,'image','2026-01-01T00:00:00.000000Z','2026-01-02T00:00:00.000000Z','old','{"photo":{"taken_at":"2026-01-02T00:00:00Z","place_id":"old"}}')`); err != nil {
		t.Fatal(err)
	}
	if err := updateScannerMediaMeta(context.Background(), db, 98, `{"photo":{"taken_at":"2026-01-02T00:00:00Z","place_id":"old"},"document":{"title":"doc"}}`, nil); err != nil {
		t.Fatal(err)
	}
	var taken, place string
	if err := db.QueryRow(`SELECT photo_taken_at,photo_place_id FROM media WHERE id=98`).Scan(&taken, &place); err != nil {
		t.Fatal(err)
	}
	if taken != "2026-01-02T00:00:00.000000Z" || place != "old" {
		t.Fatalf("taken=%q place=%q", taken, place)
	}
}
func TestScannerNormalizesPhotoTagsBeforeMetadataSave(t *testing.T) {
	db := newScannerTestDB(t)
	const dirty = `{"photo":{"tags":[" 保存 "," custom ","custom"]}}`
	if _, err := db.Exec(`INSERT INTO media(id,file_type,created_at_sort,photo_taken_at,meta_json) VALUES(99,'image','2026-01-01T00:00:00.000000Z','2026-01-01T00:00:00.000000Z',?)`, dirty); err != nil {
		t.Fatal(err)
	}
	if err := updateScannerMediaMeta(context.Background(), db, 99, dirty, nil); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT meta_json FROM media WHERE id=99`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"tags":["下载保存","custom"]`) {
		t.Fatalf("tags not normalized: %s", raw)
	}
}

func TestScannerDirectUpdateNormalizesPersistedPhotoTags(t *testing.T) {
	db := newScannerTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(path, []byte("not-an-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE library(id INTEGER PRIMARY KEY,name TEXT,type TEXT,path TEXT,scan_recursive INTEGER DEFAULT 1,scan_exclude_patterns TEXT DEFAULT ''); INSERT INTO library(id,name,type,path) VALUES(9,'photos','photo',?)`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status,file_mtime,created_at_sort,photo_taken_at) VALUES(99,9,'existing','photo',?,'image','{"photo":{"tags":[" ?? ","????"," custom ","custom"]}}','active',0,'2026-01-01T00:00:00.000000Z','2026-01-01T00:00:00.000000Z')`, normalizeMediaPath(path)); err != nil {
		t.Fatal(err)
	}
	s := &Scanner{DB: db, SkipHash: true}
	if _, err := s.ScanLibraryFoldersWithContext(context.Background(), 9, []string{root}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT meta_json FROM media WHERE id=99`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, ` ?? `) || strings.Contains(raw, ` custom `) {
		t.Fatalf("direct scanner update left dirty tags: %s", raw)
	}
}

func TestScannerMetadataRefreshRelinksMusicAndTV(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "metadata-refresh.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(501,'music','music','E:/music'),(502,'tv','tv','E:/tv');
        INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES
        (5001,501,'a','Song','E:/music/Song.mp3','audio','{"music":{"title":"Song","album":"New Album","artist":"Artist","album_artist":"Artist"}}','active'),
        (5002,502,'v','Show S02E03','E:/tv/Show/Show.S02E03.mkv','video','{"tv":{"series_title":"Show","season":2,"episode":3}}','active')`)
	if err != nil {
		t.Fatal(err)
	}
	scanner := &Scanner{DB: db}
	scanner.linkMusicIfTrack(501, 5001, "E:/music/Song.mp3", "")
	scanner.linkTVIfEpisode(502, 5002, "E:/tv/Show/Show.S02E03.mkv")
	var album string
	if err := db.QueryRow(`SELECT a.title FROM music_track mt JOIN music_album a ON a.id=mt.album_id WHERE mt.media_id=5001`).Scan(&album); err != nil || album != "New Album" {
		t.Fatalf("album=%q err=%v", album, err)
	}
	var season, episode int
	if err := db.QueryRow(`SELECT se.season_num,ep.episode_num FROM episode_media em JOIN episode ep ON ep.id=em.episode_id JOIN season se ON se.id=ep.season_id WHERE em.media_id=5002`).Scan(&season, &episode); err != nil || season != 2 || episode != 3 {
		t.Fatalf("season=%d episode=%d err=%v", season, episode, err)
	}
}

func TestScannerRollsBackMediaInsertWhenRelationshipLinkFails(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scanner-atomic.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	track := filepath.Join(root, "Song.mp3")
	if err := os.WriteFile(track, []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1001,'music','music',?);CREATE TRIGGER fail_scanner_track BEFORE INSERT ON music_track BEGIN SELECT RAISE(ABORT,'forced relation failure');END`, root)
	if err != nil {
		t.Fatal(err)
	}
	sc := &Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		return &ffprobe.Summary{RawJSON: `{"format":{"tags":{"title":"Song","album":"Album"}}}`}, nil
	}}
	if _, err := sc.ScanLibraryFoldersWithContext(context.Background(), 1001, []string{root}); err == nil {
		t.Fatal("expected scan error")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE library_id=1001`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("media rows=%d", n)
	}
}

func TestSyncMissingPhotoCleansFaceArtifactsAndRefreshesPerson(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scanner-photo-delete.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	preview := t.TempDir()
	missing := filepath.Join(root, "gone.jpg")
	plain := filepath.Join(preview, "photos", "faces", "7.jpg")
	enc := filepath.Join(t.TempDir(), "face.enc")
	if err = os.MkdirAll(filepath.Dir(plain), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(plain, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(enc, []byte("enc"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo',?)`, root)
	if err == nil {
		_, err = db.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'gone',?,'image','active'),(11,1,'keep',?,'image','active')`, filepath.ToSlash(missing), filepath.ToSlash(filepath.Join(root, "keep.jpg")))
	}
	if err == nil {
		_, err = db.Exec(`INSERT INTO photo_person(id,library_id,label,cover_face_id,face_count,media_count) VALUES(5,1,'p',7,2,2); INSERT INTO photo_face(id,media_id,library_id,person_id,bbox_x,bbox_y,bbox_w,bbox_h,quality) VALUES(7,10,1,5,0,0,1,1,.9),(8,11,1,5,0,0,1,1,.7)`)
	}
	if err == nil {
		_, err = db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(10,'photo_face_thumb','face:7',?,'w','i')`, enc)
	}
	if err != nil {
		t.Fatal(err)
	}
	sc := &Scanner{DB: db, PhotoCacheDir: filepath.Join(preview, "photos"), CleanupRoots: []string{filepath.Dir(enc)}}
	seen := map[string]struct{}{normalizeMediaPath(filepath.Join(root, "keep.jpg")): {}}
	if !sc.mediaPathUnderRoots(filepath.ToSlash(missing), []string{root}) {
		t.Fatalf("fixture path not under root: %q %q", missing, root)
	}
	var cleanupErr error
	sc.syncMissingMedia(context.Background(), 1, []string{root}, seen, func(_ string, e error) { cleanupErr = e })
	if cleanupErr != nil {
		t.Fatalf("cleanup error: %v", cleanupErr)
	}
	if _, err = os.Stat(plain); !os.IsNotExist(err) {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM media WHERE id=10`).Scan(&n)
		var fp string
		_ = db.QueryRow(`SELECT file_path FROM media WHERE id=10`).Scan(&fp)
		t.Fatalf("plain remains: %v media=%d fp=%q exists=%v", err, n, fp, mediaPathExistsOnDisk(fp))
	}
	if _, err = os.Stat(enc); !os.IsNotExist(err) {
		t.Fatalf("derived remains: %v", err)
	}
	var cover, faces, media int
	if err = db.QueryRow(`SELECT cover_face_id,face_count,media_count FROM photo_person WHERE id=5`).Scan(&cover, &faces, &media); err != nil {
		t.Fatal(err)
	}
	if cover != 8 || faces != 1 || media != 1 {
		t.Fatalf("stats=%d/%d/%d", cover, faces, media)
	}
}
