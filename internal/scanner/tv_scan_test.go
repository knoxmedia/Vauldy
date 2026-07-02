package scanner

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func newTVScannerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newScannerTestDB(t)
	_, _ = db.Exec(`CREATE TABLE library (id INTEGER PRIMARY KEY, type TEXT)`)
	_, _ = db.Exec(`CREATE TABLE series (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		library_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		title_norm TEXT NOT NULL,
		year INTEGER,
		tmdb_id TEXT,
		tvdb_id TEXT,
		poster TEXT,
		folder_paths TEXT DEFAULT '[]',
		meta_json TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	_, _ = db.Exec(`CREATE TABLE season (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		tv_id INTEGER,
		season_num INTEGER,
		name TEXT,
		poster TEXT
	)`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX idx_season_series_num ON season(tv_id, season_num)`)
	_, _ = db.Exec(`CREATE TABLE episode (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		season_id INTEGER,
		episode_num INTEGER,
		title TEXT,
		duration INTEGER,
		file_path TEXT
	)`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX idx_episode_season_num ON episode(season_id, episode_num)`)
	_, _ = db.Exec(`CREATE TABLE episode_media (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		episode_id INTEGER NOT NULL,
		media_id INTEGER NOT NULL UNIQUE,
		sort_order INTEGER DEFAULT 0
	)`)
	_, err := db.Exec(`INSERT INTO library (id, type) VALUES (10, 'tv')`)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	return db
}

func TestScanLibraryFoldersTV(t *testing.T) {
	t.Parallel()
	db := newTVScannerTestDB(t)
	root := filepath.Join(t.TempDir(), "TV")
	showDir := filepath.Join(root, "剧集A", "Season 01")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "剧集A - S01E01.mp4"), []byte("v"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := &Scanner{DB: db, SkipHash: true}
	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 10, []string{root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if added != 1 {
		t.Fatalf("added=%d want 1", added)
	}
	var seriesCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM series WHERE library_id = 10`).Scan(&seriesCount); err != nil {
		t.Fatalf("series count: %v", err)
	}
	if seriesCount != 1 {
		t.Fatalf("series count=%d want 1", seriesCount)
	}
}

func TestScanLibraryFoldersChineseFlatEpisodes(t *testing.T) {
	t.Parallel()
	db := newTVScannerTestDB(t)
	root := filepath.Join(t.TempDir(), "movies", "电视剧")
	showDir := filepath.Join(root, "去有风的地方")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"去有风的地方第1集.mp4", "去有风的地方第2集.mp4"} {
		if err := os.WriteFile(filepath.Join(showDir, name), []byte("v"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	s := &Scanner{DB: db, SkipHash: true}
	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 10, []string{root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if added != 2 {
		t.Fatalf("added=%d want 2", added)
	}
	var seriesCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM series WHERE library_id = 10`).Scan(&seriesCount); err != nil {
		t.Fatalf("series count: %v", err)
	}
	if seriesCount != 1 {
		t.Fatalf("series count=%d want 1", seriesCount)
	}
	var epCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM episode_media`).Scan(&epCount); err != nil {
		t.Fatalf("episode media: %v", err)
	}
	if epCount != 2 {
		t.Fatalf("episode media=%d want 2", epCount)
	}
}
