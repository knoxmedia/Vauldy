package handler

import (
	"context"
	"testing"
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
