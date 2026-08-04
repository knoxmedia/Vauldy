package scraper

import (
	"encoding/json"
	"net/http"
	"testing"
)

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

func TestSearchOMDbCandidatePreservesIMDbID(t *testing.T) {
	useMockOnlineHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Response": "True", "Title": "Example", "Released": "18 Jul 2026", "imdbID": "tt123",
		})
	}))
	items, err := SearchMatchCandidates("Example", 2026, "omdb", "en", Config{APIKeys: map[string]string{"omdb": "key"}}, 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(items) != 1 || items[0].ExternalID != "tt123" {
		t.Fatalf("items=%+v want ExternalID tt123", items)
	}
}

func TestSearchOMDbCandidateNormalizesMediaType(t *testing.T) {
	tests := []struct {
		name     string
		omdbType string
		want     string
	}{
		{name: "series", omdbType: "series", want: "tv"},
		{name: "movie", omdbType: "movie", want: "movie"},
		{name: "episode is not a series candidate", omdbType: "episode", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useMockOnlineHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Response": "True", "Title": "Example", "Released": "18 Jul 2026",
					"imdbID": "tt123", "Type": tt.omdbType,
				})
			}))
			items, err := SearchMatchCandidates("Example", 2026, "omdb", "en", Config{APIKeys: map[string]string{"omdb": "key"}}, 1)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(items) != 1 || items[0].MediaType != tt.want {
				t.Fatalf("items=%+v want MediaType %q", items, tt.want)
			}
		})
	}
}
