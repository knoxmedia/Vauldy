package scraper

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// roundTripRecorder wraps a RoundTripper and records every outbound request path.
type roundTripRecorder struct {
	base  http.RoundTripper
	paths *[]string
}

func (r roundTripRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	*r.paths = append(*r.paths, req.URL.Path)
	return r.base.RoundTrip(req)
}

func TestFetchImageCandidatesOnlyConfiguredSources(t *testing.T) {
	mux := http.NewServeMux()

	// TMDb search → id 123
	mux.HandleFunc("/3/search/multi", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"id": 123, "media_type": "movie", "title": "Mock", "poster_path": "/p1.jpg"},
			},
		})
	})
	// TMDb full image set → multiple posters
	mux.HandleFunc("/3/movie/123/images", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"posters": []map[string]any{
				{"file_path": "/p1.jpg"},
				{"file_path": "/p2.jpg"},
			},
			"backdrops": []map[string]any{{"file_path": "/b1.jpg"}},
		})
	})
	// Fanart for tmdb_id 123
	mux.HandleFunc("/v3/movies/123", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"movieposter": []map[string]any{
				{"url": "https://fanart.test/p1.jpg"},
				{"url": "https://fanart.test/p2.jpg"},
			},
		})
	})
	// Bangumi search
	mux.HandleFunc("/search/subject/Mock", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"list": []map[string]any{
				{"name": "Mock", "images": map[string]any{"large": "https://bgm.test/large.jpg"}},
			},
		})
	})
	// Douban
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"target": map[string]any{"cover_url": "https://douban.test/p.jpg"}},
			},
		})
	})

	useMockOnlineHTTP(t, mux)

	// Track every outbound request path so we can assert which sources were contacted.
	var paths []string
	baseRT := onlineHTTP.Transport
	onlineHTTP.Transport = roundTripRecorder{base: baseRT, paths: &paths}
	t.Cleanup(func() { onlineHTTP.Transport = baseRT })

	cfg := Config{
		ImageSources: []string{"tmdb", "fanart"}, // bangumi/douban NOT configured
		APIKeys:      map[string]string{"tmdb": "k", "fanart": "fk"},
	}
	cands, errs, scraped := FetchImageCandidates(cfg, "Mock", 0, "poster", "")

	if !scraped {
		t.Fatalf("expected scraped=true when online sources configured")
	}
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if len(cands) == 0 {
		t.Fatalf("expected candidates, got none")
	}
	// Should contain TMDb's two posters + fanart's two posters.
	var tmdbCnt, fanartCnt int
	for _, c := range cands {
		switch c.Source {
		case "tmdb":
			tmdbCnt++
		case "fanart":
			fanartCnt++
		}
	}
	if tmdbCnt != 2 {
		t.Fatalf("expected 2 tmdb candidates, got %d (cands=%v)", tmdbCnt, cands)
	}
	if fanartCnt != 2 {
		t.Fatalf("expected 2 fanart candidates, got %d (cands=%v)", fanartCnt, cands)
	}

	// Unconfigured sources must NOT be contacted: no bangumi or douban requests.
	for _, p := range paths {
		if strings.Contains(p, "/search/subject/") || strings.Contains(p, "/search") && !strings.Contains(p, "/3/search/multi") {
			t.Fatalf("unconfigured source was contacted: %s", p)
		}
	}
}

func TestFetchImageCandidatesEmptyWhenNoSourcesConfigured(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/3/search/multi", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("tmdb should not be contacted when not configured")
	})
	useMockOnlineHTTP(t, mux)

	cfg := Config{ImageSources: []string{}, APIKeys: map[string]string{"tmdb": "k"}}
	cands, _, scraped := FetchImageCandidates(cfg, "Mock", 0, "poster", "")
	// Default fallback is tmdb only — but the mock /3/search/multi is wired to fail the
	// test if hit. An empty config falls back to {"tmdb"} per applyImageSources parity.
	// To honor the user's "skip unconfigured" intent when ImageSources is explicitly empty,
	// we instead expect zero candidates here.
	if scraped {
		t.Fatalf("expected scraped=false when no online image sources configured")
	}
	if len(cands) != 0 {
		t.Fatalf("expected no candidates for empty config, got %v", cands)
	}
}
