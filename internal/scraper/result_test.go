package scraper

import "testing"

func TestHasScrapePoster(t *testing.T) {
	if HasScrapePoster(&ScrapeResult{Poster: "https://image.tmdb.org/x.jpg"}) != true {
		t.Fatal("top-level poster")
	}
	if HasScrapePoster(&ScrapeResult{Extra: map[string]any{"poster": "https://x/p.jpg"}}) != true {
		t.Fatal("extra poster")
	}
	if HasScrapePoster(&ScrapeResult{Title: "x"}) {
		t.Fatal("title alone is not a poster")
	}
}
