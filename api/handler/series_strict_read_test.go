package handler

import (
	"github.com/gin-gonic/gin"
	"knox-media/internal/app"
	"knox-media/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestTVGETReturns500OnRowScanErrorsWithoutPartialResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "strict.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(801,'tv','tv','E:/tv'); INSERT INTO series(id,library_id,title,title_norm,year) VALUES(8100,801,'Bad','bad','not-a-number')`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/library/801/series", nil)
	c.Params = gin.Params{{Key: "id", Value: "801"}}
	h.ListLibrarySeries(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGetSeriesNestedSeasonScanErrorReturns500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "nested.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(901,'tv','tv','E:/tv');INSERT INTO series(id,library_id,title,title_norm) VALUES(9100,901,'Show','show');INSERT INTO season(id,tv_id,season_num) VALUES(9101,9100,'bad')`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/series/9100", nil)
	c.Params = gin.Params{{Key: "id", Value: "9100"}}
	h.GetSeries(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestListSeasonEpisodesNestedVersionScanErrorReturns500WithoutPartial(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "versions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1001,'tv','tv','E:/tv');INSERT INTO series(id,library_id,title,title_norm) VALUES(10100,1001,'Show','show');INSERT INTO season(id,tv_id,season_num) VALUES(10101,10100,1);INSERT INTO episode(id,season_id,episode_num) VALUES(10102,10101,1);INSERT INTO media(id,library_id,file_id,title,file_path,file_type,duration,status) VALUES(10200,1001,'v','Bad','E:/tv/bad.mkv','video','bad','active');INSERT INTO episode_media(episode_id,media_id) VALUES(10102,10200)`)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/season/10101/episodes", nil)
	c.Params = gin.Params{{Key: "id", Value: "10101"}}
	h.ListSeasonEpisodes(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("partial body=%s", w.Body.String())
	}
}
