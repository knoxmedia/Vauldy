package handler

import (
	"context"
	"path/filepath"
	"testing"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func TestSyncMediaRelationshipAfterMetadataChange(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(601,'music','music','E:/music'),(602,'tv','tv','E:/tv');
      INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES
      (6001,601,'a','Song','E:/music/Song.mp3','audio','{"music":{"title":"Song","album":"Changed","artist":"Artist","album_artist":"Artist"}}','active'),
      (6002,602,'v','Episode','E:/opaque/video.mkv','video','{"tv":{"series_title":"Changed Show","season":4,"episode":5}}','active')`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	if err := h.syncMediaRelationship(context.Background(), 6001); err != nil {
		t.Fatal(err)
	}
	if err := h.syncMediaRelationship(context.Background(), 6002); err != nil {
		t.Fatal(err)
	}
	var album string
	if err := db.QueryRow(`SELECT a.title FROM music_track mt JOIN music_album a ON a.id=mt.album_id WHERE mt.media_id=6001`).Scan(&album); err != nil || album != "Changed" {
		t.Fatalf("album=%q err=%v", album, err)
	}
	var series string
	if err := db.QueryRow(`SELECT s.title FROM episode_media em JOIN episode ep ON ep.id=em.episode_id JOIN season se ON se.id=ep.season_id JOIN series s ON s.id=se.tv_id WHERE em.media_id=6002`).Scan(&series); err != nil || series != "Changed Show" {
		t.Fatalf("series=%q err=%v", series, err)
	}
}

func TestSyncMediaRelationshipRemovesInvalidTVAndPrunesOldHierarchy(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cleanup.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(701,'tv','tv','E:/tv'); INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(7001,701,'v','Opaque','E:/tv/video.mkv','video','{}','active'); INSERT INTO series(id,library_id,title,title_norm) VALUES(7100,701,'Old','old'); INSERT INTO season(id,tv_id,season_num) VALUES(7101,7100,1); INSERT INTO episode(id,season_id,episode_num) VALUES(7102,7101,1); INSERT INTO episode_media(episode_id,media_id) VALUES(7102,7001)`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	if err := h.syncMediaRelationship(context.Background(), 7001); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM episode_media WHERE media_id=7001`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("links=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM series WHERE id=7100`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("series=%d err=%v", n, err)
	}
}

func TestSyncMediaRelationshipLinkFailureKeepsOldTVRelationship(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "atomic-sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(801,'tv','tv','E:/tv'); INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(8001,801,'v','New','E:/opaque/new.mkv','video','{"tv":{"series_title":"New","season":2,"episode":3}}','active'); INSERT INTO series(id,library_id,title,title_norm) VALUES(8100,801,'Old','old'); INSERT INTO season(id,tv_id,season_num) VALUES(8101,8100,1); INSERT INTO episode(id,season_id,episode_num) VALUES(8102,8101,1); INSERT INTO episode_media(episode_id,media_id) VALUES(8102,8001); CREATE TRIGGER fail_new_episode BEFORE INSERT ON episode WHEN NEW.season_id != 8101 BEGIN SELECT RAISE(ABORT,'forced link failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	if err := h.syncMediaRelationship(context.Background(), 8001); err == nil {
		t.Fatal("expected link failure")
	}
	var episodeID int64
	if err := db.QueryRow(`SELECT episode_id FROM episode_media WHERE media_id=8001`).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	if episodeID != 8102 {
		t.Fatalf("episode=%d want old 8102", episodeID)
	}
}

func TestSyncMediaRelationshipMovesTVAndPrunesOldInOneCommit(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "move-sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(901,'tv','tv','E:/tv'); INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(9001,901,'v','New','E:/opaque/new.mkv','video','{"tv":{"series_title":"New","season":2,"episode":3}}','active'); INSERT INTO series(id,library_id,title,title_norm) VALUES(9100,901,'Old','old'); INSERT INTO season(id,tv_id,season_num) VALUES(9101,9100,1); INSERT INTO episode(id,season_id,episode_num) VALUES(9102,9101,1); INSERT INTO episode_media(episode_id,media_id) VALUES(9102,9001)`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	if err := h.syncMediaRelationship(context.Background(), 9001); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := db.QueryRow(`SELECT s.title FROM episode_media em JOIN episode e ON e.id=em.episode_id JOIN season se ON se.id=e.season_id JOIN series s ON s.id=se.tv_id WHERE em.media_id=9001`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "New" {
		t.Fatalf("title=%q", title)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM series WHERE id=9100`).Scan(&n)
	if n != 0 {
		t.Fatalf("old series remains")
	}
}

func TestSyncMediaRelationshipPrunesOnlyOldHierarchy(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scoped.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1001,'tv','tv','E:/tv'),(1002,'other','tv','E:/other');INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(10001,1001,'v','bad','E:/tv/bad.mkv','video','{}','active');INSERT INTO series(id,library_id,title,title_norm) VALUES(10100,1001,'Old','old'),(10200,1002,'Unrelated','unrelated');INSERT INTO season(id,tv_id,season_num) VALUES(10101,10100,1),(10201,10200,1);INSERT INTO episode(id,season_id,episode_num) VALUES(10102,10101,1);INSERT INTO episode_media(episode_id,media_id) VALUES(10102,10001)`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	if err := h.syncMediaRelationship(context.Background(), 10001); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM series WHERE id=10200`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("unrelated series=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM season WHERE id=10201`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("unrelated season=%d err=%v", n, err)
	}
}
