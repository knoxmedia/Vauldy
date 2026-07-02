package handler

import (
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
