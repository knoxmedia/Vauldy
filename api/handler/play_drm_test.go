package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/jit/session"
	"knox-media/internal/store"
)

func TestHLSInfoReturnsDRMFieldsForPackagedMedia(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "play-drm.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height) VALUES (1, 1, 'f-1', 'E:/videos/a.mkv', '{"format":{"format_name":"matroska"},"streams":[{"codec_type":"video","codec_name":"h264"}]}', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, output_path) VALUES (1, 'cmaf_drm', 'done', 'E:/transcode/1/master.m3u8')`); err != nil {
		t.Fatalf("insert package task: %v", err)
	}

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !containsAll(body,
		`"mode":"hls_drm"`,
		`"status":"done"`,
		`"hls_master":"http://example.com/api/v1/media/1/hls/master.m3u8"`,
		`"widevine_license_url":"http://example.com/api/v1/drm/widevine/license"`,
		`"fairplay_cert_url":"http://example.com/api/v1/drm/fairplay/cert"`,
		`"fairplay_license_url":"http://example.com/api/v1/drm/fairplay/license"`,
	) {
		t.Fatalf("unexpected body: %s", body)
	}
	if strings.Contains(body, "widevine_service_cert_url") {
		t.Fatalf("did not expect widevine_service_cert_url without private module: %s", body)
	}
}

func TestHLSInfoOmitsWidevineServiceCertWhenEmitDisabledWithPrivateModule(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "play-drm-sc.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height) VALUES (1, 1, 'f-1', 'E:/videos/a.mkv', '{"format":{"format_name":"matroska"},"streams":[{"codec_type":"video","codec_name":"h264"}]}', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, output_path) VALUES (1, 'cmaf_drm', 'done', 'E:/transcode/1/master.m3u8')`); err != nil {
		t.Fatalf("insert package task: %v", err)
	}

	cfg := &config.Config{}
	cfg.DRM.Widevine.PrivateModuleURL = "http://127.0.0.1:8080/license"
	// emit_service_cert_url defaults false: no URL in plan even with private module.
	h := &Handler{App: &app.App{DB: db, Config: cfg}, runningScans: map[int64]scanRuntime{}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "widevine_service_cert_url") {
		t.Fatalf("did not expect widevine_service_cert_url when emit_service_cert_url is false: %s", body)
	}
	if !containsAll(body, `"widevine_transport":"raw"`) {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestHLSInfoIncludesWidevineServiceCertWhenEmitFlagAndPrivateModule(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "play-drm-sc-emit.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height) VALUES (1, 1, 'f-1', 'E:/videos/a.mkv', '{"format":{"format_name":"matroska"},"streams":[{"codec_type":"video","codec_name":"h264"}]}', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, output_path) VALUES (1, 'cmaf_drm', 'done', 'E:/transcode/1/master.m3u8')`); err != nil {
		t.Fatalf("insert package task: %v", err)
	}

	cfg := &config.Config{}
	cfg.DRM.Widevine.PrivateModuleURL = "http://127.0.0.1:8080/license"
	cfg.DRM.Widevine.EmitServiceCertURL = true
	h := &Handler{App: &app.App{DB: db, Config: cfg}, runningScans: map[int64]scanRuntime{}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !containsAll(body,
		`"widevine_service_cert_url":"http://example.com/api/v1/drm/widevine/service-cert"`,
		`"widevine_transport":"raw"`,
	) {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestHLSInfoPrefersCompletedTranscodeOverNative(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	dbPath := filepath.Join(base, "play-prefer-transcode.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4', '{"format":{"format_name":"mov,mp4,m4a"},"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	master := filepath.Join(base, "master.m3u8")
	if err := os.WriteFile(master, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatalf("write master: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO transcode_task (file_id, quality, status, output_path) VALUES ('f-1', 'abr:1080p', 'done', ?)`, master); err != nil {
		t.Fatalf("insert transcode task: %v", err)
	}

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"hls"`) || !contains(body, `"status":"done"`) {
		t.Fatalf("expected hls done preferred, got: %s", body)
	}
	if contains(body, `"mode":"native"`) {
		t.Fatalf("should not fall back to native when transcode done: %s", body)
	}
}

func TestHLSMasterServesDRMManifestWhenDRMReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	dbPath := filepath.Join(base, "play-master-drm.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mkv')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	drmMaster := filepath.Join(base, "drm-master.m3u8")
	if err := os.WriteFile(drmMaster, []byte("#EXTM3U\n# DRM\n"), 0o644); err != nil {
		t.Fatalf("write drm master: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, output_path) VALUES (1, 'cmaf_drm', 'done', ?)`, drmMaster); err != nil {
		t.Fatalf("insert package task: %v", err)
	}

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls/master.m3u8", nil)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.HLSMaster(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "# DRM") {
		t.Fatalf("expected served drm master content, got: %s", w.Body.String())
	}
}

func TestStripAudioGroupFromMasterM3U8(t *testing.T) {
	in := `#EXTM3U
#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES-CTR,URI="data:text/plain;base64,AAA",KEYFORMAT="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"
#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES-CTR,URI="skd://ABC",KEYFORMAT="com.apple.streamingkeydelivery"
#EXT-X-MEDIA:TYPE=AUDIO,URI="audio.m3u8",GROUP-ID="aud1",NAME="default"
#EXT-X-STREAM-INF:BANDWIDTH=1128000,CODECS="avc1.64000d,mp4a.40.2",RESOLUTION=270x360,AUDIO="aud1"
360p.m3u8
`
	out := stripAudioGroupFromMasterM3U8(in)
	if contains(out, "TYPE=AUDIO") {
		t.Fatalf("audio media line should be removed: %s", out)
	}
	if contains(out, `AUDIO="aud1"`) {
		t.Fatalf("audio attr should be removed: %s", out)
	}
	if contains(out, "mp4a.40.2") {
		t.Fatalf("audio codec should be removed: %s", out)
	}
}

func TestPlayMediaRedirectsToHLSMasterWhenDRMReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	dbPath := filepath.Join(base, "play-redirect-drm.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	drmMaster := filepath.Join(base, "drm-master.m3u8")
	if err := os.WriteFile(drmMaster, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatalf("write drm master: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, output_path) VALUES (1, 'cmaf_drm', 'done', ?)`, drmMaster); err != nil {
		t.Fatalf("insert package task: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/play?access_token=tkn", nil)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.PlayMedia(c)
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc != "/api/v1/media/1/hls/master.m3u8?access_token=tkn" {
		t.Fatalf("unexpected location=%s", loc)
	}
}

func TestPlayMediaPreferSourceBypassesRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	dbPath := filepath.Join(base, "play-prefer-source.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	source := filepath.Join(base, "source.mp4")
	if err := os.WriteFile(source, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', ?)`, source); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	drmMaster := filepath.Join(base, "drm-master.m3u8")
	if err := os.WriteFile(drmMaster, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatalf("write drm master: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, output_path) VALUES (1, 'cmaf_drm', 'done', ?)`, drmMaster); err != nil {
		t.Fatalf("insert package task: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/play?prefer_source=1&access_token=tkn", nil)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.PlayMedia(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHLSMasterInjectsAccessTokenIntoVariantPlaylist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	dbPath := filepath.Join(base, "play-master-token.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	master := filepath.Join(base, "master.m3u8")
	content := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1200000\n720p.m3u8\n"
	if err := os.WriteFile(master, []byte(content), 0o644); err != nil {
		t.Fatalf("write master: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO transcode_task (file_id, quality, status, output_path) VALUES ('f-1', 'abr:720p', 'done', ?)`, master); err != nil {
		t.Fatalf("insert transcode: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls/master.m3u8?access_token=tkn", nil)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.HLSMaster(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "720p.m3u8?access_token=tkn") {
		t.Fatalf("token not injected in playlist: %s", w.Body.String())
	}
}

func TestHLSAssetInitSegmentFallbackFromWorkingDir(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()
	dbPath := filepath.Join(base, "play-init-fallback.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	master := filepath.Join(base, "master.m3u8")
	if err := os.WriteFile(master, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatalf("write master: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	altInit := filepath.Join(cwd, "720p_init.mp4")
	if err := os.WriteFile(altInit, []byte("init"), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(altInit) })
	if _, err := db.Exec(`INSERT INTO transcode_task (file_id, quality, status, output_path) VALUES ('f-1', 'abr:720p', 'done', ?)`, master); err != nil {
		t.Fatalf("insert transcode: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls/720p_init.mp4", nil)
	c.Request = req
	c.Params = gin.Params{
		{Key: "id", Value: "1"},
		{Key: "asset", Value: "/720p_init.mp4"},
	}
	h.HLSAsset(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHLSInfoNativeWhenClientSupportsMP4NoTranscode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "play-native-no-transcode.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4', '{"format":{"format_name":"mov,mp4,m4a"},"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	h := &Handler{App: &app.App{DB: db}, runningScans: map[int64]scanRuntime{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?video_codecs=h264&audio_codecs=aac&max_height=1080&qualities=360p,480p,720p,1080p", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"native"`) {
		t.Fatalf("expected native mode for mp4/h264 source supported by client, got: %s", body)
	}
	if !contains(body, `"mime_type":"video/mp4"`) {
		t.Fatalf("expected mime_type video/mp4 in native response, got: %s", body)
	}
}

func TestHLSInfoNativeSkipJITWhenClientSupportsSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()

	dbPath := filepath.Join(base, "play-native-skip-jit.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height) VALUES (1, 1, 'f-1', 'E:/videos/a.mp4', '{"format":{"format_name":"mov,mp4,m4a"},"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}', 1080)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	sm, err := session.NewManager("ffmpeg", "ffprobe", base, "", nil, nil)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, SessionManager: sm, runningScans: map[int64]scanRuntime{}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?video_codecs=h264&audio_codecs=aac&max_height=1080&qualities=360p,480p,720p,1080p", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"native"`) {
		t.Fatalf("expected native mode (canDirectPlay skips JIT), got: %s", body)
	}
	if contains(body, `"mode":"jit_hls"`) {
		t.Fatalf("should not create JIT session when client supports source directly: %s", body)
	}
}

func TestHLSInfoJITWhenClientCannotPlaySource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()

	dbPath := filepath.Join(base, "play-jit-unsupported.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (1, 'lib', 'movie', 'E:/videos')`); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	// Source is mkv/vp9, client only supports h264
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height, width, duration) VALUES (1, 1, 'f-1', 'E:/videos/a.mkv', '{"format":{"format_name":"matroska"},"streams":[{"codec_type":"video","codec_name":"vp9"},{"codec_type":"audio","codec_name":"opus"}]}', 720, 1280, 300)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	sm, err := session.NewManager("ffmpeg", "ffprobe", base, "", nil, nil)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	h := &Handler{App: &app.App{DB: db}, SessionManager: sm, runningScans: map[int64]scanRuntime{}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?video_codecs=h264&audio_codecs=aac&max_height=720&qualities=360p,480p,720p", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"mode":"jit_hls"`) {
		t.Fatalf("expected jit_hls when client cannot decode source (mkv/vp9), got: %s", body)
	}
	if contains(body, `"mode":"native"`) {
		t.Fatalf("should not return native for unsupported source: %s", body)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func TestHLSInfoStreamDRMReturnsJITSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := t.TempDir()

	dbPath := filepath.Join(base, "play-stream-drm-jit.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path, drm_enabled, encryption_mode) VALUES (1, 'lib', 'movie', ?, 1, 'standard')`, base); err != nil {
		t.Fatalf("insert library: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO media (id, library_id, file_id, file_path, meta_json, height, width, duration) VALUES (1, 1, 'f-1', ?, '{"format":{"format_name":"mov,mp4,m4a"},"streams":[{"codec_type":"video","codec_name":"h264"},{"codec_type":"audio","codec_name":"aac"}]}', 1080, 1920, 120)`, filepath.Join(base, "a.mp4")); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	sm, err := session.NewManager("ffmpeg", "ffprobe", base, "", nil, nil)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	cfg := &config.Config{}
	cfg.Data.Dir = base
	h := &Handler{
		App:            &app.App{DB: db, Config: cfg},
		SessionManager: sm,
		runningScans:   map[int64]scanRuntime{},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/media/1/hls?video_codecs=h264&audio_codecs=aac&max_height=1080", nil)
	req.Host = "example.com"
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "1"}}

	h.HLSInfo(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !containsAll(body,
		`"mode":"hls_aes_128"`,
		`"stream_drm":true`,
		`"session_id":"jit-`,
		`/api/v1/jit/session/`,
		`/master.m3u8`,
	) {
		t.Fatalf("unexpected stream drm jit plan: %s", body)
	}
	if contains(body, `/api/v1/media/1/hls/master.m3u8`) {
		t.Fatalf("expected jit master not batch package path: %s", body)
	}
	var keyCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM drm_key_material WHERE media_id = 1 AND mode = 'stream_jit_aes128'`).Scan(&keyCount); err != nil {
		t.Fatalf("query drm_key_material: %v", err)
	}
	if keyCount != 1 {
		t.Fatalf("expected stream jit key material persisted, got %d", keyCount)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
