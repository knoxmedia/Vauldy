package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"knox-media/internal/app"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMusicTVListMaintenanceStatusIsPureRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, writes := openPhotoGETWriteCountingDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(21,'music','music','E:/music'),(22,'tv','tv','E:/tv');INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(2101,21,'a','A','A.mp3','audio','active'),(2102,21,'b','B','B.mp3','audio','active'),(2201,22,'v1','V1','Show/V1.mkv','video','active'),(2202,22,'v2','V2','Show/V2.mkv','video','active');INSERT INTO music_album(id,library_id,title,title_norm) VALUES(2110,21,'Album','album');INSERT INTO music_track(album_id,media_id,title,sort_order) VALUES(2110,2101,'A',1);INSERT INTO series(id,library_id,title,title_norm) VALUES(2210,22,'Show','show');INSERT INTO season(id,tv_id,season_num) VALUES(2211,2210,1);INSERT INTO episode(id,season_id,episode_num) VALUES(2212,2211,1);INSERT INTO episode_media(episode_id,media_id) VALUES(2212,2201)`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	tests := []struct {
		name, id, target string
		call             func(*gin.Context)
	}{{"albums", "21", "/library/21/albums", h.ListLibraryAlbums}, {"artists", "21", "/library/21/artists", h.ListLibraryArtists}, {"genres", "21", "/library/21/genres", h.ListLibraryGenres}, {"tracks", "21", "/library/21/tracks", h.ListLibraryTracks}, {"series", "22", "/library/22/series", h.ListLibrarySeries}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writes.Store(0)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, tc.target, nil)
			c.Params = gin.Params{{Key: "id", Value: tc.id}}
			setUserCtx(c, 2, "admin", "admin")
			tc.call(c)
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var body struct {
				Items    []any `json:"items"`
				Required bool  `json:"maintenance_required"`
				Count    int   `json:"unlinked_count"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !body.Required || body.Count != 1 {
				t.Fatalf("body=%s", w.Body.String())
			}
			if writes.Load() != 0 {
				t.Fatalf("writes=%d", writes.Load())
			}
		})
	}
}
func TestMaintenanceStatusBecomesFalseWhenAllLinked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := openPhotoGETWriteCountingDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(31,'music','music','E:/music');INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(3101,31,'a','A','A.mp3','audio','active');INSERT INTO music_album(id,library_id,title,title_norm) VALUES(3110,31,'Album','album');INSERT INTO music_track(album_id,media_id,title,sort_order) VALUES(3110,3101,'A',1)`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/library/31/albums", nil)
	c.Params = gin.Params{{Key: "id", Value: "31"}}
	setUserCtx(c, 2, "admin", "admin")
	h.ListLibraryAlbums(c)
	if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["maintenance_required"] != false || body["unlinked_count"] != float64(0) {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestMaintenanceCountErrorReturns500WithoutItems(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := openPhotoGETWriteCountingDB(t)
	_, err := db.Exec(`INSERT INTO library(id,name,type,path) VALUES(41,'music','music','E:/music');DROP TABLE music_track`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/library/41/albums", nil)
	c.Params = gin.Params{{Key: "id", Value: "41"}}
	setUserCtx(c, 2, "admin", "admin")
	h.ListLibraryAlbums(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("partial body=%s", w.Body.String())
	}
}
