package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"knox-media/internal/app"
	"knox-media/internal/scraper"
)

func TestMergeScrapeResultTxPreservesConcurrentPosterAndNonScrapeFields(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	stale := `{"before":"stale","scrape":{"overview":"old"}}`
	if _, err := db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, stale, mid); err != nil {
		t.Fatal(err)
	}
	current := `{"before":"fresh","concurrent":{"token":"keep"},"scrape":{"poster":"/uploads/posters/1.jpg","extra":{"poster":"/uploads/posters/1.jpg","local_poster_source":"embedded","worker":"keep"}}}`
	if _, err := db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, current, mid); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	provider := &scraper.ScrapeResult{Title: "provider title", Overview: "provider overview", Extra: map[string]any{"provider": "yes"}}
	merged, committed, err := h.mergeScrapeResultTx(context.Background(), mid, provider, provider.Title)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Poster != "/uploads/posters/1.jpg" || committed.Extra["local_poster_source"] != "embedded" || committed.Extra["worker"] != "keep" {
		t.Fatalf("committed scrape lost concurrent poster fields: %#v", committed)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(merged), &root); err != nil {
		t.Fatal(err)
	}
	if root["before"] != "fresh" {
		t.Fatalf("used stale root: %#v", root)
	}
	concurrent, _ := root["concurrent"].(map[string]any)
	if concurrent["token"] != "keep" {
		t.Fatalf("lost concurrent key: %#v", root)
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM media WHERE id=?`, mid).Scan(&title); err != nil || title != "provider title" {
		t.Fatalf("title=(%q,%v)", title, err)
	}
}

func TestMergeScrapeResultTxProviderPosterOverridesCurrentPoster(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, `{"scrape":{"poster":"/local.jpg","extra":{"poster":"/local.jpg"}}}`, mid); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	provider := &scraper.ScrapeResult{Title: "remote", Poster: "https://remote/poster.jpg", Extra: map[string]any{"poster": "https://remote/poster.jpg"}}
	_, committed, err := h.mergeScrapeResultTx(context.Background(), mid, provider, provider.Title)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Poster != "https://remote/poster.jpg" {
		t.Fatalf("poster=%q", committed.Poster)
	}
}

func TestMergeScrapeResultTxDoesNotMutateSharedResult(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, `{"scrape":{"poster":"/local.jpg"}}`, mid); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	provider := &scraper.ScrapeResult{Title: "provider", Extra: map[string]any{"provider": "yes"}}
	if _, _, err := h.mergeScrapeResultTx(context.Background(), mid, provider, provider.Title); err != nil {
		t.Fatal(err)
	}
	if provider.Poster != "" {
		t.Fatalf("shared result mutated: %#v", provider)
	}
	if _, ok := provider.Extra["poster"]; ok {
		t.Fatalf("shared extra mutated: %#v", provider.Extra)
	}
}

func TestMergeScrapeResultTxReturnsDatabaseErrors(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	h := &Handler{App: &app.App{DB: db}}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err := h.mergeScrapeResultTx(context.Background(), mid, &scraper.ScrapeResult{Title: "x"}, "x")
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error=%v, want database error", err)
	}
}
