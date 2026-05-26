package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestScanMusicLibraryLinksTracks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Artist - Song One.mp3")
	if err := os.WriteFile(path, []byte("fake-mp3-content-one"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (3, 'music', 'music', ?)`, root)
	if err != nil {
		t.Fatal(err)
	}

	s := &Scanner{DB: db, SkipHash: true}
	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 3, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added=%d", added)
	}

	var mediaCount, trackCount, albumCount int
	_ = db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = 3 AND file_type = 'audio'`).Scan(&mediaCount)
	_ = db.QueryRow(`SELECT COUNT(1) FROM music_track mt JOIN media m ON m.id = mt.media_id WHERE m.library_id = 3`).Scan(&trackCount)
	_ = db.QueryRow(`SELECT COUNT(1) FROM music_album WHERE library_id = 3`).Scan(&albumCount)
	if mediaCount != 1 || trackCount != 1 || albumCount != 1 {
		t.Fatalf("media=%d track=%d album=%d", mediaCount, trackCount, albumCount)
	}
}
