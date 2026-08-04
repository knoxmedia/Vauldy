package scraper

import "testing"

func TestApplyMatchCandidateFields(t *testing.T) {
	res := &ScrapeResult{Title: "Test", Source: "douban"}
	ApplyMatchCandidateFields(res, "https://img.example/p.jpg", "plot")
	if res.Poster != "https://img.example/p.jpg" {
		t.Fatalf("poster: %q", res.Poster)
	}
	if res.Overview != "plot" {
		t.Fatalf("overview: %q", res.Overview)
	}
	if res.Extra["poster"] != "https://img.example/p.jpg" {
		t.Fatalf("extra poster missing")
	}

	keep := &ScrapeResult{
		Title:    "Keep",
		Poster:   "https://existing/p.jpg",
		Overview: "existing plot",
		Extra:    map[string]any{"poster": "https://existing/p.jpg"},
	}
	ApplyMatchCandidateFields(keep, "https://new/p.jpg", "new plot")
	if keep.Poster != "https://existing/p.jpg" || keep.Overview != "existing plot" {
		t.Fatalf("should not overwrite existing fields: %+v", keep)
	}
}
