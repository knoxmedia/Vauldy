package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
)

func TestMusicAndTVGETHandlersDoNotWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, writes := openPhotoGETWriteCountingDB(t)
	_, err := db.Exec(`
        INSERT INTO library(id,name,type,path,enabled) VALUES
            (2,'music','music','E:/music',1),
            (3,'tv','tv','E:/tv',1);
        INSERT INTO music_artist(id,library_id,name,name_norm) VALUES(30,2,'Artist','artist');
        INSERT INTO music_album(id,library_id,title,title_norm,album_artist_id) VALUES(40,2,'Album','album',30);
        INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES
            (50,2,'audio-50','Track','E:/music/Track.mp3','audio','{"music":{"title":"Track","artist":"Artist","album_artist":"Artist","album":"Album","track_number":1}}','active'),
            (60,3,'video-60','Show S01E01','E:/tv/Show/Season 01/Show.S01E01.mkv','video','{"tv":{"series_title":"Show","season":1,"episode":1}}','active'),
            (61,3,'video-61','Show S01E02','E:/tv/Show/Season 01/Show.S01E02.mkv','video','{"tv":{"series_title":"Show","season":1,"episode":2}}','active');
        INSERT INTO music_track(id,album_id,media_id,track_number,title,sort_order) VALUES(51,40,50,NULL,'Track',1);
        INSERT INTO series(id,library_id,title,title_norm) VALUES(70,3,'Show','show');
        INSERT INTO season(id,tv_id,season_num,name) VALUES(71,70,1,'Season 01');
        INSERT INTO episode(id,season_id,episode_num,title) VALUES(72,71,1,'Pilot');
        INSERT INTO episode_media(id,episode_id,media_id,sort_order) VALUES(73,72,60,0);
    `)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}

	tests := []struct {
		name, target, id string
		call             func(*gin.Context)
	}{
		{"albums", "/api/v1/library/2/albums", "2", h.ListLibraryAlbums},
		{"artists", "/api/v1/library/2/artists", "2", h.ListLibraryArtists},
		{"genres", "/api/v1/library/2/genres", "2", h.ListLibraryGenres},
		{"tracks", "/api/v1/library/2/tracks", "2", h.ListLibraryTracks},
		{"album detail", "/api/v1/album/40", "40", h.GetAlbum},
		{"artist detail", "/api/v1/artist/30", "30", h.GetArtist},
		{"artist albums", "/api/v1/artist/30/albums", "30", h.ListArtistAlbums},
		{"genre albums", "/api/v1/library/2/genre/albums?genre=Album", "2", h.ListGenreAlbums},
		{"series", "/api/v1/library/3/series", "3", h.ListLibrarySeries},
		{"series detail", "/api/v1/series/70", "70", h.GetSeries},
		{"episodes", "/api/v1/season/71/episodes", "71", h.ListSeasonEpisodes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writes.Store(0)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tc.target, nil)
			c.Params = gin.Params{{Key: "id", Value: tc.id}}
			setUserCtx(c, 2, "admin", "admin")
			tc.call(c)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if got := writes.Load(); got != 0 {
				t.Fatalf("GET %s performed %d Exec/Begin writes", tc.target, got)
			}
		})
	}
}

func TestFolderScopedUsersCannotReadMusicTVAggregates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := openPhotoGETWriteCountingDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(2,'music','music','E:/music'),(3,'tv','tv','E:/tv');
      INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(9,'folder','x','user',1,'selected');
      INSERT INTO user_library_permission(user_id,library_id) VALUES(9,2),(9,3);
      INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(9,2,'E:/music/allowed'),(9,3,'E:/tv/allowed');
      INSERT INTO music_artist(id,library_id,name,name_norm) VALUES(30,2,'Artist','artist');
      INSERT INTO music_album(id,library_id,title,title_norm,album_artist_id) VALUES(40,2,'Album','album',30);
      INSERT INTO series(id,library_id,title,title_norm) VALUES(70,3,'Show','show');
      INSERT INTO season(id,tv_id,season_num,name) VALUES(71,70,1,'Season 01')`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	tests := []struct {
		name, id, target string
		call             func(*gin.Context)
	}{
		{"albums", "2", "/library/2/albums", h.ListLibraryAlbums}, {"artists", "2", "/library/2/artists", h.ListLibraryArtists},
		{"genres", "2", "/library/2/genres", h.ListLibraryGenres}, {"tracks", "2", "/library/2/tracks", h.ListLibraryTracks},
		{"album", "40", "/album/40", h.GetAlbum}, {"album artwork", "40", "/album/40/artwork", h.ServeAlbumArtwork},
		{"series", "3", "/library/3/series", h.ListLibrarySeries}, {"series detail", "70", "/series/70", h.GetSeries},
		{"episodes", "71", "/season/71/episodes", h.ListSeasonEpisodes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tc.target, nil)
			c.Params = gin.Params{{Key: "id", Value: tc.id}}
			setUserCtx(c, 9, "user", "folder")
			tc.call(c)
			if w.Code != http.StatusForbidden || w.Body.String() != `{"error":"folder-scoped media aggregate unavailable"}` {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMusicGETAndTVPlayTargetHonorCancelledRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := openPhotoGETWriteCountingDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(2,'music','music','E:/music'),(3,'tv','tv','E:/tv');INSERT INTO music_artist(id,library_id,name,name_norm) VALUES(30,2,'Artist','artist');INSERT INTO music_album(id,library_id,title,title_norm,album_artist_id) VALUES(40,2,'Album','album',30);INSERT INTO series(id,library_id,title,title_norm) VALUES(70,3,'Show','show')`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	tests := []struct {
		id, target string
		call       func(*gin.Context)
	}{{"2", "/library/2/albums", h.ListLibraryAlbums}, {"2", "/library/2/artists", h.ListLibraryArtists}, {"2", "/library/2/genres", h.ListLibraryGenres}, {"2", "/library/2/tracks", h.ListLibraryTracks}, {"40", "/album/40", h.GetAlbum}, {"40", "/album/40/play-target", h.GetAlbumPlayTarget}, {"40", "/album/40/artwork", h.ServeAlbumArtwork}, {"30", "/artist/30", h.GetArtist}, {"30", "/artist/30/albums", h.ListArtistAlbums}, {"2", "/library/2/genre/albums?genre=x", h.ListGenreAlbums}, {"70", "/series/70/play-target", h.GetSeriesPlayTarget}}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tc.target, nil).WithContext(ctx)
			c.Params = gin.Params{{Key: "id", Value: tc.id}}
			setUserCtx(c, 2, "admin", "admin")
			tc.call(c)
			if w.Code == http.StatusOK {
				t.Fatalf("status=200 body=%s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), `"items"`) {
				t.Fatalf("partial body=%s", w.Body.String())
			}
		})
	}
}
