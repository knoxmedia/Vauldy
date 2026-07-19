package handler

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/scraper"
	"knox-media/internal/store"
)

func posterHandlerTestDB(t *testing.T) (*sql.DB, int64) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "poster-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lr, err := db.Exec(`INSERT INTO library(name,type,path,image_providers) VALUES('posters','movie',?,'embedded,screen_grabber')`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lid, _ := lr.LastInsertId()
	mr, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,title,file_type,status,meta_json) VALUES(?,'poster-media','movie.mp4','movie','video','active','{}')`, lid)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := mr.LastInsertId()
	return db, mid
}

func TestImageSourceEnabled(t *testing.T) {
	cfg := scraper.Config{ImageSources: []string{"tmdb", "screen_grabber"}}
	if !imageSourceEnabled(cfg, "screen_grabber") {
		t.Fatal("expected screen_grabber enabled")
	}
	if imageSourceEnabled(cfg, "embedded") {
		t.Fatal("embedded should be disabled")
	}
}

func TestHasScrapePosterSkipsLocalCapture(t *testing.T) {
	res := &scraper.ScrapeResult{
		Poster: "https://image.tmdb.org/t/p/w500/x.jpg",
		Extra:  map[string]any{},
	}
	if !scraper.HasScrapePoster(res) {
		t.Fatal("expected poster detected")
	}
}

func TestPosterSnapSecond(t *testing.T) {
	if posterSnapSecond(0) != 10 {
		t.Fatalf("zero duration")
	}
	if posterSnapSecond(600) != 120 {
		t.Fatalf("20%% of 600")
	}
	if posterSnapSecond(3600) != 180 {
		t.Fatalf("cap at 180")
	}
}

func TestPublishCapturedPosterReplacesPlainPosterAtomically(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "9.jpg")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	staged, err := newPosterStagingPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{}
	url, source := h.publishCapturedPoster(9, staged, target, "embedded")
	if url != "/uploads/posters/9.jpg" || source != "embedded" {
		t.Fatalf("publish=(%q,%q)", url, source)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("poster=%q, want new", got)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged file still exists: %v", err)
	}
}

func TestNewPosterStagingPathDoesNotOverwriteTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "9.jpg")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	staged, err := newPosterStagingPath(target)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(staged)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("target changed during staging: %q", got)
	}
}
