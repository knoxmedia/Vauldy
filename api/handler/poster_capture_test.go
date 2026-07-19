package handler

import (
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/scraper"
)

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
