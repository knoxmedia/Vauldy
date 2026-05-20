package scraper

import "testing"

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
