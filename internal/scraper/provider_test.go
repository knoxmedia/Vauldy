package scraper

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestProviderConnectivityCases(t *testing.T) {
	useMockOnlineHTTP(t, newProviderMux())

	tests := []struct {
		name     string
		provider string
		keys     map[string]string
		wantOK   bool
	}{
		{name: "tmdb ok", provider: "tmdb", keys: map[string]string{"tmdb": "tmdb-key"}, wantOK: true},
		{name: "tmdb missing key", provider: "tmdb", keys: map[string]string{}, wantOK: false},
		{name: "omdb ok", provider: "omdb", keys: map[string]string{"omdb": "omdb-key"}, wantOK: true},
		{name: "bangumi ok", provider: "bangumi", keys: map[string]string{}, wantOK: true},
		{name: "tvdb ok", provider: "tvdb", keys: map[string]string{"tvdb": "tvdb-key"}, wantOK: true},
		{name: "douban ok", provider: "douban", keys: map[string]string{}, wantOK: true},
		{name: "fanart ok", provider: "fanart", keys: map[string]string{"fanart": "fanart-key"}, wantOK: true},
		{name: "fanart missing key", provider: "fanart", keys: map[string]string{}, wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckProviderConnectivity(tc.provider, tc.keys)
			if got.OK != tc.wantOK {
				t.Fatalf("ok=%v message=%q", got.OK, got.Message)
			}
		})
	}
}

func TestProviderConnectivityTMDBAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "bad" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"images": map[string]any{"base_url": "https://image.tmdb.org/t/p/"},
			})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	old := onlineHTTP
	onlineHTTP = &http.Client{Transport: &rewriteTransport{base: u}}
	t.Cleanup(func() { onlineHTTP = old })

	ok := testTMDB("good")
	if !ok.OK {
		t.Fatalf("expected success: %s", ok.Message)
	}
	bad := testTMDB("bad")
	if bad.OK {
		t.Fatalf("expected failure")
	}
}
