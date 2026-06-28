package scanner

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"

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
