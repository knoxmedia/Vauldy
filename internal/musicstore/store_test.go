package musicstore

import (
	"path/filepath"
	"testing"

	"knox-media/internal/musicparse"
	"knox-media/internal/store"
)

func TestDecodeMusicMetaFromStoredJSON(t *testing.T) {
	raw := `{"music":{"title":"无言的结局","artist":"李茂山","album":"经典老歌","album_artist":"李茂山","track_number":2},"format":{"tags":{"album":"ignored"}}}`
	meta := DecodeMusicMeta(raw, `D:\Music\test.mp3`)
	if meta.Title != "无言的结局" {
		t.Fatalf("title=%q", meta.Title)
	}
	if meta.Album != "经典老歌" {
		t.Fatalf("album=%q", meta.Album)
	}
	if meta.TrackNumber != 2 {
		t.Fatalf("track=%d", meta.TrackNumber)
	}
}

func TestBackfillLinksAudioToAlbum(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "music.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'music', 'music', 'D:\Music')`)
	_, _ = db.Exec(`
		INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status, meta_json)
		VALUES (10, 1, 'f1', 'song1', 'D:\Music\song1.mp3', 'audio', 'active', ?)
	`, `{"music":{"title":"song1","artist":"artistA","album":"[Unknown Album]","album_artist":"Various Artists"}}`)
	_, _ = db.Exec(`
		INSERT INTO music_album (id, library_id, title, title_norm, is_unknown)
		VALUES (5, 1, '[Unknown Album]', ?, 1)
	`, musicparse.NormKey("[Unknown Album]"))

	linked, err := BackfillAlbumTracks(db, 5)
	if err != nil {
		t.Fatal(err)
	}
	if linked != 1 {
		t.Fatalf("linked=%d", linked)
	}
	var count int
	if db.QueryRow(`SELECT COUNT(1) FROM music_track WHERE album_id = 5 AND media_id = 10`).Scan(&count) != nil || count != 1 {
		t.Fatalf("track not linked, count=%d", count)
	}
}

func TestMergeUnknownAlbums(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "music.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'music', 'music', 'D:\Music')`)
	_, _ = db.Exec(`INSERT INTO music_album (id, library_id, title, title_norm, is_unknown) VALUES (1, 1, '[Unknown Album]', 'x', 1)`)
	_, _ = db.Exec(`INSERT INTO music_album (id, library_id, title, title_norm, is_unknown) VALUES (2, 1, '[Unknown Album]', 'x', 1)`)
	_, _ = db.Exec(`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status) VALUES (10, 1, 'f1', 'a', 'a.mp3', 'audio', 'active')`)
	_, _ = db.Exec(`INSERT INTO music_track (album_id, media_id, title, sort_order) VALUES (2, 10, 'a', 1)`)

	if err := MergeUnknownAlbums(db, 1); err != nil {
		t.Fatal(err)
	}
	var albumID int64
	if db.QueryRow(`SELECT album_id FROM music_track WHERE media_id = 10`).Scan(&albumID) != nil || albumID != 1 {
		t.Fatalf("album_id=%d want 1", albumID)
	}
	var remain int
	_ = db.QueryRow(`SELECT COUNT(1) FROM music_album WHERE library_id = 1 AND is_unknown = 1`).Scan(&remain)
	if remain != 1 {
		t.Fatalf("remain=%d", remain)
	}
}
