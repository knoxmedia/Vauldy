package tvstore

import (
	"path/filepath"
	"testing"
)

func TestBackfillLibraryTV(t *testing.T) {
	db := newTVStoreTestDB(t)
	meta := `{"tv":{"series_title":"Show","season":1,"episode":1}}`
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_path, file_type, meta_json, status) VALUES (1, 1, 'episode.mp4', 'video', ?, 'active')`, meta); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	linked, err := BackfillLibraryTV(db, 1)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if linked != 1 {
		t.Fatalf("linked=%d want 1", linked)
	}
	var seriesCount int
	_ = db.QueryRow(`SELECT COUNT(1) FROM series WHERE library_id = 1`).Scan(&seriesCount)
	if seriesCount != 1 {
		t.Fatalf("series count=%d want 1", seriesCount)
	}
}

func TestBackfillLibraryTV_FolderGroups(t *testing.T) {
	t.Parallel()
	db := newTVStoreTestDB(t)
	paths := []struct {
		id   int64
		path string
	}{
		{1, filepath.FromSlash(`K:/movies/电视剧/宿醉/宿醉.mp4`)},
		{2, filepath.FromSlash(`K:/movies/电视剧/宿醉/宿醉2.mp4`)},
		{3, filepath.FromSlash(`K:/movies/电视剧/奇思妙探第三季/01.mp4`)},
	}
	for _, p := range paths {
		if _, err := db.Exec(`INSERT INTO media (id, library_id, file_path, file_type, status) VALUES (?, 1, ?, 'video', 'active')`, p.id, p.path); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	linked, err := BackfillLibraryTV(db, 1)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if linked != 3 {
		t.Fatalf("linked=%d want 3", linked)
	}
	var seriesCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM series WHERE library_id = 1`).Scan(&seriesCount); err != nil {
		t.Fatalf("series: %v", err)
	}
	if seriesCount != 2 {
		t.Fatalf("series count=%d want 2", seriesCount)
	}
}
