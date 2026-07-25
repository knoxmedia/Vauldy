package handler

import (
	"context"
	"testing"

	"knox-media/internal/store"
)

func TestUpdateMediaTitleAndMetaTxRollsBackTitleOnMetaFailure(t *testing.T) {
	db, id := posterHandlerTestDB(t)

	if _, err := db.Exec(`CREATE TRIGGER fail_meta BEFORE UPDATE OF meta_json ON media BEGIN SELECT RAISE(ABORT,'meta failed'); END`); err != nil {
		t.Fatal(err)
	}
	err := updateMediaTitleAndMetaTx(context.Background(), db, id, "changed", `{"scrape":{"overview":"new"}}`)
	if err == nil {
		t.Fatal("expected metadata failure")
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM media WHERE id=?`, id).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title == "changed" {
		t.Fatal("title committed despite metadata failure")
	}
}

func TestUpdateMediaTitleAndMetaTxUpdatesDerivedFields(t *testing.T) {
	db, id := posterHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE media SET file_type='image',created_at_sort='2026-01-01T00:00:00.000000Z' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	meta := `{"photo":{"taken_at":"2026-01-02T00:00:00Z","place_id":"p"}}`
	if err := updateMediaTitleAndMetaTx(context.Background(), db, id, "changed", meta); err != nil {
		t.Fatal(err)
	}
	var title, taken, place string
	if err := db.QueryRow(`SELECT title,photo_taken_at,photo_place_id FROM media WHERE id=?`, id).Scan(&title, &taken, &place); err != nil {
		t.Fatal(err)
	}
	if title != "changed" || taken != "2026-01-02T00:00:00.000000Z" || place != "p" {
		t.Fatalf("%q %q %q", title, taken, place)
	}
}

var _ = store.PhotoPlaceID

