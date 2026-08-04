package scraper

import (
	"encoding/json"
	"testing"
)

func TestPreserveScrapeImagesFromExisting(t *testing.T) {
	res := &ScrapeResult{Title: "new", Extra: map[string]any{}}
	existing := `{"scrape":{"poster":"/uploads/posters/9.jpg","backdrop":"https://x/b.jpg"}}`
	PreserveScrapeImagesFromExisting(res, existing)
	if res.Poster != "/uploads/posters/9.jpg" {
		t.Fatalf("poster=%q", res.Poster)
	}
	if res.Backdrop != "https://x/b.jpg" {
		t.Fatalf("backdrop=%q", res.Backdrop)
	}
}

func TestPreserveScrapeImagesDoesNotOverwriteNew(t *testing.T) {
	res := &ScrapeResult{Poster: "/metadata/library/1/poster.jpg"}
	existing := `{"scrape":{"poster":"/uploads/posters/9.jpg"}}`
	PreserveScrapeImagesFromExisting(res, existing)
	if res.Poster != "/metadata/library/1/poster.jpg" {
		t.Fatalf("poster replaced: %q", res.Poster)
	}
}

func TestMergeSeriesFieldsPreservingEpisodeKeepsEpisodePoster(t *testing.T) {
	existing := `{"scrape":{"poster":"/uploads/posters/9.jpg","backdrop":"/uploads/posters/b.jpg","title":"Show S01E01","extra":{"poster":"/uploads/posters/9.jpg","episode":1,"season":1,"episode_still":"https://x/still.jpg","series_title":"Show"}}}`
	patch := map[string]any{"scrape": map[string]any{"series_title": "Show", "series_overview": "New Overview", "series_poster": "https://image.tmdb.org/s.jpg", "series_backdrop": "https://image.tmdb.org/b.jpg", "extra": map[string]any{"series_title": "Show", "series_overview": "New Overview", "series_poster": "https://image.tmdb.org/s.jpg", "series_backdrop": "https://image.tmdb.org/b.jpg", "tmdb_id": "123", "tmdb_type": "tv", "tvdb_id": "456"}}}
	merged, err := MergeSeriesFieldsPreservingEpisode(existing, patch)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(merged), &root); err != nil {
		t.Fatal(err)
	}
	scrape, _ := root["scrape"].(map[string]any)
	if scrape == nil {
		t.Fatalf("missing scrape object: %s", merged)
	}
	if scrape["poster"] != "/uploads/posters/9.jpg" {
		t.Fatalf("episode poster was dropped: %v", scrape["poster"])
	}
	if scrape["title"] != "Show S01E01" {
		t.Fatalf("episode title was dropped: %v", scrape["title"])
	}
	if scrape["series_title"] != "Show" {
		t.Fatalf("series_title not added: %v", scrape["series_title"])
	}
	if scrape["series_overview"] != "New Overview" {
		t.Fatalf("series_overview not added: %v", scrape["series_overview"])
	}
	extra, _ := scrape["extra"].(map[string]any)
	if extra == nil {
		t.Fatalf("missing extra object: %s", merged)
	}
	if extra["poster"] != "/uploads/posters/9.jpg" {
		t.Fatalf("extra poster was dropped: %v", extra["poster"])
	}
	if extra["episode"] != float64(1) || extra["season"] != float64(1) {
		t.Fatalf("episode fields were dropped: %v", extra)
	}
	if extra["series_poster"] != "https://image.tmdb.org/s.jpg" || extra["tmdb_id"] != "123" {
		t.Fatalf("series fields not merged into extra: %v", extra)
	}
}

func TestMergeSeriesFieldsPreservingEpisodeCreatesScrapeWhenAbsent(t *testing.T) {
	merged, err := MergeSeriesFieldsPreservingEpisode(`{"sibling":true}`, map[string]any{"scrape": map[string]any{"series_title": "Show", "extra": map[string]any{"tmdb_id": "9"}}})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(merged), &root); err != nil {
		t.Fatal(err)
	}
	if root["sibling"] != true {
		t.Fatalf("non-scrape keys were dropped: %s", merged)
	}
	scrape, _ := root["scrape"].(map[string]any)
	if scrape == nil || scrape["series_title"] != "Show" {
		t.Fatalf("scrape not created correctly: %s", merged)
	}
	extra, _ := scrape["extra"].(map[string]any)
	if extra == nil || extra["tmdb_id"] != "9" {
		t.Fatalf("extra not created correctly: %s", merged)
	}
}
