package handler

import (
	"testing"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func TestQueryLibraryTracksWithNullTrackNumber(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (7, 'music', 'music', 'F:\Music')`)
	_, _ = db.Exec(`INSERT INTO music_artist (id, library_id, name, name_norm) VALUES (1, 7, 'Artist', 'artist')`)
	_, _ = db.Exec(`INSERT INTO music_album (id, library_id, title, title_norm, album_artist_id) VALUES (1, 7, 'Album', 'album', 1)`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (540, 7, 'f1', 'Track', 'a.mp3', 'audio', 'active')`)
	_, _ = db.Exec(`INSERT INTO music_track (album_id, media_id, track_number, title, sort_order) VALUES (1, 540, NULL, 'Track', 1)`)

	h := &Handler{App: &app.App{DB: db}}
	items, err := h.queryLibraryTracks(7, 1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("tracks=%d want 1", len(items))
	}
}
