package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func setupPublicationBrowseTestDB(t *testing.T) *Handler {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "publication-browse.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO library(id,name,type,path,enabled) VALUES(1,'music','music','E:/music',1),(2,'tv','tv','E:/tv',1);
		INSERT INTO user(id,username,password,role,can_play,library_scope) VALUES(1,'viewer','x','user',1,'all');
		INSERT INTO music_artist(id,library_id,name,name_norm) VALUES(10,1,'Artist','artist');
		INSERT INTO music_album(id,library_id,title,title_norm,album_artist_id,genre) VALUES(20,1,'Album','album',10,'Rock');
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,duration) VALUES
		(101,1,'music-processing','Processing','E:/music/1.mp3','audio','active','processing',101),
		(102,1,'music-failed','Failed','E:/music/2.mp3','audio','active','failed',102),
		(103,1,'music-cancelled','Cancelled','E:/music/3.mp3','audio','active','cancelled',103),
		(104,1,'music-degraded','Degraded','E:/music/4.mp3','audio','active','degraded',104);
		INSERT INTO music_track(id,album_id,media_id,track_number,title,sort_order) VALUES
		(201,20,101,1,'Processing',1),(202,20,102,2,'Failed',2),(203,20,103,3,'Cancelled',3),(204,20,104,4,'Degraded',4);
		INSERT INTO series(id,library_id,title,title_norm) VALUES(30,2,'Show','show');
		INSERT INTO season(id,tv_id,season_num,name) VALUES(31,30,1,'Season 1');
		INSERT INTO episode(id,season_id,episode_num,title) VALUES(32,31,1,'Hidden Only'),(33,31,2,'Mixed');
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,meta_json) VALUES
		(105,2,'episode-processing-only','Hidden Only','E:/tv/hidden.mkv','video','active','processing','{"scrape":{"poster":"processing-only.jpg","extra":{"series_poster":"processing-series.jpg"}}}'),
		(106,2,'episode-processing-mixed','Hidden Version','E:/tv/mixed-hidden.mkv','video','active','processing','{"scrape":{"poster":"processing-mixed.jpg","extra":{"series_poster":"processing-series-2.jpg"}}}'),
		(107,2,'episode-degraded','Visible Version','E:/tv/visible.mkv','video','active','degraded','{"scrape":{"poster":"degraded-poster.jpg","extra":{"series_poster":"degraded-series.jpg"}}}');
		INSERT INTO episode_media(id,episode_id,media_id,sort_order) VALUES(301,32,105,0),(302,33,106,0),(303,33,107,1)`); err != nil {
		t.Fatal(err)
	}
	return &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
}

func publicationBrowseContext(target, id string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	setUserCtx(c, 1, "user", "viewer")
	return c, w
}

func decodeBrowseJSON(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
}

func TestMusicBrowseFiltersPublicationStateAcrossTracksAndAlbumDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupPublicationBrowseTestDB(t)

	c, w := publicationBrowseContext("/api/v1/library/1/tracks", "1")
	h.ListLibraryTracks(c)
	var list struct {
		Items []struct {
			MediaID int64 `json:"media_id"`
		} `json:"items"`
	}
	decodeBrowseJSON(t, w, &list)
	if got := fmt.Sprint(list.Items); got != "[{104}]" {
		t.Fatalf("library tracks=%s body=%s", got, w.Body.String())
	}

	c, w = publicationBrowseContext("/api/v1/album/20", "20")
	h.GetAlbum(c)
	var album struct {
		TrackCount    int64 `json:"track_count"`
		TotalDuration int64 `json:"total_duration"`
		Tracks        []struct {
			MediaID int64 `json:"media_id"`
		} `json:"tracks"`
	}
	decodeBrowseJSON(t, w, &album)
	if album.TrackCount != 1 || album.TotalDuration != 104 || fmt.Sprint(album.Tracks) != "[{104}]" {
		t.Fatalf("album=%+v body=%s", album, w.Body.String())
	}

	c, w = publicationBrowseContext("/api/v1/album/20/play-target", "20")
	h.GetAlbumPlayTarget(c)
	var target struct {
		MediaID int64 `json:"media_id"`
	}
	decodeBrowseJSON(t, w, &target)
	if target.MediaID != album.Tracks[0].MediaID {
		t.Fatalf("play target=%d album track=%d body=%s", target.MediaID, album.Tracks[0].MediaID, w.Body.String())
	}
}

func TestMusicBrowseAggregatesCountOnlyVisibleTracks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupPublicationBrowseTestDB(t)
	cases := []struct {
		name, target, id string
		call             func(*gin.Context)
	}{
		{"albums", "/api/v1/library/1/albums", "1", h.ListLibraryAlbums},
		{"artists", "/api/v1/library/1/artists", "1", h.ListLibraryArtists},
		{"genres", "/api/v1/library/1/genres", "1", h.ListLibraryGenres},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := publicationBrowseContext(tc.target, tc.id)
			tc.call(c)
			var payload struct {
				Items []struct {
					TrackCount    int64 `json:"track_count"`
					AlbumCount    int64 `json:"album_count"`
					TotalDuration int64 `json:"total_duration"`
				} `json:"items"`
			}
			decodeBrowseJSON(t, w, &payload)
			if len(payload.Items) != 1 || payload.Items[0].TrackCount != 1 {
				t.Fatalf("%s aggregates=%+v body=%s", tc.name, payload.Items, w.Body.String())
			}
			if tc.name == "albums" && payload.Items[0].TotalDuration != 104 {
				t.Fatalf("album duration=%d want 104", payload.Items[0].TotalDuration)
			}
		})
	}
}

func TestSeriesBrowseHidesEpisodesWithoutVisibleVersions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupPublicationBrowseTestDB(t)
	c, w := publicationBrowseContext("/api/v1/season/31/episodes", "31")
	h.ListSeasonEpisodes(c)
	var payload struct {
		Items []struct {
			ID       int64 `json:"id"`
			Versions []struct {
				MediaID   int64  `json:"media_id"`
				FileID    string `json:"file_id"`
				FilePath  string `json:"file_path"`
				PosterURL string `json:"poster_url"`
			} `json:"versions"`
		} `json:"items"`
	}
	decodeBrowseJSON(t, w, &payload)
	if len(payload.Items) != 1 || payload.Items[0].ID != 33 || len(payload.Items[0].Versions) != 1 || payload.Items[0].Versions[0].MediaID != 107 || payload.Items[0].Versions[0].FileID != "episode-degraded" || payload.Items[0].Versions[0].FilePath != "E:/tv/visible.mkv" {
		t.Fatalf("episodes=%+v body=%s", payload.Items, w.Body.String())
	}
}

func TestSeriesBrowseCountsAndPosterIgnoreUnpublishedMedia(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupPublicationBrowseTestDB(t)
	c, w := publicationBrowseContext("/api/v1/library/2/series", "2")
	h.ListLibrarySeries(c)
	var list struct {
		Items []struct {
			EpisodeCount int64  `json:"episode_count"`
			SeasonCount  int64  `json:"season_count"`
			PosterURL    string `json:"poster_url"`
		} `json:"items"`
	}
	decodeBrowseJSON(t, w, &list)
	if len(list.Items) != 1 || list.Items[0].EpisodeCount != 1 || list.Items[0].SeasonCount != 1 || list.Items[0].PosterURL != "degraded-series.jpg" {
		t.Fatalf("series=%+v body=%s", list.Items, w.Body.String())
	}
	c, w = publicationBrowseContext("/api/v1/series/30", "30")
	h.GetSeries(c)
	var detail struct {
		PosterURL string `json:"poster_url"`
		Seasons   []struct {
			EpisodeCount int64 `json:"episode_count"`
		} `json:"seasons"`
	}
	decodeBrowseJSON(t, w, &detail)
	if detail.PosterURL != "degraded-series.jpg" || len(detail.Seasons) != 1 || detail.Seasons[0].EpisodeCount != 1 {
		t.Fatalf("detail=%+v body=%s", detail, w.Body.String())
	}
}

func TestMusicBrowseOmitsAggregatesWithNoVisibleTracks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupPublicationBrowseTestDB(t)
	if _, err := h.App.DB.Exec(`INSERT INTO music_artist(id,library_id,name,name_norm) VALUES(11,1,'Hidden Artist','hidden artist');
		INSERT INTO music_album(id,library_id,title,title_norm,album_artist_id,genre) VALUES(21,1,'Hidden Album','hidden album',11,'HiddenGenre');
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state) VALUES(108,1,'hidden-only','Hidden','E:/music/hidden.mp3','audio','active','processing');
		INSERT INTO music_track(id,album_id,media_id,title,sort_order) VALUES(205,21,108,'Hidden',1)`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, target string
		call         func(*gin.Context)
	}{
		{"albums", "/api/v1/library/1/albums", h.ListLibraryAlbums},
		{"artists", "/api/v1/library/1/artists", h.ListLibraryArtists},
		{"genres", "/api/v1/library/1/genres", h.ListLibraryGenres},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := publicationBrowseContext(tc.target, "1")
			tc.call(c)
			var payload struct {
				Items []map[string]any `json:"items"`
			}
			decodeBrowseJSON(t, w, &payload)
			if len(payload.Items) != 1 {
				t.Fatalf("%s retained hidden-only aggregate: %s", tc.name, w.Body.String())
			}
		})
	}
}

func TestLibraryPreviewCandidatesOnlyIncludePublishedAndDegraded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupPublicationBrowseTestDB(t)
	if _, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,meta_json) VALUES
		(109,2,'preview-published','Published','E:/tv/published.mkv','video','active','published','{"scrape":{"poster":"published.jpg"}}')`); err != nil {
		t.Fatal(err)
	}
	items, err := h.queryVideoPreviewCandidates(2, 20)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, item := range items {
		ids = append(ids, item.mediaID)
	}
	if got := fmt.Sprint(ids); got != "[109 107]" {
		t.Fatalf("preview candidates=%s want published and degraded only", got)
	}
}
