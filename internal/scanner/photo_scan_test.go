package scanner

import (
	"context"
	"database/sql"
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

func TestScannerPhotoDiscoveryCarriesMetadataAttempt(t *testing.T) {
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
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "photo-diagnostics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO library (id,name,type,path) VALUES (9,'photos','photo',?)`, root); err != nil {
		t.Fatal(err)
	}
	var got ScanDiscovery
	added, err := (&Scanner{DB: db, SkipHash: true}).ScanLibraryFoldersWithContextAndCallbacks(context.Background(), 9, []string{root}, ScanCallbacks{
		OnMediaDiscoveredTx: func(_ context.Context, _ *sql.Tx, discovery ScanDiscovery) error { got = discovery; return nil },
	})
	if err != nil || added != 1 {
		t.Fatalf("scan added=%d err=%v", added, err)
	}
	if !got.MetadataAttempt.Attempted {
		t.Fatal("photo metadata attempt not recorded")
	}
	for _, field := range []string{"title", "width", "height", "format", "taken_at"} {
		if !containsString(got.MetadataAttempt.Fields, field) {
			t.Fatalf("fields=%v missing %q", got.MetadataAttempt.Fields, field)
		}
	}
	if len(got.MetadataAttempt.Errors) != 0 {
		t.Fatalf("errors=%+v", got.MetadataAttempt.Errors)
	}
}
