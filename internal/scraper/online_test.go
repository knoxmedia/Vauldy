package scraper

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type rewriteTransport struct {
	base *url.URL
	rt   http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	u2 := *r2.URL
	u2.Scheme = t.base.Scheme
	u2.Host = t.base.Host
	r2.URL = &u2
	if t.rt == nil {
		return http.DefaultTransport.RoundTrip(r2)
	}
	return t.rt.RoundTrip(r2)
}

func useMockOnlineHTTP(t *testing.T, h http.Handler) {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	oldClient := onlineHTTP
	onlineHTTP = &http.Client{
		Timeout:   5 * time.Second,
		Transport: &rewriteTransport{base: u},
	}
	t.Cleanup(func() { onlineHTTP = oldClient })
}

func newProviderMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/3/configuration", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"images": map[string]any{"base_url": "https://image.tmdb.org/t/p/"},
		})
	})

	mux.HandleFunc("/3/search/multi", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{
					"media_type":    "movie",
					"title":         "Mock TMDB Title",
					"overview":      "tmdb overview",
					"poster_path":   "/poster.jpg",
					"backdrop_path": "/back.jpg",
					"release_date":  "2024-03-01",
					"vote_average":  8.2,
					"id":            123,
				},
			},
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Response":   "True",
			"Title":      "Mock OMDb Title",
			"Plot":       "omdb plot",
			"Poster":     "https://omdb.test/poster.jpg",
			"Released":   "01 Jan 2020",
			"imdbRating": "7.8",
			"Genre":      "Drama, Mystery",
		})
	})

	mux.HandleFunc("/search/subject/Inception", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"list": []map[string]any{
				{
					"name":     "Bangumi Name",
					"name_cn":  "Bangumi CN",
					"summary":  "bangumi summary",
					"air_date": "2021-08-10",
					"images": map[string]any{
						"large": "https://bgm.test/large.jpg",
					},
					"rating": map[string]any{
						"score": 7.5,
					},
				},
			},
		})
	})

	mux.HandleFunc("/search/subject/TestMovie", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"list": []map[string]any{
				{
					"name":     "Bangumi Name",
					"name_cn":  "Bangumi CN",
					"summary":  "bangumi summary",
					"air_date": "2021-08-10",
					"images": map[string]any{
						"large": "https://bgm.test/large.jpg",
					},
					"rating": map[string]any{
						"score": 7.5,
					},
				},
			},
		})
	})

	mux.HandleFunc("/v4/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"token": "tvdb-token",
			},
		})
	})

	mux.HandleFunc("/v4/search", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"name":       "Mock TVDB Series",
					"overview":   "tvdb overview",
					"firstAired": "2022-01-01",
				},
			},
		})
	})

	mux.HandleFunc("/j/subject_suggest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":        "12345",
				"title":     "Mock Douban",
				"year":      "2019",
				"type":      "movie",
				"img":       "https://douban.test/poster.jpg",
				"sub_title": "douban subtitle",
			},
		})
	})
	mux.HandleFunc("/j/subject_abstract", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"subject": map[string]any{
				"title":      "Mock Douban",
				"short_info": "douban overview",
				"pic_url":    "https://douban.test/poster-large.jpg",
				"rating":     map[string]any{"value": 8.2},
			},
		})
	})

	mux.HandleFunc("/v3/movies/27205", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("api-key")) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"movieposter":     []map[string]any{{"url": "https://fanart.test/poster.jpg"}},
			"moviebackground": []map[string]any{{"url": "https://fanart.test/bg.jpg"}},
			"hdmovielogo":     []map[string]any{{"url": "https://fanart.test/logo.png"}},
		})
	})

	mux.HandleFunc("/v3/movies/123", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("api-key")) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"movieposter": []map[string]any{{"url": "https://fanart.test/poster.jpg"}},
			"moviebackground": []map[string]any{{"url": "https://fanart.test/bg.jpg"}},
			"hdmovielogo": []map[string]any{{"url": "https://fanart.test/logo.png"}},
		})
	})

	return mux
}

func TestProviderScrapers(t *testing.T) {
	useMockOnlineHTTP(t, newProviderMux())

	t.Run("tmdb", func(t *testing.T) {
		res, err := scrapeTMDB("TestMovie", 2024, "tmdb-key")
		if err != nil {
			t.Fatalf("scrapeTMDB error: %v", err)
		}
		if res.Source != "tmdb" || res.Title == "" || res.Poster == "" {
			t.Fatalf("unexpected tmdb result: %+v", res)
		}
		if res.Extra["tmdb_id"] != int64(123) {
			t.Fatalf("unexpected tmdb_id: %#v", res.Extra["tmdb_id"])
		}
	})

	t.Run("omdb", func(t *testing.T) {
		res, err := scrapeOMDb("TestMovie", 2020, "omdb-key")
		if err != nil {
			t.Fatalf("scrapeOMDb error: %v", err)
		}
		if res.Source != "omdb" || len(res.Genres) != 2 || res.Rating <= 0 {
			t.Fatalf("unexpected omdb result: %+v", res)
		}
	})

	t.Run("douban", func(t *testing.T) {
		res, err := scrapeDouban("TestMovie", 0)
		if err != nil {
			t.Fatalf("scrapeDouban error: %v", err)
		}
		if res.Source != "douban" || res.Poster == "" || res.Title != "Mock Douban" {
			t.Fatalf("unexpected douban result: %+v", res)
		}
	})

	t.Run("tvdb", func(t *testing.T) {
		res, err := scrapeTVDB("TestMovie", 2022, "tvdb-key:pin")
		if err != nil {
			t.Fatalf("scrapeTVDB error: %v", err)
		}
		if res.Source != "tvdb" || res.Title == "" || res.ReleaseDate == "" {
			t.Fatalf("unexpected tvdb result: %+v", res)
		}
	})

	t.Run("bangumi", func(t *testing.T) {
		res, err := scrapeBangumi("TestMovie")
		if err != nil {
			t.Fatalf("scrapeBangumi error: %v", err)
		}
		if res.Source != "bangumi" || res.Title != "Bangumi CN" || res.Poster == "" {
			t.Fatalf("unexpected bangumi result: %+v", res)
		}
	})

	t.Run("fanart", func(t *testing.T) {
		res, err := scrapeFanart("123", "fanart-key")
		if err != nil {
			t.Fatalf("scrapeFanart error: %v", err)
		}
		if res.Source != "fanart" || res.Poster == "" || res.Logo == "" {
			t.Fatalf("unexpected fanart result: %+v", res)
		}
	})
}

func TestScrapeOnlineAggregateAllProviders(t *testing.T) {
	useMockOnlineHTTP(t, newProviderMux())

	cfg := Config{
		Providers: []string{"tmdb", "omdb", "douban", "tvdb", "bangumi", "fanart"},
		APIKeys: map[string]string{
			"tmdb":   "tmdb-key",
			"omdb":   "omdb-key",
			"tvdb":   "tvdb-key:pin",
			"fanart": "fanart-key",
		},
	}
	res, err := ScrapeOnline("TestMovie 2024", "tmdb", cfg)
	if err != nil {
		t.Fatalf("ScrapeOnline error: %v", err)
	}
	if res.Source != "online-aggregate" {
		t.Fatalf("unexpected source: %s", res.Source)
	}
	if res.Poster == "" || res.Overview == "" {
		t.Fatalf("aggregate missing key fields: %+v", res)
	}
	if gotID, ok := res.Extra["tmdb_id"]; !ok || gotID != int64(123) {
		t.Fatalf("aggregate tmdb_id mismatch: %#v", res.Extra["tmdb_id"])
	}
}

func TestClassifyProviderErrorCategories(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		errText  string
		expected string
	}{
		{name: "key missing", errText: "tmdb api key missing", expected: "key_missing"},
		{name: "auth error", errText: "http 401", expected: "auth_error"},
		{name: "quota limited", errText: "http 429", expected: "quota_limited"},
		{name: "network", errText: "dial tcp timeout", expected: "network_error"},
		{name: "no result", errText: "douban empty", expected: "no_result"},
		{name: "remote default", errText: "http 500", expected: "remote_error"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := classifyProviderError("tmdb", &testErr{msg: tc.errText})
			if got["category"] != tc.expected {
				t.Fatalf("category=%q want %q for %q", got["category"], tc.expected, tc.errText)
			}
		})
	}
}

func TestScrapeOnlineImagesWhenMetadataFails(t *testing.T) {
	useMockOnlineHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/3/search/multi"):
			w.WriteHeader(http.StatusTooManyRequests)
		case strings.HasPrefix(r.URL.Path, "/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Response":   "True",
				"Title":      "Image Only",
				"Plot":       "plot",
				"Poster":     "https://ok/poster.jpg",
				"Released":   "01 Jan 2023",
				"imdbRating": "7.0",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	cfg := Config{
		Providers:    []string{"tmdb"},
		ImageSources: []string{"omdb"},
		APIKeys: map[string]string{
			"tmdb": "tmdb-key",
			"omdb": "omdb-key",
		},
	}
	res, err := ScrapeOnline("NoMetaMovie", "tmdb", cfg)
	if err != nil {
		t.Fatalf("expected success with images, got error: %v", err)
	}
	if res.Poster != "https://ok/poster.jpg" {
		t.Fatalf("poster=%q want image from omdb", res.Poster)
	}
	if res.Overview != "" {
		t.Fatalf("overview should be empty when metadata failed, got %q", res.Overview)
	}
	providerErrors, ok := res.Extra["provider_errors"].(map[string]map[string]string)
	if !ok || providerErrors["tmdb"]["category"] != "quota_limited" {
		t.Fatalf("expected tmdb metadata error, got %#v", res.Extra["provider_errors"])
	}
}

func TestScrapeOnlineProviderErrorsSummary(t *testing.T) {
	useMockOnlineHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/3/search/multi"):
			w.WriteHeader(http.StatusTooManyRequests) // tmdb -> quota_limited
		case strings.HasPrefix(r.URL.Path, "/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Response": "False"}) // omdb -> no_result
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	cfg := Config{
		Providers: []string{"tmdb", "omdb"},
		APIKeys: map[string]string{
			"tmdb": "tmdb-key",
			"omdb": "omdb-key",
		},
	}
	res, err := ScrapeOnline("NoResultMovie", "tmdb", cfg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if res == nil || res.Title != "NoResultMovie" {
		t.Fatalf("expected partial result, got %#v", res)
	}
	msg := err.Error()
	if !strings.Contains(msg, "all providers failed:") {
		t.Fatalf("unexpected error: %s", msg)
	}
	if !strings.Contains(msg, "tmdb:quota_limited") {
		t.Fatalf("missing tmdb quota summary: %s", msg)
	}
	if !strings.Contains(msg, "omdb:no_result") {
		t.Fatalf("missing omdb no_result summary: %s", msg)
	}
}

func TestScrapeOnlineManualSourceUsesConfiguredProviders(t *testing.T) {
	useMockOnlineHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Response":   "True",
				"Title":      "Matched",
				"Plot":       "plot",
				"Poster":     "https://ok/poster.jpg",
				"Released":   "01 Jan 2020",
				"imdbRating": "7.0",
				"Genre":      "Drama",
			})
		}
	}))

	cfg := Config{
		Providers: []string{"omdb"},
		APIKeys:   map[string]string{"omdb": "omdb-key"},
	}
	res, err := ScrapeOnline("Test Movie", "manual", cfg)
	if err != nil {
		t.Fatalf("ScrapeOnline(manual): %v", err)
	}
	if !HasMeaningfulScrapeData(res) {
		t.Fatalf("expected data: %+v", res)
	}
	pe, _ := res.Extra["provider_errors"].(map[string]map[string]string)
	if pe["manual"] != nil {
		t.Fatalf("manual should not be treated as provider: %#v", pe)
	}
}

func TestScrapeOnlineFallbackWithProviderErrors(t *testing.T) {
	useMockOnlineHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/3/search/multi"):
			w.WriteHeader(http.StatusUnauthorized) // tmdb auth_error
		case strings.HasPrefix(r.URL.Path, "/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Response":   "True",
				"Title":      "OMDb Success",
				"Plot":       "success plot",
				"Poster":     "https://ok/poster.jpg",
				"Released":   "01 Jan 2023",
				"imdbRating": "8.1",
				"Genre":      "Drama",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	cfg := Config{
		Providers: []string{"tmdb", "omdb"},
		APIKeys: map[string]string{
			"tmdb": "tmdb-key",
			"omdb": "omdb-key",
		},
	}
	res, err := ScrapeOnline("Some Movie", "tmdb", cfg)
	if err != nil {
		t.Fatalf("ScrapeOnline unexpected error: %v", err)
	}
	if res.Title != "Some Movie" { // title set from search keyword by aggregate root
		t.Fatalf("unexpected title: %s", res.Title)
	}
	providerErrors, ok := res.Extra["provider_errors"].(map[string]map[string]string)
	if !ok {
		t.Fatalf("provider_errors type mismatch: %#v", res.Extra["provider_errors"])
	}
	if providerErrors["tmdb"]["category"] != "auth_error" {
		t.Fatalf("tmdb category=%q want auth_error", providerErrors["tmdb"]["category"])
	}
}

type testErr struct {
	msg string
}

func (e *testErr) Error() string { return e.msg }
