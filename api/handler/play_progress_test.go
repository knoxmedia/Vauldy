package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"

	"knox-media/api/middleware"
	"knox-media/cmd/scheduler"
	"knox-media/internal/app"
	"knox-media/internal/auth"
	"knox-media/internal/config"
	"knox-media/internal/store"
)

type progressEndpointFixture struct {
	t       *testing.T
	h       *Handler
	db      *sql.DB
	mediaID int64
	fileID  string
	userID  int64
}

func newProgressEndpointFixture(t *testing.T, fileType string) *progressEndpointFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "progress.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stmts := []string{
		`INSERT INTO library (id,name,type,path,enabled) VALUES (1,'videos','movie','E:/videos',1)`,
		`INSERT INTO user (id,username,password,role,can_play,library_scope) VALUES (1,'viewer','x','user',1,'all')`,
		`INSERT INTO media (id,library_id,file_id,file_path,file_type,duration) VALUES (10,1,'file-10','E:/videos/a.mp4','` + fileType + `',1000)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture SQL: %v", err)
		}
	}
	return &progressEndpointFixture{t: t, h: &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}, db: db, mediaID: 10, fileID: "file-10", userID: 1}
}

func (f *progressEndpointFixture) request(method, suffix, body string, auth bool, apiClient bool) *httptest.ResponseRecorder {
	f.t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/api/v1/media/10/"+suffix, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "10"}}
	if auth {
		setUserCtx(c, f.userID, "user", "viewer")
	}
	if apiClient {
		c.Set("role", "api_client")
	}
	switch suffix {
	case "progress":
		if method == http.MethodDelete {
			f.h.ClearProgress(c)
		} else {
			f.h.SaveProgress(c)
		}
	case "watched":
		f.h.ToggleWatched(c)
	case "playback/start":
		f.h.PlaybackStart(c)
	case "playback/end":
		f.h.PlaybackEnd(c)
	default:
		f.t.Fatalf("unknown endpoint %q", suffix)
	}
	return w
}

func (f *progressEndpointFixture) rowCounts() (progress, sessions int) {
	f.t.Helper()
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM play_progress`).Scan(&progress); err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM playback_completion_session`).Scan(&sessions); err != nil {
		f.t.Fatal(err)
	}
	return
}

func TestSaveProgressRejectsInvalidEvidence(t *testing.T) {
	cases := []string{
		`{"position":10,"event":"bad","session_id":"s","sequence":1}`,
		`{"position":10,"event":"progress","session_id":"","sequence":1}`,
		`{"position":10,"event":"progress","session_id":"s","sequence":0}`,
		`{"position":-1,"event":"progress","session_id":"s","sequence":1}`,
		`{"position":10,"event":"progress","session_id":"s"}`,
		`{"position":10,"session_id":"s","sequence":1}`,
		`{"position":10,"event":"progress","sequence":1}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			f := newProgressEndpointFixture(t, "video")
			w := f.request(http.MethodPost, "progress", body, true, false)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if p, s := f.rowCounts(); p != 0 || s != 0 {
				t.Fatalf("invalid evidence created rows: progress=%d sessions=%d", p, s)
			}
		})
	}
}

func TestSaveProgressJSONAndBodyValidation(t *testing.T) {
	cases := []string{"", `{`, `{}`, `{"position":null}`, `{"position":"10","completed":0}`}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			f := newProgressEndpointFixture(t, "video")
			w := f.request(http.MethodPost, "progress", body, true, false)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSaveProgressPreservesCredentialContracts(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	body := `{"position":10,"event":"progress","session_id":"s","sequence":1}`
	if w := f.request(http.MethodPost, "progress", body, false, true); w.Code != http.StatusForbidden {
		t.Fatalf("API client status=%d body=%s", w.Code, w.Body.String())
	}
	if w := f.request(http.MethodPost, "progress", body, false, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing login status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPlaybackStartSessionCreatesBaselineWithoutResettingWatched(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	if _, err := f.db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed,play_count) VALUES(1,'file-10',900,1,2)`); err != nil {
		t.Fatal(err)
	}
	w := f.request(http.MethodPost, "playback/start", `{"position":850,"event":"start","session_id":"app-session","jit_session_id":"jit-session","sequence":1}`, true, false)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var active, completed, count int
	var sid string
	if err := f.db.QueryRow(`SELECT session_id,active FROM playback_completion_session WHERE user_id=1 AND file_id='file-10'`).Scan(&sid, &active); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT completed,play_count FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&completed, &count); err != nil {
		t.Fatal(err)
	}
	if sid != "app-session" || active != 1 || completed != 1 || count != 3 {
		t.Fatalf("sid=%q active=%d completed=%d count=%d", sid, active, completed, count)
	}
}

func TestProgressEvidenceResponseAndDuplicate(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	start := f.request(http.MethodPost, "playback/start", `{"position":100,"event":"start","session_id":"s","sequence":1}`, true, false)
	if start.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	body := `{"position":110,"event":"progress","session_id":"s","sequence":2}`
	first := f.request(http.MethodPost, "progress", body, true, false)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var result struct {
		Completed         bool  `json:"completed"`
		AutoCompleted     bool  `json:"auto_completed"`
		EffectivePosition int64 `json:"effective_position"`
		Stale             bool  `json:"stale"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Completed || result.AutoCompleted || result.EffectivePosition != 110 || result.Stale {
		t.Fatalf("result=%+v body=%s", result, first.Body.String())
	}
	duplicate := f.request(http.MethodPost, "progress", body, true, false)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	if err := json.Unmarshal(duplicate.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Stale || result.EffectivePosition != 110 {
		t.Fatalf("duplicate result=%+v", result)
	}
}

func TestProgressEvidenceSeekDoesNotComplete(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	if w := f.request(http.MethodPost, "playback/start", `{"position":100,"event":"start","session_id":"s","sequence":1}`, true, false); w.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", w.Code, w.Body.String())
	}
	w := f.request(http.MethodPost, "progress", `{"position":950,"event":"seek","session_id":"s","sequence":2}`, true, false)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"completed":true`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSaveProgressLegacyIsMonotonicAndDoesNotAccumulateEvidence(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	if _, err := f.db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed) VALUES(1,'file-10',900,1)`); err != nil {
		t.Fatal(err)
	}
	w := f.request(http.MethodPost, "progress", `{"position":100,"completed":0}`, true, false)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var completed, sessions int
	if err := f.db.QueryRow(`SELECT completed FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	_, sessions = f.rowCounts()
	if completed != 1 || sessions != 0 {
		t.Fatalf("completed=%d sessions=%d", completed, sessions)
	}
}

func TestSaveProgressLegacyCompletedMarksNonVideo(t *testing.T) {
	f := newProgressEndpointFixture(t, "audio")
	w := f.request(http.MethodPost, "progress", `{"position":77,"completed":1}`, true, false)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var completed int
	if err := f.db.QueryRow(`SELECT completed FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Fatalf("completed=%d", completed)
	}
}

func TestSaveProgressDBErrorReturns500WithoutActivity(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	if _, err := f.db.Exec(`DROP TABLE playback_completion_session`); err != nil {
		t.Fatal(err)
	}
	w := f.request(http.MethodPost, "progress", `{"position":10,"event":"progress","session_id":"s","sequence":1}`, true, false)
	if w.Code != http.StatusInternalServerError || w.Body.String() != `{"error":"internal playback persistence error"}` {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var logs int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM activity_log WHERE action='progress'`).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if logs != 0 {
		t.Fatalf("activity logs=%d", logs)
	}
}

func TestPlaybackEndDoesNotResetProgress(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	if _, err := f.db.Exec(`INSERT INTO play_progress(user_id,file_id,position,completed) VALUES(1,'file-10',900,1)`); err != nil {
		t.Fatal(err)
	}
	w := f.request(http.MethodPost, "playback/end", `{"position":100,"completed":0,"session_id":"app","jit_session_id":"jit"}`, true, false)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var position, completed int
	if err := f.db.QueryRow(`SELECT position,completed FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&position, &completed); err != nil {
		t.Fatal(err)
	}
	if position != 900 || completed != 1 {
		t.Fatalf("position=%d completed=%d", position, completed)
	}
}

func TestPlaybackEndLegacyCompletedMarksWatched(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	w := f.request(http.MethodPost, "playback/end", `{"position":1000,"completed":1}`, true, false)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var position, completed int
	if err := f.db.QueryRow(`SELECT position,completed FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&position, &completed); err != nil {
		t.Fatal(err)
	}
	if position != 1000 || completed != 1 {
		t.Fatalf("position=%d completed=%d", position, completed)
	}
}

func TestPlaybackEndRejectsPartialNewProtocol(t *testing.T) {
	for _, body := range []string{
		`{"position":10,"event":"ended","session_id":"s"}`,
		`{"position":10,"sequence":2,"session_id":"s"}`,
		`{"position":10,"event":"progress","sequence":2,"session_id":"s"}`,
	} {
		t.Run(body, func(t *testing.T) {
			f := newProgressEndpointFixture(t, "video")
			w := f.request(http.MethodPost, "playback/end", body, true, false)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func (f *progressEndpointFixture) activityCount(action string) int {
	f.t.Helper()
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM activity_log WHERE action=?`, action).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func TestPlaybackStartAPIClientIgnoresApplicationEvidence(t *testing.T) {
	for _, body := range []string{
		`{"position":10,"event":"start","session_id":"app","sequence":1}`,
		`{"position":10,"event":"start","session_id":"app"}`,
		`{"position":10,"event":null,"sequence":null,"session_id":"app"}`,
	} {
		t.Run(body, func(t *testing.T) {
			f := newProgressEndpointFixture(t, "video")
			w := f.request(http.MethodPost, "playback/start", body, false, true)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if progress, sessions := f.rowCounts(); progress != 0 || sessions != 0 {
				t.Fatalf("progress=%d sessions=%d", progress, sessions)
			}
			if logs := f.activityCount("playback_start"); logs != 1 {
				t.Fatalf("activity logs=%d", logs)
			}
		})
	}
}

func TestPlaybackEndAPIClientIgnoresApplicationEvidenceAndStopsJIT(t *testing.T) {
	for _, body := range []string{
		`{"position":10,"event":"ended","session_id":"app","jit_session_id":"jit-end","sequence":2}`,
		`{"position":10,"event":"ended","session_id":"app","jit_session_id":"jit-end"}`,
		`{"position":10,"event":null,"sequence":null,"session_id":"app","jit_session_id":"jit-end"}`,
	} {
		t.Run(body, func(t *testing.T) {
			f := newProgressEndpointFixture(t, "video")
			mr, err := miniredis.Run()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(mr.Close)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			if err := rdb.HSet(context.Background(), "session:jit-end", "file_id", "file-10").Err(); err != nil {
				t.Fatal(err)
			}
			f.h.Instant = scheduler.NewScheduler(rdb, nil)
			w := f.request(http.MethodPost, "playback/end", body, false, true)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if exists, err := rdb.Exists(context.Background(), "session:jit-end").Result(); err != nil || exists != 0 {
				t.Fatalf("JIT session exists=%d err=%v", exists, err)
			}
			if progress, sessions := f.rowCounts(); progress != 0 || sessions != 0 {
				t.Fatalf("progress=%d sessions=%d", progress, sessions)
			}
			if logs := f.activityCount("playback_end"); logs != 1 {
				t.Fatalf("activity logs=%d", logs)
			}
		})
	}
}

func attachTestJITScheduler(t *testing.T, f *progressEndpointFixture, sessions ...string) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	for _, session := range sessions {
		if err := rdb.HSet(context.Background(), "session:"+session, "file_id", "file-10").Err(); err != nil {
			t.Fatal(err)
		}
	}
	f.h.Instant = scheduler.NewScheduler(rdb, nil)
	return rdb
}

func TestPlaybackEndNewProtocolPersistsEndedAndDuplicateIsSafe(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	if w := f.request(http.MethodPost, "playback/start", `{"position":900,"event":"start","session_id":"app","sequence":1}`, true, false); w.Code != http.StatusOK {
		t.Fatalf("start=%d %s", w.Code, w.Body.String())
	}
	body := `{"position":950,"event":"ended","session_id":"app","sequence":2}`
	for i := 0; i < 2; i++ {
		if w := f.request(http.MethodPost, "playback/end", body, true, false); w.Code != http.StatusOK {
			t.Fatalf("end %d=%d %s", i, w.Code, w.Body.String())
		}
	}
	var completed, sequence int
	var ended sql.NullString
	if err := f.db.QueryRow(`SELECT completed,play_end_at FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&completed, &ended); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT last_sequence FROM playback_completion_session WHERE user_id=1 AND file_id='file-10' AND session_id='app'`).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	if completed != 1 || !ended.Valid || sequence != 2 {
		t.Fatalf("completed=%d ended=%v sequence=%d", completed, ended.Valid, sequence)
	}
}

func TestPlaybackEndPersistenceFailurePreventsJITAndActivity(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	if w := f.request(http.MethodPost, "playback/start", `{"position":900,"event":"start","session_id":"app","sequence":1}`, true, false); w.Code != http.StatusOK {
		t.Fatalf("start=%d %s", w.Code, w.Body.String())
	}
	rdb := attachTestJITScheduler(t, f, "jit-end")
	if _, err := f.db.Exec(`DROP TABLE playback_completion_session`); err != nil {
		t.Fatal(err)
	}
	w := f.request(http.MethodPost, "playback/end", `{"position":950,"event":"ended","session_id":"app","jit_session_id":"jit-end","sequence":2}`, true, false)
	if w.Code != http.StatusInternalServerError || w.Body.String() != `{"error":"internal playback persistence error"}` {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if n, _ := rdb.Exists(context.Background(), "session:jit-end").Result(); n != 1 {
		t.Fatalf("JIT session removed on failure")
	}
	if logs := f.activityCount("playback_end"); logs != 0 {
		t.Fatalf("activity=%d", logs)
	}
}

func TestPlaybackEndJITCorrelationLegacyAndNewProtocol(t *testing.T) {
	t.Run("legacy session_id fallback", func(t *testing.T) {
		f := newProgressEndpointFixture(t, "video")
		rdb := attachTestJITScheduler(t, f, "legacy-jit")
		w := f.request(http.MethodPost, "playback/end", `{"position":10,"session_id":"legacy-jit"}`, true, false)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if n, _ := rdb.Exists(context.Background(), "session:legacy-jit").Result(); n != 0 {
			t.Fatal("legacy session not ended")
		}
	})
	t.Run("explicit jit session wins", func(t *testing.T) {
		f := newProgressEndpointFixture(t, "video")
		rdb := attachTestJITScheduler(t, f, "legacy-app", "explicit-jit")
		w := f.request(http.MethodPost, "playback/end", `{"position":10,"session_id":"legacy-app","jit_session_id":"explicit-jit"}`, true, false)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if explicit, _ := rdb.Exists(context.Background(), "session:explicit-jit").Result(); explicit != 0 {
			t.Fatal("explicit JIT session not ended")
		}
		if legacy, _ := rdb.Exists(context.Background(), "session:legacy-app").Result(); legacy != 1 {
			t.Fatal("legacy fallback used despite explicit JIT session")
		}
	})
	t.Run("new app session is not jit", func(t *testing.T) {
		f := newProgressEndpointFixture(t, "video")
		if w := f.request(http.MethodPost, "playback/start", `{"position":1,"event":"start","session_id":"app-only","sequence":1}`, true, false); w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		rdb := attachTestJITScheduler(t, f, "app-only", "header-jit")
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"position":2,"event":"ended","session_id":"app-only","sequence":2}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("X-Session-ID", "header-jit")
		c.Params = gin.Params{{Key: "id", Value: "10"}}
		setUserCtx(c, 1, "user", "viewer")
		f.h.PlaybackEnd(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if app, _ := rdb.Exists(context.Background(), "session:app-only").Result(); app != 1 {
			t.Fatal("application session used as JIT")
		}
		if header, _ := rdb.Exists(context.Background(), "session:header-jit").Result(); header != 0 {
			t.Fatal("header JIT not ended")
		}
	})
}

func TestPlaybackEvidenceNullPresenceRejectsOrdinaryUsers(t *testing.T) {
	bodies := []string{`{"position":10,"event":null,"completed":1}`, `{"position":10,"sequence":null,"completed":1}`, `{"position":10,"event":null,"sequence":null,"completed":1}`}
	for _, endpoint := range []string{"playback/start", "progress", "playback/end"} {
		for _, body := range bodies {
			t.Run(endpoint+body, func(t *testing.T) {
				f := newProgressEndpointFixture(t, "video")
				w := f.request(http.MethodPost, endpoint, body, true, false)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				if p, s := f.rowCounts(); p != 0 || s != 0 {
					t.Fatalf("writes p=%d s=%d", p, s)
				}
			})
		}
	}
}

func TestSaveProgressLegacyResponseIsCompatibleSuperset(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	w := f.request(http.MethodPost, "progress", `{"position":10,"completed":0}`, true, false)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ok", "completed", "auto_completed", "effective_position", "stale"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing %s: %s", key, w.Body.String())
		}
	}
}
func TestSaveProgressNewResponseHasExactSaveResultFields(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	if w := f.request(http.MethodPost, "playback/start", `{"position":1,"event":"start","session_id":"s","sequence":1}`, true, false); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := f.request(http.MethodPost, "progress", `{"position":2,"event":"progress","session_id":"s","sequence":2}`, true, false)
	var payload map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	if len(payload) != 4 {
		t.Fatalf("fields=%v", payload)
	}
}

func TestPlaybackEvidenceBodySizeLimit(t *testing.T) {
	large := `{"position":1,"padding":"` + strings.Repeat("x", 20*1024) + `"}`
	for _, endpoint := range []string{"playback/start", "progress", "playback/end"} {
		t.Run(endpoint, func(t *testing.T) {
			f := newProgressEndpointFixture(t, "video")
			w := f.request(http.MethodPost, endpoint, large, true, false)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if p, s := f.rowCounts(); p != 0 || s != 0 {
				t.Fatalf("writes p=%d s=%d", p, s)
			}
		})
	}
}

func TestPlaybackEvidencePresenceIsCaseInsensitive(t *testing.T) {
	bodies := []string{
		`{"position":10,"Event":null,"completed":1}`,
		`{"position":10,"EVENT":"progress","completed":1}`,
		`{"position":10,"Sequence":null,"completed":1}`,
		`{"position":10,"SEQUENCE":1,"completed":1}`,
		`{"position":10,"EVENT":null,"SEQUENCE":null,"completed":1}`,
	}
	for _, endpoint := range []string{"playback/start", "progress", "playback/end"} {
		for _, body := range bodies {
			t.Run(endpoint+body, func(t *testing.T) {
				f := newProgressEndpointFixture(t, "video")
				w := f.request(http.MethodPost, endpoint, body, true, false)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				if progress, sessions := f.rowCounts(); progress != 0 || sessions != 0 {
					t.Fatalf("writes progress=%d sessions=%d", progress, sessions)
				}
			})
		}
	}
}

func TestPlaybackEvidenceRejectsAmbiguousCaseDuplicateKeys(t *testing.T) {
	bodies := []string{
		`{"position":10,"event":"progress","EVENT":null,"session_id":"s","sequence":1,"completed":1}`,
		`{"position":10,"event":"progress","session_id":"s","sequence":1,"SEQUENCE":2,"completed":1}`,
	}
	for _, endpoint := range []string{"playback/start", "progress", "playback/end"} {
		for _, body := range bodies {
			t.Run(endpoint+body, func(t *testing.T) {
				f := newProgressEndpointFixture(t, "video")
				w := f.request(http.MethodPost, endpoint, body, true, false)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				if progress, sessions := f.rowCounts(); progress != 0 || sessions != 0 {
					t.Fatalf("writes progress=%d sessions=%d", progress, sessions)
				}
			})
		}
	}
}

func TestPlaybackEvidenceRejectsExactDuplicateKeysBothOrders(t *testing.T) {
	bodies := []string{
		`{"position":10,"event":"progress","event":null,"session_id":"s","sequence":1,"completed":1}`,
		`{"position":10,"event":null,"event":"progress","session_id":"s","sequence":1,"completed":1}`,
		`{"position":10,"event":"progress","session_id":"s","sequence":1,"sequence":null,"completed":1}`,
		`{"position":10,"event":"progress","session_id":"s","sequence":null,"sequence":1,"completed":1}`,
	}
	for _, endpoint := range []string{"progress", "playback/end"} {
		for _, body := range bodies {
			t.Run(endpoint+body, func(t *testing.T) {
				f := newProgressEndpointFixture(t, "video")
				w := f.request(http.MethodPost, endpoint, body, true, false)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				if progress, sessions := f.rowCounts(); progress != 0 || sessions != 0 {
					t.Fatalf("writes progress=%d sessions=%d", progress, sessions)
				}
			})
		}
	}
}

func TestPlaybackEvidenceRequiresTopLevelObject(t *testing.T) {
	for _, body := range []string{`[]`, `null`, `1`, `"object"`} {
		t.Run(body, func(t *testing.T) {
			f := newProgressEndpointFixture(t, "video")
			w := f.request(http.MethodPost, "progress", body, true, false)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestWatchedProgressExplicitClearSequence(t *testing.T) {
	f := newProgressEndpointFixture(t, "video")
	if w := f.request(http.MethodPut, "watched", "", true, false); w.Code != http.StatusOK {
		t.Fatalf("PUT watched status=%d body=%s", w.Code, w.Body.String())
	}
	if w := f.request(http.MethodPost, "progress", `{"position":100,"completed":0}`, true, false); w.Code != http.StatusOK {
		t.Fatalf("legacy progress status=%d body=%s", w.Code, w.Body.String())
	}
	if w := f.request(http.MethodPost, "playback/start", `{"position":100,"event":"start","session_id":"new","sequence":1}`, true, false); w.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", w.Code, w.Body.String())
	}
	if w := f.request(http.MethodPost, "progress", `{"position":110,"event":"progress","session_id":"new","sequence":2}`, true, false); w.Code != http.StatusOK {
		t.Fatalf("new progress status=%d body=%s", w.Code, w.Body.String())
	}
	var completed int
	if err := f.db.QueryRow(`SELECT completed FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&completed); err != nil || completed != 1 {
		t.Fatalf("before unwatch completed=%d err=%v", completed, err)
	}
	if w := f.request(http.MethodDelete, "watched", "", true, false); w.Code != http.StatusOK {
		t.Fatalf("DELETE watched status=%d body=%s", w.Code, w.Body.String())
	}
	progress, sessions := f.rowCounts()
	if progress != 1 || sessions != 0 {
		t.Fatalf("after unwatch progress=%d sessions=%d", progress, sessions)
	}
	if err := f.db.QueryRow(`SELECT completed FROM play_progress WHERE user_id=1 AND file_id='file-10'`).Scan(&completed); err != nil || completed != 0 {
		t.Fatalf("after unwatch completed=%d err=%v", completed, err)
	}
	if _, err := f.db.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id) VALUES(1,'file-10','delete-me')`); err != nil {
		t.Fatal(err)
	}
	if w := f.request(http.MethodDelete, "progress", "", true, false); w.Code != http.StatusOK {
		t.Fatalf("DELETE progress status=%d body=%s", w.Code, w.Body.String())
	}
	if progress, sessions = f.rowCounts(); progress != 0 || sessions != 0 {
		t.Fatalf("after clear progress=%d sessions=%d", progress, sessions)
	}
}

func TestWatchedAndClearProgressFailuresReturn500(t *testing.T) {
	cases := []struct{ method, suffix string }{{http.MethodPut, "watched"}, {http.MethodDelete, "watched"}, {http.MethodDelete, "progress"}}
	for _, tc := range cases {
		t.Run(tc.method+"_"+tc.suffix, func(t *testing.T) {
			f := newProgressEndpointFixture(t, "video")
			if _, err := f.db.Exec(`DROP TABLE playback_completion_session`); err != nil {
				t.Fatal(err)
			}
			w := f.request(tc.method, tc.suffix, "", true, false)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

type autoWatchedAcceptanceFixture struct {
	t      *testing.T
	db     *sql.DB
	router *gin.Engine
	now    time.Time
	token  string
	userID int64
}

func newAutoWatchedAcceptanceFixture(t *testing.T) *autoWatchedAcceptanceFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "auto-watched-acceptance.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		`INSERT INTO library (id,name,type,path,enabled) VALUES (1,'videos','movie','E:/videos',1)`,
		`INSERT INTO library (id,name,type,path,enabled) VALUES (2,'music','music','E:/music',1)`,
		`INSERT INTO user (id,username,password,role,can_play,library_scope) VALUES (1,'viewer','x','user',1,'all')`,
		`INSERT INTO media (id,library_id,file_id,file_path,file_type,duration,title) VALUES (10,1,'video-natural','E:/videos/natural.mp4','video',1000,'Natural')`,
		`INSERT INTO media (id,library_id,file_id,file_path,file_type,duration,title) VALUES (11,1,'video-seek','E:/videos/seek.mp4','video',1000,'Seek')`,
		`INSERT INTO media (id,library_id,file_id,file_path,file_type,duration,title) VALUES (12,2,'music-threshold','E:/music/song.flac','audio',1000,'Song')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture SQL: %v", err)
		}
	}

	cfg := &config.Config{}
	cfg.Security.JWTSecret = "auto-watched-acceptance-secret"
	token, err := auth.SignToken(cfg.Security.JWTSecret, 1, 1, "viewer", "user")
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	f := &autoWatchedAcceptanceFixture{
		t: t, db: db, now: time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC), token: token, userID: 1,
	}
	h := &Handler{App: &app.App{DB: db, Config: cfg}, runningScans: map[int64]scanRuntime{}}
	h.PlayCompletionNow = func() time.Time { return f.now }
	r := gin.New()
	authenticated := r.Group("/api/v1")
	authenticated.Use(middleware.RequireAuthentication(cfg, false))
	authenticated.POST("/media/:id/playback/start", h.PlaybackStart)
	authenticated.POST("/media/:id/progress", h.SaveProgress)
	authenticated.DELETE("/media/:id/watched", h.ToggleWatched)
	authenticated.GET("/user/history", h.UserHistory)
	authenticated.GET("/media", h.ListMedia)
	f.router = r
	return f
}

func (f *autoWatchedAcceptanceFixture) advance(seconds int) {
	f.now = f.now.Add(time.Duration(seconds) * time.Second)
}

func (f *autoWatchedAcceptanceFixture) request(method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	f.router.ServeHTTP(w, req)
	return w
}

func decodeAcceptanceJSON(t *testing.T, w *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
		t.Fatalf("decode status=%d body=%q: %v", w.Code, w.Body.String(), err)
	}
}

func (f *autoWatchedAcceptanceFixture) start(mediaID int64, session string, position int64) {
	f.t.Helper()
	body := fmt.Sprintf(`{"position":%d,"event":"start","session_id":%q,"sequence":1}`, position, session)
	w := f.request(http.MethodPost, fmt.Sprintf("/api/v1/media/%d/playback/start", mediaID), body)
	if w.Code != http.StatusOK {
		f.t.Fatalf("start media=%d status=%d body=%s", mediaID, w.Code, w.Body.String())
	}
}

func (f *autoWatchedAcceptanceFixture) evidence(mediaID int64, session, event string, sequence, position int64) (result struct {
	Completed         bool  `json:"completed"`
	AutoCompleted     bool  `json:"auto_completed"`
	EffectivePosition int64 `json:"effective_position"`
	Stale             bool  `json:"stale"`
}) {
	f.t.Helper()
	body := fmt.Sprintf(`{"position":%d,"event":%q,"session_id":%q,"sequence":%d}`, position, event, session, sequence)
	w := f.request(http.MethodPost, fmt.Sprintf("/api/v1/media/%d/progress", mediaID), body)
	if w.Code != http.StatusOK {
		f.t.Fatalf("evidence media=%d status=%d body=%s", mediaID, w.Code, w.Body.String())
	}
	decodeAcceptanceJSON(f.t, w, &result)
	return result
}

func TestVideoAutoWatchedAPISequenceAcceptance(t *testing.T) {
	f := newAutoWatchedAcceptanceFixture(t)

	f.start(10, "natural-session", 850)
	positions := []int64{865, 882, 899, 916, 933, 950}
	for i, position := range positions {
		f.advance(10)
		result := f.evidence(10, "natural-session", "progress", int64(i+2), position)
		if i < len(positions)-1 && (result.Completed || result.AutoCompleted) {
			t.Fatalf("completed before 60 seconds at report %d: %+v", i+1, result)
		}
		if i == len(positions)-1 && (!result.Completed || !result.AutoCompleted || result.EffectivePosition != 950) {
			t.Fatalf("60-second threshold result=%+v", result)
		}
	}

	var history struct {
		Items []json.RawMessage `json:"items"`
	}
	historyResponse := f.request(http.MethodGet, "/api/v1/user/history?limit=24&library_types=movie", "")
	if historyResponse.Code != http.StatusOK {
		t.Fatalf("history status=%d body=%s", historyResponse.Code, historyResponse.Body.String())
	}
	decodeAcceptanceJSON(t, historyResponse, &history)
	if len(history.Items) != 0 {
		t.Fatalf("completed video remained in continue watching: %s", historyResponse.Body.String())
	}

	var mediaList struct {
		Items []struct {
			ID        int64 `json:"id"`
			Completed int64 `json:"completed"`
		} `json:"items"`
	}
	listResponse := f.request(http.MethodGet, "/api/v1/media?library_id=1&file_type=video&limit=20", "")
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list media status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	decodeAcceptanceJSON(t, listResponse, &mediaList)
	foundCompleted := false
	for _, item := range mediaList.Items {
		if item.ID == 10 {
			foundCompleted = item.Completed == 1
		}
	}
	if !foundCompleted {
		t.Fatalf("ListMedia did not report current-user completion: %s", listResponse.Body.String())
	}

	f.start(11, "seek-session", 100)
	f.advance(10)
	seek := f.evidence(11, "seek-session", "seek", 2, 950)
	if seek.Completed || seek.AutoCompleted {
		t.Fatalf("seek completed video: %+v", seek)
	}
	f.advance(10)
	baseline := f.evidence(11, "seek-session", "progress", 3, 960)
	if baseline.Completed || baseline.AutoCompleted {
		t.Fatalf("first progress after seek completed video: %+v", baseline)
	}

	unwatch := f.request(http.MethodDelete, "/api/v1/media/10/watched", "")
	if unwatch.Code != http.StatusOK {
		t.Fatalf("DELETE watched status=%d body=%s", unwatch.Code, unwatch.Body.String())
	}
	var completed, sessions int
	if err := f.db.QueryRow(`SELECT completed FROM play_progress WHERE user_id=1 AND file_id='video-natural'`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM playback_completion_session WHERE user_id=1 AND file_id='video-natural'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if completed != 0 || sessions != 0 {
		t.Fatalf("after DELETE watched completed=%d sessions=%d", completed, sessions)
	}
	f.advance(10)
	oldSession := f.evidence(10, "natural-session", "progress", 8, 960)
	if oldSession.Completed || !oldSession.Stale {
		t.Fatalf("retired session changed unwatched state: %+v", oldSession)
	}

	f.start(12, "music-session", 850)
	for i, position := range positions {
		f.advance(10)
		result := f.evidence(12, "music-session", "progress", int64(i+2), position)
		if result.Completed || result.AutoCompleted {
			t.Fatalf("music auto-completed at report %d: %+v", i+1, result)
		}
	}
}
