package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/jit/session"
	"knox-media/internal/store"
)

func TestContainerMimeType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"matroska,webm", "video/webm"},
		{"webm", "video/webm"},
		{"matroska", "video/x-matroska"},
		{"mov,mp4,m4a", "video/mp4"},
		{"isom+mp4", "video/mp4"},
		{"ogg", "video/ogg"},
	}
	for _, tc := range cases {
		if got := containerMimeType(tc.in); got != tc.want {
			t.Fatalf("containerMimeType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDirectPlayContainerNeeds(t *testing.T) {
	cases := []struct {
		in       string
		wantNeed string
		wantOK   bool
	}{
		{"mov,mp4,m4a", "mp4", true},
		{"isom+mp4", "mp4", true},
		{"matroska", "mkv", true},
		{"matroska,webm", "webm", true},
		{"webm", "webm", true},
		{"ogg", "ogg", true},
		{"ogv", "ogg", true},
		{"flv", "flv", true},
		{"mpegts", "", false},
		{"", "", true},
		{"dash,cenc", "", true},
	}
	for _, tc := range cases {
		gotNeed, gotOK := directPlayContainerNeeds(tc.in)
		if gotOK != tc.wantOK || gotNeed != tc.wantNeed {
			t.Fatalf("directPlayContainerNeeds(%q) = (%q,%v), want (%q,%v)", tc.in, gotNeed, gotOK, tc.wantNeed, tc.wantOK)
		}
	}
}

func TestHLSInfoFlvNativeWhenClientSupportsFlv(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()

	dbPath := filepath.Join(base, "play-flv-native.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	meta := `{"format":{"format_name":"flv"},"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}`
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height, width, duration) VALUES (1, 1, 'f-1', 'E:/videos/a.flv', ?, 720, 1280, 300)`, meta); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	sm, err := session.NewManager("ffmpeg", "ffprobe", base, "", nil, nil)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, SessionManager: sm, runningScans: map[int64]scanRuntime{}}

	q := url.Values{}
	q.Set("video_codecs", "h264,h265")
	q.Set("audio_codecs", "aac,mp3")
	q.Set("max_height", "720")
	q.Set("qualities", "360p,480p,720p")
	q.Set("containers", "mp4,flv")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?"+q.Encode(), nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"native"`) {
		t.Fatalf("expected native for flv/h264/aac when client lists flv, got: %s", body)
	}
	if !contains(body, `"video/x-flv"`) {
		t.Fatalf("expected video/x-flv mime in native plan, got: %s", body)
	}
}

func TestHLSInfoFlvNotNativeWhenClientOmitsFlvContainer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()

	dbPath := filepath.Join(base, "play-flv-no-cap.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	meta := `{"format":{"format_name":"flv"},"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}`
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height, width, duration) VALUES (1, 1, 'f-1', 'E:/videos/a.flv', ?, 720, 1280, 300)`, meta); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	sm, err := session.NewManager("ffmpeg", "ffprobe", base, "", nil, nil)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, SessionManager: sm, runningScans: map[int64]scanRuntime{}}

	q := url.Values{}
	q.Set("video_codecs", "h264")
	q.Set("audio_codecs", "aac")
	q.Set("max_height", "720")
	q.Set("qualities", "360p,480p,720p")
	q.Set("containers", "mp4,webm")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?"+q.Encode(), nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"jit_hls"`) {
		t.Fatalf("expected jit_hls when client omits flv for flv source, got: %s", body)
	}
	if contains(body, `"mode":"native"`) {
		t.Fatalf("should not return native when client lacks flv container support: %s", body)
	}
}

func TestHLSInfoMkvNativeWhenClientListsMkvContainer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()

	dbPath := filepath.Join(base, "play-mkv-native-containers.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height) VALUES (1, 1, 'f-1', 'E:/videos/a.mkv', '{"format":{"format_name":"matroska"},"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	sm, err := session.NewManager("ffmpeg", "ffprobe", base, "", nil, nil)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, SessionManager: sm, runningScans: map[int64]scanRuntime{}}

	q := url.Values{}
	q.Set("video_codecs", "h264")
	q.Set("audio_codecs", "aac")
	q.Set("max_height", "1080")
	q.Set("qualities", "360p,480p,720p,1080p")
	q.Set("containers", "mp4,mkv,webm")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?"+q.Encode(), nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"native"`) {
		t.Fatalf("expected native for mkv/h264 when client lists mkv in containers, got: %s", body)
	}
}

func TestHLSInfoWebmNativeWhenFfprobeMatroskaWebmAndClientListsWebm(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()

	dbPath := filepath.Join(base, "play-webm-matroska-webm-format.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	meta := `{"format":{"format_name":"matroska,webm"},"streams":[{"codec_type":"video","codec_name":"vp9"},{"codec_type":"audio","codec_name":"opus"}]}`
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height) VALUES (1, 1, 'f-1', 'E:/videos/a.webm', ?, 720)`, meta); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	sm, err := session.NewManager("ffmpeg", "ffprobe", base, "", nil, nil)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, SessionManager: sm, runningScans: map[int64]scanRuntime{}}

	q := url.Values{}
	q.Set("video_codecs", "vp9")
	q.Set("audio_codecs", "opus")
	q.Set("max_height", "720")
	q.Set("qualities", "360p,480p,720p")
	q.Set("containers", "mp4,webm,ogg")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?"+q.Encode(), nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"native"`) {
		t.Fatalf("expected native for matroska,webm + vp9 when client lists webm (no mkv), got: %s", body)
	}
}

func TestHLSInfoMkvNotNativeWhenClientContainersExcludeMkv(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()

	dbPath := filepath.Join(base, "play-mkv-no-mkv-cap.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height, width, duration) VALUES (1, 1, 'f-1', 'E:/videos/a.mkv', '{"format":{"format_name":"matroska"},"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}', 1080, 1920, 300)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	sm, err := session.NewManager("ffmpeg", "ffprobe", base, "", nil, nil)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, SessionManager: sm, runningScans: map[int64]scanRuntime{}}

	q := url.Values{}
	q.Set("video_codecs", "h264")
	q.Set("audio_codecs", "aac")
	q.Set("max_height", "720")
	q.Set("qualities", "360p,480p,720p")
	q.Set("containers", "mp4,webm")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?"+q.Encode(), nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"jit_hls"`) {
		t.Fatalf("expected jit_hls when client omits mkv for matroska source, got: %s", body)
	}
	if contains(body, `"mode":"native"`) {
		t.Fatalf("should not return native when client lacks mkv container support: %s", body)
	}
}

func TestHLSInfoForcesJITWhenSourceExceedsHomeStreamQuality(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()

	dbPath := filepath.Join(base, "play-home-stream-quality.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height, width, duration, bitrate) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4', '{"format":{"format_name":"mov,mp4,m4a"},"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}', 2160, 3840, 300, 50000000)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	opts := `{"playback":{"home_stream_quality":"1080p-30mbps","screen_orientation":"auto"},"transcoder":{"quality":"auto","disable_video_stream_transcoding":false}}`
	if _, err := db.Exec(`UPDATE system_options SET options_json = ? WHERE id = 1`, opts); err != nil {
		t.Fatalf("update system options: %v", err)
	}

	sm, err := session.NewManager("ffmpeg", "ffprobe", base, "", nil, nil)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, SessionManager: sm, runningScans: map[int64]scanRuntime{}}

	q := url.Values{}
	q.Set("video_codecs", "h264")
	q.Set("audio_codecs", "aac")
	q.Set("max_height", "2160")
	q.Set("qualities", "360p,480p,720p,1080p,2160p")
	q.Set("containers", "mp4,mkv,webm")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?"+q.Encode(), nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"jit_hls"`) {
		t.Fatalf("expected jit_hls when 4K source exceeds 1080p-30mbps cap, got: %s", body)
	}
	if contains(body, `"mode":"native"`) {
		t.Fatalf("should not return native when source exceeds home stream quality: %s", body)
	}
}
