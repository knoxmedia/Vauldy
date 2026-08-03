package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/store"
)

func TestCreateLibraryPersistsDRMFlags(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "handler.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	widevineEnabled := true
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{
		DRM: config.DRMConfig{
			Widevine: config.WidevineConfig{Enabled: &widevineEnabled},
			PowerDRM: config.PowerDRMConfig{Enabled: true},
		},
	}}, runningScans: map[int64]scanRuntime{}}

	body := map[string]any{
		"name":                               "Movies",
		"type":                               "movie",
		"folders":                            []string{"E:/videos/movies"},
		"drm_enabled":                        1,
		"encryption_mode":                    "powerdrm",
		"cleanup_local_source_after_package": 1,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/libraries", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.CreateLibrary(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var gotDRM int
	var gotMode string
	var gotCleanup int
	if err := db.QueryRow(`SELECT drm_enabled, encryption_mode, cleanup_local_source_after_package FROM library ORDER BY id DESC LIMIT 1`).
		Scan(&gotDRM, &gotMode, &gotCleanup); err != nil {
		t.Fatalf("query flags: %v", err)
	}
	if gotDRM != 1 || gotMode != "powerdrm" || gotCleanup != 1 {
		t.Fatalf("unexpected flags drm=%d mode=%s cleanup=%d", gotDRM, gotMode, gotCleanup)
	}
}

type libraryProcessingOptionsJSON struct {
	Preview           bool `json:"preview"`
	SubtitleExtract   bool `json:"subtitle_extract"`
	ATrackExtract     bool `json:"atrack_extract"`
	SubtitleRecognize bool `json:"subtitle_recognize"`
	KeyframeExtract   bool `json:"keyframe_extract"`
	AIAnalysis        bool `json:"ai_analysis"`
}
type libraryProcessingJSON struct {
	Explicit   libraryProcessingOptionsJSON `json:"explicit"`
	Effective  libraryProcessingOptionsJSON `json:"effective"`
	Provenance struct {
		Explicit        []string `json:"explicit"`
		DependencyAdded []string `json:"dependency_added"`
	} `json:"provenance"`
}
type libraryProcessingResponse struct {
	ID                int64                       `json:"id"`
	PreviewExtract    int                         `json:"preview_extract"`
	SubtitleExtract   int                         `json:"subtitle_extract"`
	ATrackExtract     int                         `json:"atrack_extract"`
	SubtitleRecognize int                         `json:"subtitle_recognize"`
	KeyframeExtract   int                         `json:"keyframe_extract"`
	AIAnalysis        int                         `json:"ai_analysis"`
	ProcessingOptions libraryProcessingJSON       `json:"processing_options"`
	Items             []libraryProcessingResponse `json:"items"`
}

func newLibraryProcessingHandler(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "library-processing.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Handler{App: &app.App{DB: db, Config: &config.Config{}}, runningScans: map[int64]scanRuntime{}}, db
}
func callLibraryJSON(t *testing.T, h *Handler, method, target string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	setUserCtx(c, 0, "admin", "admin")
	if strings.HasPrefix(target, "/api/v1/libraries/") {
		c.Params = gin.Params{{Key: "id", Value: strings.TrimPrefix(target, "/api/v1/libraries/")}}
	}
	switch method {
	case http.MethodPost:
		h.CreateLibrary(c)
	case http.MethodPut:
		h.UpdateLibrary(c)
	case http.MethodGet:
		h.ListLibraries(c)
	}
	return w
}
func decodeLibraryProcessingResponse(t *testing.T, w *httptest.ResponseRecorder) libraryProcessingResponse {
	t.Helper()
	var got libraryProcessingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	return got
}
func TestLibraryProcessingSchemaColumnsAndDefaults(t *testing.T) {
	_, db := newLibraryProcessingHandler(t)
	for _, column := range []string{"subtitle_extract", "atrack_extract", "subtitle_recognize", "keyframe_extract", "ai_analysis"} {
		var typ string
		var notNull int
		var d sql.NullString
		if err := db.QueryRow(`SELECT type,"notnull",dflt_value FROM pragma_table_info('library') WHERE name=?`, column).Scan(&typ, &notNull, &d); err != nil {
			t.Fatalf("column %s: %v", column, err)
		}
		if typ != "INTEGER" || notNull != 1 || !d.Valid || d.String != "0" {
			t.Fatalf("column %s type=%q notnull=%d default=%q", column, typ, notNull, d.String)
		}
	}
	if _, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('defaults','movie','E:/defaults')`); err != nil {
		t.Fatal(err)
	}
	var v [5]int
	if err := db.QueryRow(`SELECT subtitle_extract,atrack_extract,subtitle_recognize,keyframe_extract,ai_analysis FROM library WHERE name='defaults'`).Scan(&v[0], &v[1], &v[2], &v[3], &v[4]); err != nil {
		t.Fatal(err)
	}
	if v != [5]int{} {
		t.Fatalf("defaults=%v", v)
	}
}
func TestLibraryProcessingMigratesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE library(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,type TEXT NOT NULL,path TEXT NOT NULL,preview_extract INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO library(name,type,path,preview_extract) VALUES('legacy','movie','E:/legacy',1)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	defer db.Close()
	var preview, subtitle, atrack, recognize, keyframe, ai int
	if err := db.QueryRow(`SELECT preview_extract,subtitle_extract,atrack_extract,subtitle_recognize,keyframe_extract,ai_analysis FROM library WHERE name='legacy'`).Scan(&preview, &subtitle, &atrack, &recognize, &keyframe, &ai); err != nil {
		t.Fatal(err)
	}
	if preview != 1 || subtitle != 0 || atrack != 0 || recognize != 0 || keyframe != 0 || ai != 0 {
		t.Fatalf("migrated values=%d,%d,%d,%d,%d,%d", preview, subtitle, atrack, recognize, keyframe, ai)
	}
}

func TestLibraryProcessingRejectsMalformedExistingColumn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		definition string
	}{
		{name: "wrong type", definition: "TEXT NOT NULL DEFAULT 0"},
		{name: "nullable", definition: "INTEGER DEFAULT 0"},
		{name: "wrong default", definition: "INTEGER NOT NULL DEFAULT 1"},
		{name: "missing default", definition: "INTEGER NOT NULL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "malformed.sqlite")
			legacy, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = legacy.Exec(`CREATE TABLE library(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,type TEXT NOT NULL,path TEXT NOT NULL,preview_extract INTEGER DEFAULT 0,subtitle_extract ` + tc.definition + `)`)
			if err != nil {
				t.Fatal(err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}

			db, err := store.OpenSQLite(path)
			if db != nil {
				_ = db.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "incompatible library processing column subtitle_extract") {
				t.Fatalf("OpenSQLite error=%v, want clear incompatible-column failure", err)
			}
		})
	}
}

func TestLibraryProcessingAcceptsNormalizedZeroDefaults(t *testing.T) {
	for _, defaultDDL := range []string{"0", "(0)", "'0'"} {
		t.Run(defaultDDL, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compatible.sqlite")
			legacy, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = legacy.Exec(`CREATE TABLE library(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,type TEXT NOT NULL,path TEXT NOT NULL,preview_extract INTEGER DEFAULT 0,subtitle_extract INTEGER NOT NULL DEFAULT ` + defaultDDL + `)`)
			if err != nil {
				t.Fatal(err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := store.OpenSQLite(path)
			if err != nil {
				t.Fatalf("OpenSQLite compatible default %s: %v", defaultDDL, err)
			}
			_ = db.Close()
		})
	}
}

func TestCreateLibraryProcessingEmptyProvenanceUsesArrays(t *testing.T) {
	h, _ := newLibraryProcessingHandler(t)
	w := callLibraryJSON(t, h, http.MethodPost, "/api/v1/libraries", map[string]any{"name": "Defaults", "type": "movie", "folders": []string{"E:/defaults"}})
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var raw struct {
		ProcessingOptions struct {
			Provenance struct {
				Explicit        json.RawMessage `json:"explicit"`
				DependencyAdded json.RawMessage `json:"dependency_added"`
			} `json:"provenance"`
		} `json:"processing_options"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if string(raw.ProcessingOptions.Provenance.Explicit) != "[]" || string(raw.ProcessingOptions.Provenance.DependencyAdded) != "[]" {
		t.Fatalf("provenance must use arrays: %s", w.Body.String())
	}
}

func TestCreateLibraryProcessingRecognitionClosurePreservesExplicit(t *testing.T) {
	h, db := newLibraryProcessingHandler(t)
	w := callLibraryJSON(t, h, http.MethodPost, "/api/v1/libraries", map[string]any{"name": "Movies", "type": "movie", "folders": []string{"E:/movies"}, "subtitle_recognize": 1})
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	created := decodeLibraryProcessingResponse(t, w)
	if !created.ProcessingOptions.Explicit.SubtitleRecognize || created.ProcessingOptions.Explicit.SubtitleExtract || created.ProcessingOptions.Explicit.ATrackExtract {
		t.Fatalf("explicit=%+v", created.ProcessingOptions.Explicit)
	}
	if !created.ProcessingOptions.Effective.SubtitleRecognize || !created.ProcessingOptions.Effective.SubtitleExtract || !created.ProcessingOptions.Effective.ATrackExtract {
		t.Fatalf("effective=%+v", created.ProcessingOptions.Effective)
	}
	var s, a, r int
	if err := db.QueryRow(`SELECT subtitle_extract,atrack_extract,subtitle_recognize FROM library WHERE id=?`, created.ID).Scan(&s, &a, &r); err != nil {
		t.Fatal(err)
	}
	if s != 0 || a != 0 || r != 1 {
		t.Fatalf("persisted=%d,%d,%d", s, a, r)
	}
	listResponse := callLibraryJSON(t, h, http.MethodGet, "/api/v1/libraries", nil)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	listed := decodeLibraryProcessingResponse(t, listResponse).Items[0]
	if !reflect.DeepEqual(listed.ProcessingOptions, created.ProcessingOptions) {
		t.Fatalf("list=%+v create=%+v", listed.ProcessingOptions, created.ProcessingOptions)
	}
	if listed.SubtitleRecognize != 1 || listed.SubtitleExtract != 0 || listed.ATrackExtract != 0 {
		t.Fatalf("legacy=%+v", listed)
	}
}
func TestCreateLibraryProcessingAIClosure(t *testing.T) {
	h, _ := newLibraryProcessingHandler(t)
	w := callLibraryJSON(t, h, http.MethodPost, "/api/v1/libraries", map[string]any{"name": "AI", "type": "video", "folders": []string{"E:/ai"}, "ai_analysis": 1})
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeLibraryProcessingResponse(t, w).ProcessingOptions
	if !got.Explicit.AIAnalysis || got.Explicit.SubtitleRecognize || got.Explicit.SubtitleExtract || got.Explicit.ATrackExtract {
		t.Fatalf("explicit=%+v", got.Explicit)
	}
	if !got.Effective.AIAnalysis || !got.Effective.SubtitleRecognize || !got.Effective.SubtitleExtract || !got.Effective.ATrackExtract {
		t.Fatalf("effective=%+v", got.Effective)
	}
}
func TestUpdateLibraryProcessingOmissionPreservesExplicit(t *testing.T) {
	h, db := newLibraryProcessingHandler(t)
	res, err := db.Exec(`INSERT INTO library(name,type,path,subtitle_extract,keyframe_extract,ai_analysis) VALUES('Old','movie','E:/old',1,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	w := callLibraryJSON(t, h, http.MethodPut, fmt.Sprintf("/api/v1/libraries/%d", id), map[string]any{"name": "New", "type": "movie", "folders": []string{"E:/new"}, "subtitle_extract": 0})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeLibraryProcessingResponse(t, w)
	if got.SubtitleExtract != 0 || got.KeyframeExtract != 1 || got.AIAnalysis != 1 {
		t.Fatalf("response=%+v", got)
	}
	var s, k, a int
	if err := db.QueryRow(`SELECT subtitle_extract,keyframe_extract,ai_analysis FROM library WHERE id=?`, id).Scan(&s, &k, &a); err != nil {
		t.Fatal(err)
	}
	if s != 0 || k != 1 || a != 1 {
		t.Fatalf("persisted=%d,%d,%d", s, k, a)
	}
}
func TestCreateLibraryProcessingAnimeUsesVideoClosure(t *testing.T) {
	h, _ := newLibraryProcessingHandler(t)
	w := callLibraryJSON(t, h, http.MethodPost, "/api/v1/libraries", map[string]any{"name": "Anime", "type": "anime", "folders": []string{"E:/anime"}, "subtitle_recognize": 1})
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeLibraryProcessingResponse(t, w).ProcessingOptions
	if !got.Explicit.SubtitleRecognize || got.Explicit.SubtitleExtract || got.Explicit.ATrackExtract {
		t.Fatalf("explicit=%+v", got.Explicit)
	}
	if !got.Effective.SubtitleRecognize || !got.Effective.SubtitleExtract || !got.Effective.ATrackExtract {
		t.Fatalf("effective=%+v", got.Effective)
	}
}

func TestCreateLibraryProcessingNonVideoKeepsExplicitAndDisablesEffective(t *testing.T) {
	h, _ := newLibraryProcessingHandler(t)
	w := callLibraryJSON(t, h, http.MethodPost, "/api/v1/libraries", map[string]any{"name": "Music", "type": "music", "folders": []string{"E:/music"}, "ai_analysis": 1, "preview_extract": 1})
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeLibraryProcessingResponse(t, w).ProcessingOptions
	if !got.Explicit.AIAnalysis || !got.Explicit.Preview {
		t.Fatalf("explicit=%+v", got.Explicit)
	}
	if got.Effective != (libraryProcessingOptionsJSON{}) {
		t.Fatalf("effective=%+v", got.Effective)
	}
	if !reflect.DeepEqual(got.Provenance.Explicit, []string{"ai_analysis", "preview"}) || len(got.Provenance.DependencyAdded) != 0 {
		t.Fatalf("provenance=%+v", got.Provenance)
	}
}
func TestUpdateLibraryProcessingRejectsNonBinaryValues(t *testing.T) {
	h, db := newLibraryProcessingHandler(t)
	res, err := db.Exec(`INSERT INTO library(name,type,path,subtitle_recognize) VALUES('Existing','anime','E:/anime',1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	w := callLibraryJSON(t, h, http.MethodPut, fmt.Sprintf("/api/v1/libraries/%d", id), map[string]any{"name": "Existing", "type": "anime", "folders": []string{"E:/anime"}, "ai_analysis": -1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var recognize int
	if err := db.QueryRow(`SELECT subtitle_recognize FROM library WHERE id=?`, id).Scan(&recognize); err != nil {
		t.Fatal(err)
	}
	if recognize != 1 {
		t.Fatalf("stored value changed to %d", recognize)
	}
}

func TestCreateLibraryProcessingRejectsNonBinaryValues(t *testing.T) {
	for _, field := range []string{"preview_extract", "subtitle_extract", "atrack_extract", "subtitle_recognize", "keyframe_extract", "ai_analysis"} {
		t.Run(field, func(t *testing.T) {
			h, _ := newLibraryProcessingHandler(t)
			w := callLibraryJSON(t, h, http.MethodPost, "/api/v1/libraries", map[string]any{"name": "Bad", "type": "movie", "folders": []string{"E:/bad"}, field: 2})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
