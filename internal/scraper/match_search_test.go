package scraper

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSearchOMDbCandidateDecodesIdentityAndMediaType(t *testing.T) {
	useMockOnlineHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payloads := map[string]map[string]string{
			"Movie Title": {
				"Response": "True",
				"Title":    "Movie Title",
				"imdbID":   "tt0000001",
				"Type":     "movie",
			},
			"Series Title": {
				"Response": "True",
				"Title":    "Series Title",
				"imdbID":   "tt0000002",
				"Type":     "series",
			},
		}
		_ = json.NewEncoder(w).Encode(payloads[r.URL.Query().Get("t")])
	}))

	tests := []struct {
		name          string
		query         string
		wantIMDbID    string
		wantMediaType string
	}{
		{name: "movie", query: "Movie Title", wantIMDbID: "tt0000001", wantMediaType: "movie"},
		{name: "series", query: "Series Title", wantIMDbID: "tt0000002", wantMediaType: "tv"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := searchOMDbCandidate(tc.query, 0, "test-key")
			if err != nil {
				t.Fatalf("searchOMDbCandidate() error = %v", err)
			}
			if got.ExternalID != tc.wantIMDbID {
				t.Errorf("ExternalID = %q, want %q", got.ExternalID, tc.wantIMDbID)
			}
			if got.MediaType != tc.wantMediaType {
				t.Errorf("MediaType = %q, want %q", got.MediaType, tc.wantMediaType)
			}
		})
	}
}

func TestFetchOMDbByIDDecodesIMDbID(t *testing.T) {
	useMockOnlineHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"Response": "True",
			"Title":    "Detail Title",
			"imdbID":   "tt0000003",
		})
	}))

	got, err := fetchOMDbByID("tt0000003", "test-key")
	if err != nil {
		t.Fatalf("fetchOMDbByID() error = %v", err)
	}
	if got.Extra["imdb_id"] != "tt0000003" {
		t.Errorf("Extra[imdb_id] = %q, want %q", got.Extra["imdb_id"], "tt0000003")
	}
}
