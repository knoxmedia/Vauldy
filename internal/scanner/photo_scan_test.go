package scanner

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"knox-media/internal/photoparse"

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
	for _, field := range []string{"title", "width", "height", "mime_type", "taken_at"} {
		if !containsString(got.MetadataAttempt.Fields, field) {
			t.Fatalf("fields=%v missing %q", got.MetadataAttempt.Fields, field)
		}
	}
	if len(got.MetadataAttempt.Errors) != 0 {
		t.Fatalf("errors=%+v", got.MetadataAttempt.Errors)
	}
}

func TestMetadataAttemptAddErrorTruncatesUTF8Safely(t *testing.T) {
	prefix := "????"
	message := strings.Repeat(prefix, 100)
	var attempt MetadataAttempt
	attempt.addError("photo", errors.New(message))
	if len(attempt.Errors) != 1 {
		t.Fatalf("errors=%v", attempt.Errors)
	}
	got := attempt.Errors[0].Message
	if len(got) > maxMetadataDiagnosticMessage {
		t.Fatalf("bytes=%d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(message, got) {
		t.Fatal("truncated message is not original prefix")
	}
}

func TestPhotoMetadataFieldsReflectAllPersistedPhotoFields(t *testing.T) {
	meta := photoparse.PhotoMeta{
		Title: "title", Width: 10, Height: 20, TakenAt: "2026-01-02T03:04:05Z",
		CameraMake: "make", CameraModel: "model", LensModel: "lens", Orientation: 6,
		MimeType: "image/jpeg", ThumbPath: "thumb", MediumPath: "medium",
		Latitude: 1.25, Longitude: 2.5, HasGPS: true, PlaceID: "place",
		LocationName: "name", LocationCity: "city", LocationProvince: "province", LocationCountry: "country",
	}
	got := photoMetadataFields(meta)
	for _, field := range []string{"title", "width", "height", "taken_at", "camera_make", "camera_model", "lens_model", "orientation", "mime_type", "thumb_path", "medium_path", "latitude", "longitude", "has_gps", "place_id", "location_name", "location_city", "location_province", "location_country"} {
		if !containsString(got, field) {
			t.Fatalf("fields=%v missing %q", got, field)
		}
	}
}

func TestScannerDamagedPhotoCarriesBestEffortDiagnostics(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken.jpg"), []byte("not-an-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "broken-photo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO library (id,name,type,path) VALUES (9,'photos','photo',?)`, root); err != nil {
		t.Fatal(err)
	}
	var got ScanDiscovery
	added, err := (&Scanner{DB: db, SkipHash: true}).ScanLibraryFoldersWithContextAndCallbacks(context.Background(), 9, []string{root}, ScanCallbacks{OnMediaDiscoveredTx: func(_ context.Context, _ *sql.Tx, discovery ScanDiscovery) error { got = discovery; return nil }})
	if err != nil || added != 1 {
		t.Fatalf("scan added=%d err=%v", added, err)
	}
	if !got.MetadataAttempt.Attempted || len(got.MetadataAttempt.Errors) == 0 {
		t.Fatalf("diagnostics=%+v", got.MetadataAttempt)
	}
	for _, diagnostic := range got.MetadataAttempt.Errors {
		if diagnostic.Source != "photo" {
			t.Fatalf("source=%q", diagnostic.Source)
		}
	}
}

func TestScannerInvokesPhotoParserOnce(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "once.png")
	if err := os.WriteFile(path, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "photo-once.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO library (id,name,type,path) VALUES (9,'photos','photo',?)`, root); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := &Scanner{DB: db, SkipHash: true, ParsePhoto: func(got string) (photoparse.PhotoMeta, []error) {
		calls++
		if got != path {
			t.Fatalf("path=%q want %q", got, path)
		}
		return photoparse.PhotoMeta{Title: "once", Width: 1, Height: 1, MimeType: "image/png"}, nil
	}}
	added, err := s.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), 9, []string{root}, ScanCallbacks{})
	if err != nil || added != 1 {
		t.Fatalf("scan added=%d err=%v", added, err)
	}
	if calls != 1 {
		t.Fatalf("photo parser calls=%d want 1", calls)
	}
}
