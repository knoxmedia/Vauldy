package tvstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"knox-media/internal/tvparse"
)

func newTVStoreTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tvstore.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
CREATE TABLE library (id INTEGER PRIMARY KEY, type TEXT);
CREATE TABLE media (id INTEGER PRIMARY KEY, library_id INTEGER, file_path TEXT, file_type TEXT, meta_json TEXT, status TEXT DEFAULT 'active');
CREATE TABLE series (
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
);
CREATE TABLE season (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tv_id INTEGER,
    season_num INTEGER,
    name TEXT,
    poster TEXT
);
CREATE UNIQUE INDEX idx_season_series_num ON season(tv_id, season_num);
CREATE TABLE episode (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    season_id INTEGER,
    episode_num INTEGER,
    title TEXT,
    duration INTEGER,
    file_path TEXT
);
CREATE UNIQUE INDEX idx_episode_season_num ON episode(season_id, episode_num);
CREATE TABLE episode_media (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    episode_id INTEGER NOT NULL,
    media_id INTEGER NOT NULL UNIQUE,
    sort_order INTEGER DEFAULT 0
);
`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestLinkEpisode_MergesCrossFolder(t *testing.T) {
	t.Parallel()
	db := newTVStoreTestDB(t)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_path) VALUES (1, 1, '/a/S01E01.mp4'), (2, 1, '/b/S01E02.mp4')`)

	infoA := tvparse.EpisodeInfo{
		SeriesTitle: "剧集A", SeriesTitleNorm: tvparse.NormalizeSeriesTitle("剧集A"),
		SeasonNum: 1, EpisodeNum: 1, SourceFolder: "/media/TV/剧集A",
	}
	infoB := tvparse.EpisodeInfo{
		SeriesTitle: "剧集A", SeriesTitleNorm: tvparse.NormalizeSeriesTitle("剧集A"),
		SeasonNum: 1, EpisodeNum: 2, SourceFolder: "/media/anime/剧集A",
	}
	if err := LinkEpisode(db, 1, 1, infoA); err != nil {
		t.Fatalf("link A: %v", err)
	}
	if err := LinkEpisode(db, 1, 2, infoB); err != nil {
		t.Fatalf("link B: %v", err)
	}
	var seriesCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM series WHERE library_id = 1`).Scan(&seriesCount); err != nil {
		t.Fatalf("count series: %v", err)
	}
	if seriesCount != 1 {
		t.Fatalf("series count=%d want 1 (merged)", seriesCount)
	}
	var epCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM episode_media`).Scan(&epCount); err != nil {
		t.Fatalf("count episodes: %v", err)
	}
	if epCount != 2 {
		t.Fatalf("episode media count=%d want 2", epCount)
	}
}

func TestLinkEpisode_MergeByTMDBID(t *testing.T) {
	t.Parallel()
	db := newTVStoreTestDB(t)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_path) VALUES (1, 1, '/a.mp4'), (2, 1, '/b.mp4')`)

	infoA := tvparse.EpisodeInfo{
		SeriesTitle: "Show A", SeriesTitleNorm: "show a", TMDBID: "12345",
		SeasonNum: 1, EpisodeNum: 1, SourceFolder: "/folder1",
	}
	infoB := tvparse.EpisodeInfo{
		SeriesTitle: "Show A 1080p", SeriesTitleNorm: "show a",
		TMDBID: "12345", SeasonNum: 1, EpisodeNum: 2, SourceFolder: "/folder2",
	}
	_ = LinkEpisode(db, 1, 1, infoA)
	_ = LinkEpisode(db, 1, 2, infoB)

	var seriesCount int
	_ = db.QueryRow(`SELECT COUNT(1) FROM series`).Scan(&seriesCount)
	if seriesCount != 1 {
		t.Fatalf("series count=%d want 1", seriesCount)
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM series LIMIT 1`).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "Show A" {
		t.Fatalf("title=%q want Show A (must not change when adding episodes)", title)
	}
}

func TestLinkEpisode_MultiVersionSameEpisode(t *testing.T) {
	t.Parallel()
	db := newTVStoreTestDB(t)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_path) VALUES (1, 1, '/1080p.mp4'), (2, 1, '/4k.mp4')`)

	info := tvparse.EpisodeInfo{
		SeriesTitle: "My Show", SeriesTitleNorm: "my show",
		SeasonNum: 1, EpisodeNum: 1, SourceFolder: "/show",
	}
	_ = LinkEpisode(db, 1, 1, info)
	_ = LinkEpisode(db, 1, 2, info)

	var versionCount int
	_ = db.QueryRow(`
		SELECT COUNT(1) FROM episode_media em
		JOIN episode ep ON ep.id = em.episode_id
		WHERE ep.episode_num = 1
	`).Scan(&versionCount)
	if versionCount != 2 {
		t.Fatalf("versions=%d want 2", versionCount)
	}
}
