package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	models "knox-media/internal/model"
)

func TestGenerateMasterPlaylist_SingleVariantMatchesSource1080(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, _ := newTestScheduler(t)

	fileID := "f-master"
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jit/master/"+fileID, nil)
	c.Request = req

	out := s.generateMasterPlaylist(c, fileID, &models.VideoMetadata{
		Height:     1080,
		Codec:      "h264",
		AudioCodec: "aac",
	})

	if !strings.Contains(out, "1920x1080") {
		t.Fatalf("expected 1080p single variant in master\n%s", out)
	}
	if strings.Contains(out, "3840x2160") {
		t.Fatalf("did not expect 4K variant for 1080p source\n%s", out)
	}
	if strings.Count(out, "#EXT-X-STREAM-INF") != 1 {
		t.Fatalf("expected exactly one STREAM-INF for single-quality master\n%s", out)
	}
	// 单清晰度 + 内嵌音频：不应再渲染 TYPE=AUDIO 媒体行
	if strings.Contains(out, "TYPE=AUDIO") {
		t.Fatalf("single-quality master should embed audio in TS, not declare AUDIO group\n%s", out)
	}
}

func TestGenerateMasterPlaylist_RespectsClientMaxHeight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, _ := newTestScheduler(t)

	fileID := "f-master-2"
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jit/master/"+fileID+"?max_height=720", nil)
	c.Request = req

	out := s.generateMasterPlaylist(c, fileID, &models.VideoMetadata{
		Height:     2160,
		Codec:      "h264",
		AudioCodec: "aac",
	})

	if strings.Contains(out, "1920x1080") || strings.Contains(out, "3840x2160") {
		t.Fatalf("client max_height=720 ignored\n%s", out)
	}
	if !strings.Contains(out, "1280x720") {
		t.Fatalf("expected 720p ladder for max_height=720\n%s", out)
	}
}

func TestEndSession_ClearsRedisKeys(t *testing.T) {
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	sessionID := "test-end"
	if err := rdb.HSet(ctx, "session:"+sessionID, "file_id", "f1").Err(); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := rdb.Set(ctx, "jit:session_seek_boost:"+sessionID, "1", 0).Err(); err != nil {
		t.Fatalf("seed boost: %v", err)
	}

	s.EndSession(sessionID)

	if n, _ := rdb.Exists(ctx, "session:"+sessionID).Result(); n != 0 {
		t.Fatalf("session key still exists after EndSession")
	}
	if n, _ := rdb.Exists(ctx, "jit:session_seek_boost:"+sessionID).Result(); n != 0 {
		t.Fatalf("seek boost key still exists after EndSession")
	}
}

func TestHandleJITSessionEnd_RemovesSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()
	if err := rdb.HSet(ctx, "session:end-1", "file_id", "f").Err(); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jit/session/end", nil)
	req.Header.Set("X-Session-ID", "end-1")
	c.Request = req
	s.handleJITSessionEnd(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if n, _ := rdb.Exists(ctx, "session:end-1").Result(); n != 0 {
		t.Fatalf("session not deleted")
	}
}

func TestHandleJITSessionSeek_SetsBoostAndResumes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()
	if err := rdb.HSet(ctx, "session:seek-1", "transcode_paused", "1").Err(); err != nil {
		t.Fatalf("seed paused: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jit/session/seek", nil)
	req.Header.Set("X-Session-ID", "seek-1")
	c.Request = req
	s.handleJITSessionSeek(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if n, _ := rdb.Exists(ctx, "jit:session_seek_boost:seek-1").Result(); n == 0 {
		t.Fatalf("expected seek boost key set")
	}
	v, _ := rdb.HGet(ctx, "session:seek-1", "transcode_paused").Result()
	if v != "0" {
		t.Fatalf("expected transcode_paused=0 after seek, got %q", v)
	}
}

func TestHandleVideoPlaylist_UsesMaxSegmentForTargetDuration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	fileID := "f-pl"
	idx := models.SegmentIndex{
		FileID: fileID,
		Status: "ready",
		VideoSegments: []models.VideoSegmentInfo{
			{ID: 0, Duration: 6.0, Status: "indexed"},
			{ID: 1, Duration: 8.4, Status: "indexed"},
			{ID: 2, Duration: 4.1, Status: "indexed"},
		},
	}
	raw, _ := json.Marshal(idx)
	_ = rdb.Set(ctx, "video:index:"+fileID, raw, 0).Err()
	// Variant playlist 现在等 video:meta status=ready 才返回
	_ = rdb.HSet(ctx, "video:meta:"+fileID, "status", "ready", "file_path", "/tmp/x.mp4").Err()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jit/playlist/"+fileID+"/video/2000k", nil)
	c.Request = req
	c.Params = gin.Params{{Key: "fileId", Value: fileID}, {Key: "bitrate", Value: "2000k"}}
	s.handleVideoPlaylist(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "#EXT-X-TARGETDURATION:9") {
		t.Fatalf("expected target duration 9 (ceil 8.4), got\n%s", body)
	}
	if strings.Count(body, "#EXTINF") != 3 {
		t.Fatalf("expected 3 EXTINF lines, got\n%s", body)
	}
}
