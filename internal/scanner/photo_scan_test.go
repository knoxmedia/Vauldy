package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestScanPhotoLibraryIngestsImages(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "vacation.png")
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("not-a-video"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (9, 'photos', 'photo', ?)`, root)
	if err != nil {
		t.Fatal(err)
	}

	s := &Scanner{DB: db, SkipHash: true}
	added, err := s.ScanLibraryFoldersWithContext(context.Background(), 9, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added=%d", added)
	}

	var mediaCount int
	_ = db.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = 9 AND file_type = 'image'`).Scan(&mediaCount)
	if mediaCount != 1 {
		t.Fatalf("media=%d", mediaCount)
	}
}
