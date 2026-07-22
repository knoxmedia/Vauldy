package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/scraper"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func setupAdditionalPublicationDB(t *testing.T) *Handler {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "publication-additional.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`
        INSERT INTO library(id,name,type,path,enabled) VALUES
          (1,'docs','document','E:/docs',1),(2,'photos','photo','E:/photos',1),(3,'media','movie','E:/media',1),(4,'music','music','E:/music',1),(5,'tv','tv','E:/tv',1);
        INSERT INTO user(id,username,password,role,can_play,can_download,library_scope) VALUES(1,'admin','x','admin',1,1,'all');
        INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state,meta_json) VALUES
          (101,1,'doc-hidden','Hidden Doc','E:/docs/folder/hidden.pdf','document','active','processing','{"document":{"author":"Hidden Author","format":"pdf","year":2020}}'),
          (102,1,'doc-visible','Visible Doc','E:/docs/folder/visible.pdf','document','active','degraded','{"document":{"author":"Visible Author","format":"pdf","year":2021}}'),
          (201,2,'photo-hidden','Hidden Photo','E:/photos/hidden.jpg','image','active','failed','{"photo":{"tags":["hidden-tag"],"place_id":"hidden-place","location_name":"Hidden Place"}}'),
          (202,2,'photo-visible','Visible Photo','E:/photos/visible.jpg','image','active','published','{"photo":{"tags":["visible-tag"],"place_id":"visible-place","location_name":"Visible Place"}}'),
          (301,3,'fav-hidden','Hidden Favorite','E:/media/hidden.mp4','video','active','cancelled','{}'),
          (302,3,'fav-visible','Visible Favorite','E:/media/visible.mp4','video','active','published','{}');
        INSERT INTO library_node(id,library_id,parent_path,node_path,node_name,node_type,media_id) VALUES
          (1,1,'','E:/docs/folder','folder','dir',NULL),(2,1,'E:/docs/folder','E:/docs/folder/hidden.pdf','hidden.pdf','file',101),(3,1,'E:/docs/folder','E:/docs/folder/visible.pdf','visible.pdf','file',102);
        INSERT INTO document_tag(media_id,tag) VALUES(101,'hidden-tag'),(102,'visible-tag');
        INSERT INTO read_progress(user_id,media_id,position,percent) VALUES(1,101,'1',10),(1,102,'2',20);
        INSERT INTO photo_person(id,library_id,label,cover_face_id,face_count,media_count) VALUES(401,2,'Mixed Person',501,99,99),(402,2,'Hidden Person',503,99,99);
        INSERT INTO photo_face(id,media_id,library_id,person_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(501,201,2,401,.1,.1,.2,.2),(502,202,2,401,.1,.1,.2,.2),(503,201,2,402,.1,.1,.2,.2);
        INSERT INTO favorite(user_id,media_id) VALUES(1,301),(1,302);
        INSERT INTO playlist(id,user_id,name) VALUES(601,1,'Mixed'),(602,1,'Hidden Only');
        INSERT INTO favorite_folder(id,user_id,name) VALUES(611,1,'Mixed Favorites'),(612,1,'Hidden Favorites');
        INSERT INTO playlist_item(id,playlist_id,media_id,sort_order) VALUES(701,601,301,0),(702,601,302,1),(703,602,301,0);
        INSERT INTO favorite_folder_item(id,folder_id,media_id,sort_order) VALUES(711,611,301,0),(712,611,302,1),(713,612,301,0);
    `); err != nil {
		t.Fatal(err)
	}
	return &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
}

func additionalCtx(method, target, id string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	setUserCtx(c, 1, "admin", "admin")
	return c, w
}

func decodeItems(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	return body.Items
}

func TestDocumentBrowseFiltersPublicationForAdminCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	c, w := additionalCtx(http.MethodGet, "/api/v1/library/1/documents", "1")
	h.ListDocuments(c)
	if items := decodeItems(t, w); w.Code != 200 || len(items) != 1 || int64(items[0]["id"].(float64)) != 102 {
		t.Fatalf("documents status=%d body=%s", w.Code, w.Body.String())
	}
	for _, kind := range []string{"author", "format", "tag", "year"} {
		c, w = additionalCtx(http.MethodGet, "/api/v1/library/1/document/facets?kind="+kind, "1")
		c.Request.URL.RawQuery = "kind=" + kind
		h.ListDocumentFacets(c)
		items := decodeItems(t, w)
		if len(items) != 1 {
			t.Fatalf("facet %s body=%s", kind, w.Body.String())
		}
		if kind == "author" && items[0]["name"] != "Visible Author" {
			t.Fatalf("author facet=%s", w.Body.String())
		}
	}
	c, w = additionalCtx(http.MethodGet, "/api/v1/library/1/documents/recent", "1")
	h.ListRecentDocuments(c)
	if items := decodeItems(t, w); len(items) != 1 || int64(items[0]["id"].(float64)) != 102 {
		t.Fatalf("recent=%s", w.Body.String())
	}
}

func TestDocumentNodesAndDownloadsFilterHiddenMedia(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	c, w := additionalCtx(http.MethodGet, "/api/v1/library/1/document/nodes?parent=E:/docs/folder", "1")
	h.ListDocumentNodes(c)
	items := decodeItems(t, w)
	if len(items) != 1 {
		t.Fatalf("nodes=%s", w.Body.String())
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item["name"].(string)] = true
	}
	if !seen["visible.pdf"] || seen["hidden.pdf"] {
		t.Fatalf("nodes=%s", w.Body.String())
	}
	paths, _, err := h.resolveDocumentDownloadPaths([]int64{101, 102}, "")
	if err != nil || fmt.Sprint(paths) != "[E:/docs/folder/visible.pdf]" {
		t.Fatalf("selected paths=%v err=%v", paths, err)
	}
	paths, _, err = h.resolveDirDownloadPaths("E:/docs/folder")
	if err != nil || fmt.Sprint(paths) != "[E:/docs/folder/visible.pdf]" {
		t.Fatalf("folder paths=%v err=%v", paths, err)
	}
	paths, _, err = h.resolveDirDownloadPaths("E:/docs")
	if err == nil {
		t.Fatalf("non-node directory unexpectedly resolved: %v", paths)
	}
}

func TestPhotoAggregatesUseOnlyVisibleMediaEvenForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	c, w := additionalCtx(http.MethodGet, "/api/v1/library/2/photo/categories", "2")
	h.ListPhotoCategories(c)
	items := decodeItems(t, w)
	if len(items) != 2 || w.Body.String() == "" {
		t.Fatalf("categories=%s", w.Body.String())
	}
	if fmt.Sprint(items) == "" || containsJSON(w.Body.String(), "hidden-tag") {
		t.Fatalf("hidden category leaked: %s", w.Body.String())
	}
	c, w = additionalCtx(http.MethodGet, "/api/v1/library/2/photo/places", "2")
	h.ListPhotoPlaces(c)
	items = decodeItems(t, w)
	if len(items) != 1 || items[0]["id"] != "visible-place" || int64(items[0]["cover_id"].(float64)) != 202 {
		t.Fatalf("places=%s", w.Body.String())
	}
	c, w = additionalCtx(http.MethodGet, "/api/v1/library/2/photo/persons", "2")
	h.ListPhotoPersons(c)
	items = decodeItems(t, w)
	if len(items) != 1 || items[0]["name"] != "Mixed Person" || int64(items[0]["count"].(float64)) != 1 || int64(items[0]["cover_face_id"].(float64)) != 502 {
		t.Fatalf("persons=%s", w.Body.String())
	}
}

func containsJSON(s, part string) bool {
	return len(part) > 0 && len(s) >= len(part) && func() bool {
		for i := 0; i+len(part) <= len(s); i++ {
			if s[i:i+len(part)] == part {
				return true
			}
		}
		return false
	}()
}

func TestFavoritesAndPlaylistsFilterHiddenMedia(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	c, w := additionalCtx(http.MethodGet, "/api/v1/favorites", "")
	h.ListFavorites(c)
	if items := decodeItems(t, w); len(items) != 1 || int64(items[0]["id"].(float64)) != 302 {
		t.Fatalf("favorites=%s", w.Body.String())
	}
	c, w = additionalCtx(http.MethodGet, "/api/v1/playlists", "")
	h.ListPlaylists(c)
	items := decodeItems(t, w)
	if len(items) != 2 {
		t.Fatalf("playlists=%s", w.Body.String())
	}
	byID := map[int64]map[string]any{}
	for _, item := range items {
		byID[int64(item["id"].(float64))] = item
	}
	if int64(byID[601]["item_count"].(float64)) != 1 || int64(byID[601]["first_media_id"].(float64)) != 302 {
		t.Fatalf("mixed=%v body=%s", byID[601], w.Body.String())
	}
	if int64(byID[602]["item_count"].(float64)) != 0 || int64(byID[602]["first_media_id"].(float64)) != 0 {
		t.Fatalf("hidden-only=%v body=%s", byID[602], w.Body.String())
	}
	c, w = additionalCtx(http.MethodGet, "/api/v1/playlist/601", "601")
	h.GetPlaylist(c)
	if items := decodeItems(t, w); len(items) != 1 || int64(items[0]["media_id"].(float64)) != 302 {
		t.Fatalf("playlist detail=%s", w.Body.String())
	}
	c, w = additionalCtx(http.MethodGet, "/api/v1/favorite-folders", "")
	h.ListFavoriteFolders(c)
	items = decodeItems(t, w)
	if len(items) != 2 {
		t.Fatalf("favorite folders=%s", w.Body.String())
	}
	byID = map[int64]map[string]any{}
	for _, item := range items {
		byID[int64(item["id"].(float64))] = item
	}
	if int64(byID[611]["item_count"].(float64)) != 1 || int64(byID[611]["first_media_id"].(float64)) != 302 || len(byID[611]["preview_items"].([]any)) != 1 {
		t.Fatalf("mixed favorite folder=%v body=%s", byID[611], w.Body.String())
	}
	if int64(byID[612]["item_count"].(float64)) != 0 || int64(byID[612]["first_media_id"].(float64)) != 0 || len(byID[612]["preview_items"].([]any)) != 0 {
		t.Fatalf("hidden favorite folder=%v body=%s", byID[612], w.Body.String())
	}
	c, w = additionalCtx(http.MethodGet, "/api/v1/favorite-folders/611", "611")
	h.GetFavoriteFolder(c)
	if items := decodeItems(t, w); len(items) != 1 || int64(items[0]["media_id"].(float64)) != 302 {
		t.Fatalf("favorite folder detail=%s", w.Body.String())
	}
}

func TestHiddenOnlyMusicAndSeriesAggregatesAreNotBrowsable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	if _, err := h.App.DB.Exec(`
      INSERT INTO music_artist(id,library_id,name,name_norm) VALUES(801,4,'Visible Artist','visible'),(802,4,'Hidden Artist','hidden');
      INSERT INTO music_album(id,library_id,title,title_norm,album_artist_id,artwork_path) VALUES(811,4,'Visible Album','visible',801,'visible.jpg'),(812,4,'Hidden Album','hidden',802,'hidden.jpg');
      INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state) VALUES(821,4,'track-visible','Visible','E:/music/v.mp3','audio','active','published'),(822,4,'track-hidden','Hidden','E:/music/h.mp3','audio','active','processing'),(831,5,'ep-visible','Visible','E:/tv/v.mkv','video','active','degraded'),(832,5,'ep-hidden','Hidden','E:/tv/h.mkv','video','active','failed');
      INSERT INTO music_track(id,album_id,media_id,title,sort_order) VALUES(841,811,821,'Visible',1),(842,812,822,'Hidden',1);
      INSERT INTO series(id,library_id,title,title_norm) VALUES(851,5,'Visible Series','visible'),(852,5,'Hidden Series','hidden');
      INSERT INTO season(id,tv_id,season_num,name) VALUES(861,851,1,'S1'),(862,852,1,'S1');
      INSERT INTO episode(id,season_id,episode_num,title) VALUES(871,861,1,'Visible'),(872,862,1,'Hidden');
      INSERT INTO episode_media(id,episode_id,media_id) VALUES(881,871,831),(882,872,832);
    `); err != nil {
		t.Fatal(err)
	}
	hiddenArtwork := filepath.Join(t.TempDir(), "hidden.jpg")
	if err := os.WriteFile(hiddenArtwork, []byte("hidden artwork"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`UPDATE music_album SET artwork_path=? WHERE id=812; UPDATE music_artist SET artwork_path=? WHERE id=802`, hiddenArtwork, hiddenArtwork); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		call func(*gin.Context)
	}{{"812", h.GetAlbum}, {"802", h.GetArtist}, {"852", h.GetSeries}, {"812", h.ServeAlbumArtwork}, {"802", h.ServeArtistArtwork}} {
		c, w := additionalCtx(http.MethodGet, "/detail/"+tc.id, tc.id)
		tc.call(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("id=%s status=%d body=%s", tc.id, w.Code, w.Body.String())
		}
	}
	c, w := additionalCtx(http.MethodGet, "/api/v1/library/5/series", "5")
	h.ListLibrarySeries(c)
	if items := decodeItems(t, w); len(items) != 1 || int64(items[0]["id"].(float64)) != 851 {
		t.Fatalf("series list=%s", w.Body.String())
	}
	previews, err := h.queryMusicPreviewCandidates(4, 10)
	if err != nil || len(previews) != 1 || previews[0].albumID != 811 || previews[0].mediaID != 821 {
		t.Fatalf("music previews=%+v err=%v", previews, err)
	}
}

func TestHiddenOnlyAggregateSubroutesReturnNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	fetchCalls := 0
	originalFetch := fetchAggregateImageCandidates
	fetchAggregateImageCandidates = func(scraper.Config, string, int, string, string) ([]scraper.ImageCandidate, map[string]string, bool) {
		fetchCalls++
		return []scraper.ImageCandidate{}, map[string]string{}, false
	}
	t.Cleanup(func() { fetchAggregateImageCandidates = originalFetch })
	if _, err := h.App.DB.Exec(`
		UPDATE library SET image_providers='screen_grabber' WHERE id IN (4,5);
		INSERT INTO music_artist(id,library_id,name,name_norm) VALUES(801,4,'Visible Artist','visible'),(802,4,'Hidden Artist','hidden');
		INSERT INTO music_album(id,library_id,title,title_norm,album_artist_id) VALUES(811,4,'Visible Album','visible',801),(812,4,'Hidden Album','hidden',802);
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state) VALUES
		  (821,4,'track-visible','Visible','E:/music/v.mp3','audio','active','published'),
		  (822,4,'track-hidden','Hidden','E:/music/h.mp3','audio','active','processing'),
		  (831,5,'episode-visible','Visible','E:/tv/v.mkv','video','active','degraded'),
		  (832,5,'episode-hidden','Hidden','E:/tv/h.mkv','video','active','failed');
		INSERT INTO music_track(id,album_id,media_id,title,sort_order) VALUES(841,811,821,'Visible',1),(842,812,822,'Hidden',1);
		INSERT INTO series(id,library_id,title,title_norm) VALUES(851,5,'Visible Series','visible'),(852,5,'Hidden Series','hidden');
		INSERT INTO season(id,tv_id,season_num,name) VALUES(861,851,1,'S1'),(862,852,1,'S1');
		INSERT INTO episode(id,season_id,episode_num,title) VALUES(871,861,1,'Visible'),(872,862,1,'Hidden');
		INSERT INTO episode_media(id,episode_id,media_id) VALUES(881,871,831),(882,872,832);
	`); err != nil {
		t.Fatal(err)
	}

	hidden := []struct {
		id   string
		call func(*gin.Context)
	}{
		{"812", h.ListAlbumImageCandidates},
		{"802", h.ListArtistImageCandidates},
		{"802", h.ListArtistAlbums},
		{"852", h.ListSeriesImageCandidates},
		{"862", h.ListSeasonEpisodes},
	}
	for _, tc := range hidden {
		c, w := additionalCtx(http.MethodGet, "/hidden/"+tc.id, tc.id)
		tc.call(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("hidden id=%s status=%d body=%s", tc.id, w.Code, w.Body.String())
		}
	}
	if fetchCalls != 0 {
		t.Fatalf("hidden-only image endpoints called scraper %d times", fetchCalls)
	}

	visible := []struct {
		id   string
		call func(*gin.Context)
	}{
		{"811", h.ListAlbumImageCandidates},
		{"801", h.ListArtistImageCandidates},
		{"801", h.ListArtistAlbums},
		{"851", h.ListSeriesImageCandidates},
		{"861", h.ListSeasonEpisodes},
	}
	for _, tc := range visible {
		c, w := additionalCtx(http.MethodGet, "/visible/"+tc.id, tc.id)
		tc.call(c)
		if w.Code != http.StatusOK {
			t.Fatalf("visible id=%s status=%d body=%s", tc.id, w.Code, w.Body.String())
		}
	}
	if fetchCalls != 3 {
		t.Fatalf("visible image endpoints called scraper %d times, want 3", fetchCalls)
	}
}

func TestSeriesDetailOmitsHiddenOnlySeasons(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	if _, err := h.App.DB.Exec(`
		INSERT INTO series(id,library_id,title,title_norm) VALUES(900,5,'Mixed Series','mixed');
		INSERT INTO season(id,tv_id,season_num,name) VALUES(901,900,1,'Visible Season'),(902,900,2,'Hidden Season');
		INSERT INTO episode(id,season_id,episode_num,title) VALUES(911,901,1,'Visible Episode'),(912,902,1,'Hidden Episode');
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state) VALUES
		  (921,5,'mixed-visible','Visible','E:/tv/visible.mkv','video','active','published'),
		  (922,5,'mixed-hidden','Hidden','E:/tv/hidden.mkv','video','active','processing');
		INSERT INTO episode_media(episode_id,media_id) VALUES(911,921),(912,922);
	`); err != nil {
		t.Fatal(err)
	}
	c, w := additionalCtx(http.MethodGet, "/api/v1/series/900", "900")
	h.GetSeries(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Seasons []map[string]any `json:"seasons"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Seasons) != 1 || int64(body.Seasons[0]["id"].(float64)) != 901 {
		t.Fatalf("seasons=%v body=%s", body.Seasons, w.Body.String())
	}
}

func TestBatchDownloadDocumentsRejectsAnyUnauthorizedSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed", "allowed.pdf")
	denied := filepath.Join(root, "denied", "denied.pdf")
	for _, path := range []string{allowed, denied} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.App.DB.Exec(`INSERT INTO user(id,username,password,role,can_download,library_scope) VALUES(10,'selected-downloader','x','user',1,'selected'); INSERT INTO user_library_permission(user_id,library_id) VALUES(10,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(10,1,?)`, filepath.Dir(allowed)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state) VALUES(1001,1,'allowed-doc','Allowed',?,'document','active','published'),(1002,1,'denied-doc','Denied',?,'document','active','published')`, allowed, denied); err != nil {
		t.Fatal(err)
	}

	call := func(ids []int64) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(batchDownloadBody{MediaIDs: ids})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/documents/download", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		setUserCtx(c, 10, "user", "selected-downloader")
		h.BatchDownloadDocuments(c)
		return w
	}
	if w := call([]int64{1001, 1002}); w.Code != http.StatusForbidden {
		t.Fatalf("mixed status=%d body=%s", w.Code, w.Body.String())
	}
	if w := call([]int64{1001}); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("allowed status=%d content-type=%s body=%s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}

func TestBatchDownloadDocumentsRejectsUnauthorizedDirectoryScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	root := t.TempDir()
	allowedDir, deniedDir := "C:/docs/allowed", "C:/docs/denied"
	allowedFile, deniedFile := filepath.Join(root, "allowed", "a.pdf"), filepath.Join(root, "denied", "d.pdf")
	for _, path := range []string{allowedFile, deniedFile} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("pdf"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.App.DB.Exec(`INSERT INTO user(id,username,password,role,can_download,library_scope) VALUES(10,'selected-downloader','x','user',1,'selected'); INSERT INTO user_library_permission(user_id,library_id) VALUES(10,1); DELETE FROM library_node WHERE library_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO user_library_folder_permission(user_id,library_id,folder_path) VALUES(10,1,?)`, allowedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO library_node(library_id,parent_path,node_path,node_name,node_type,media_id) VALUES(1,'',?,'allowed','dir',NULL),(1,'',?,'denied','dir',NULL)`, allowedDir, deniedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state) VALUES(1011,1,'dir-a','A',?,'document','active','published'),(1012,1,'dir-d','D',?,'document','active','published')`, allowedFile, deniedFile); err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`INSERT INTO library_node(library_id,parent_path,node_path,node_name,node_type,media_id) VALUES(1,?,?, 'a.pdf','file',1011),(1,?,?, 'd.pdf','file',1012)`, allowedDir, allowedDir+"/a.pdf", deniedDir, deniedDir+"/d.pdf"); err != nil {
		t.Fatal(err)
	}

	call := func(dir string, uid int64, role string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(batchDownloadBody{DirPath: dir})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/download", bytes.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		setUserCtx(c, uid, role, role)
		h.BatchDownloadDocuments(c)
		return w
	}
	if w := call(deniedDir, 10, "user"); w.Code != http.StatusForbidden {
		t.Fatalf("denied status=%d body=%s", w.Code, w.Body.String())
	}
	if w := call(allowedDir, 10, "user"); w.Code != http.StatusOK {
		t.Fatalf("allowed status=%d body=%s", w.Code, w.Body.String())
	}
	if w := call(deniedDir, 1, "admin"); w.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPhotoPersonsIgnoreInactiveAndCrossLibraryFaces(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	if _, err := h.App.DB.Exec(`
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state) VALUES
		 (210,2,'valid-face','Valid','E:/photos/v.jpg','image','active','published'),
		 (211,2,'inactive-face','Inactive','E:/photos/i.jpg','image','inactive','published'),
		 (212,3,'cross-face','Cross','E:/media/c.jpg','image','active','published'),
		 (213,2,'wrong-type','Wrong','E:/photos/w.mp4','video','active','published');
		INSERT INTO photo_person(id,library_id,label,cover_face_id,media_count) VALUES(410,2,'Valid Person',610,99),(411,2,'Invalid Only',611,99);
		INSERT INTO photo_face(id,media_id,library_id,person_id) VALUES(610,210,2,410),(611,211,2,411),(612,212,3,410),(613,213,2,410);
	`); err != nil {
		t.Fatal(err)
	}
	c, w := additionalCtx(http.MethodGet, "/api/v1/library/2/photo/persons", "2")
	h.ListPhotoPersons(c)
	items := decodeItems(t, w)
	found := false
	for _, item := range items {
		if item["name"] == "Invalid Only" {
			t.Fatalf("invalid-only leaked body=%s", w.Body.String())
		}
		if item["name"] == "Valid Person" {
			found = true
			if int64(item["count"].(float64)) != 1 || int64(item["cover_face_id"].(float64)) != 610 {
				t.Fatalf("valid person=%v", item)
			}
		}
	}
	if !found {
		t.Fatalf("valid person missing body=%s", w.Body.String())
	}
}

func TestArtistSubroutesCheckAccessBeforeVisibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupAdditionalPublicationDB(t)
	if _, err := h.App.DB.Exec(`
		INSERT INTO user(id,username,password,role,library_scope) VALUES(20,'scoped','x','user','selected');
		INSERT INTO user_library_permission(user_id,library_id) VALUES(20,3);
		INSERT INTO music_artist(id,library_id,name,name_norm) VALUES(1101,4,'Visible Artist','visible'),(1102,4,'Hidden Artist','hidden');
		INSERT INTO music_album(id,library_id,title,title_norm,album_artist_id) VALUES(1111,4,'Visible','visible',1101),(1112,4,'Hidden','hidden',1102);
		INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status,publication_state) VALUES(1121,4,'artist-visible','Visible','E:/music/v.mp3','audio','active','published'),(1122,4,'artist-hidden','Hidden','E:/music/h.mp3','audio','active','failed');
		INSERT INTO music_track(album_id,media_id,title) VALUES(1111,1121,'Visible'),(1112,1122,'Hidden');
	`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1101", "1102"} {
		for _, call := range []func(*gin.Context){h.GetArtist, h.ListArtistAlbums, h.ServeArtistArtwork} {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/artist/"+id, nil)
			c.Params = gin.Params{{Key: "id", Value: id}}
			setUserCtx(c, 20, "user", "scoped")
			call(c)
			if w.Code != http.StatusForbidden {
				t.Fatalf("id=%s status=%d body=%s", id, w.Code, w.Body.String())
			}
		}
	}
}
