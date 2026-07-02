package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"

	models "knox-media/internal/model"
)

func newTestScheduler(t *testing.T) (*Scheduler, *redis.Client, string) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	base := t.TempDir()
	s := NewScheduler(rdb, NewLocalStorage(base))
	return s, rdb, base
}

func TestHandleVideoSegment_PrefersExistingPreheatedSegment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, rdb, base := newTestScheduler(t)

	fileID := "file-preheat"
	bitrate := "2000k"
	body := []byte("preheated-ts")
	tsPath := filepath.Join(base, "ts", "video", fileID, bitrate, "0.ts")
	if err := os.MkdirAll(filepath.Dir(tsPath), 0o755); err != nil {
		t.Fatalf("mkdir ts dir: %v", err)
	}
	if err := os.WriteFile(tsPath, body, 0o644); err != nil {
		t.Fatalf("write ts: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jit/segment/"+fileID+"/video/"+bitrate+"/0", nil)
	req.Header.Set("X-Session-ID", "play-s1")
	c.Request = req
	c.Params = gin.Params{
		{Key: "fileId", Value: fileID},
		{Key: "bitrate", Value: bitrate},
		{Key: "segId", Value: "0"},
	}

	s.handleVideoSegment(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if string(w.Body.Bytes()) != string(body) {
		t.Fatalf("unexpected body=%q", w.Body.String())
	}

	nHigh, err := rdb.ZCard(context.Background(), "transcode:queue:high").Result()
	if err != nil {
		t.Fatalf("read queue size: %v", err)
	}
	if nHigh != 0 {
		t.Fatalf("expected no transcode enqueue when segment already exists, got %d", nHigh)
	}
}

func TestHandleVideoSegment_SeekFallsBackToPreviewAndEnqueuesHD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, rdb, base := newTestScheduler(t)
	ctx := context.Background()

	fileID := "file-seek"
	seekSession := "seek-s1"
	requestBitrate := "4000k"
	segID := 5

	index := models.SegmentIndex{
		FileID:    fileID,
		Status:    "ready",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		VideoSegments: []models.VideoSegmentInfo{
			{ID: 0, Status: "indexed", Duration: 6},
			{ID: 1, Status: "indexed", Duration: 6},
			{ID: 2, Status: "indexed", Duration: 6},
			{ID: 3, Status: "indexed", Duration: 6},
			{ID: 4, Status: "indexed", Duration: 6},
			{ID: 5, Status: "indexed", Duration: 6},
		},
	}
	rawIdx, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if err := rdb.Set(ctx, "video:index:"+fileID, rawIdx, 0).Err(); err != nil {
		t.Fatalf("set index: %v", err)
	}

	// Seed previous segment so this request is treated as a seek jump and enables seek boost.
	if err := rdb.HSet(ctx, "session:"+seekSession, "current_segment", "0").Err(); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	fallbackBody := []byte("fallback-500k-ts")
	fallbackPath := filepath.Join(base, "ts", "video", fileID, "500k", "5.ts")
	if err := os.MkdirAll(filepath.Dir(fallbackPath), 0o755); err != nil {
		t.Fatalf("mkdir fallback dir: %v", err)
	}
	if err := os.WriteFile(fallbackPath, fallbackBody, 0o644); err != nil {
		t.Fatalf("write fallback ts: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jit/segment/"+fileID+"/video/"+requestBitrate+"/5", nil)
	req.Header.Set("X-Session-ID", seekSession)
	c.Request = req
	c.Params = gin.Params{
		{Key: "fileId", Value: fileID},
		{Key: "bitrate", Value: requestBitrate},
		{Key: "segId", Value: "5"},
	}

	start := time.Now()
	s.handleVideoSegment(c)
	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-JIT-Substituted-Bitrate"); got != "500k" {
		t.Fatalf("substituted bitrate=%q, want 500k", got)
	}
	if got := w.Header().Get("X-JIT-Requested-Bitrate"); got != requestBitrate {
		t.Fatalf("requested bitrate header=%q, want %s", got, requestBitrate)
	}
	if got := string(w.Body.Bytes()); got != string(fallbackBody) {
		t.Fatalf("fallback body=%q, want %q", got, string(fallbackBody))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("seek fallback took too long: %s", elapsed)
	}

	boostExists, err := rdb.Exists(ctx, "jit:session_seek_boost:"+seekSession).Result()
	if err != nil {
		t.Fatalf("read seek boost key: %v", err)
	}
	if boostExists == 0 {
		t.Fatalf("seek boost key not set")
	}

	members, err := rdb.ZRange(ctx, "transcode:queue:high", 0, -1).Result()
	if err != nil {
		t.Fatalf("read high queue: %v", err)
	}
	if len(members) == 0 {
		t.Fatalf("expected on-demand HD transcode task to be enqueued")
	}
	foundHD := false
	for _, raw := range members {
		var task models.TranscodeTask
		if err := json.Unmarshal([]byte(raw), &task); err != nil {
			t.Fatalf("decode transcode task: %v", err)
		}
		if task.FileID == fileID && task.SegmentID == segID && task.Bitrate == requestBitrate {
			foundHD = true
			break
		}
	}
	if !foundHD {
		t.Fatalf("missing HD transcode task for seek segment")
	}
}

func TestStartTranscodeTask_PrefetchUsesLowQueue(t *testing.T) {
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	if err := s.startTranscodeTask("f-queue", 1, "2000k", "prefetch"); err != nil {
		t.Fatalf("start prefetch task: %v", err)
	}
	if err := s.startTranscodeTask("f-queue", 2, "2000k", "session-x"); err != nil {
		t.Fatalf("start user task: %v", err)
	}

	nLow, err := rdb.ZCard(ctx, "transcode:queue:low").Result()
	if err != nil {
		t.Fatalf("read low queue size: %v", err)
	}
	nHigh, err := rdb.ZCard(ctx, "transcode:queue:high").Result()
	if err != nil {
		t.Fatalf("read high queue size: %v", err)
	}
	if nLow != 1 {
		t.Fatalf("low queue size=%d, want 1", nLow)
	}
	if nHigh != 1 {
		t.Fatalf("high queue size=%d, want 1", nHigh)
	}
}

func TestJITSessionPauseResumeEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	sessionID := "pause-s1"
	if err := rdb.HSet(ctx, "session:"+sessionID, "file_id", "f1").Err(); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := rdb.Expire(ctx, "session:"+sessionID, 35*time.Second).Err(); err != nil {
		t.Fatalf("seed session ttl: %v", err)
	}

	w1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(w1)
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/jit/session/pause", nil)
	req1.Header.Set("X-Session-ID", sessionID)
	c1.Request = req1
	s.handleJITSessionPause(c1)
	if w1.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", w1.Code, w1.Body.String())
	}

	paused, err := rdb.HGet(ctx, "session:"+sessionID, "transcode_paused").Result()
	if err != nil {
		t.Fatalf("read paused flag: %v", err)
	}
	if paused != "1" {
		t.Fatalf("paused flag=%q, want 1", paused)
	}
	ttlPaused, err := rdb.TTL(ctx, "session:"+sessionID).Result()
	if err != nil {
		t.Fatalf("read paused ttl: %v", err)
	}
	if ttlPaused < time.Hour {
		t.Fatalf("paused ttl=%s, expected long keepalive", ttlPaused)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/jit/session/resume", nil)
	req2.Header.Set("X-Session-ID", sessionID)
	c2.Request = req2
	s.handleJITSessionResume(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", w2.Code, w2.Body.String())
	}

	resumed, err := rdb.HGet(ctx, "session:"+sessionID, "transcode_paused").Result()
	if err != nil {
		t.Fatalf("read resumed flag: %v", err)
	}
	if resumed != "0" {
		t.Fatalf("resumed flag=%q, want 0", resumed)
	}
	ttlResumed, err := rdb.TTL(ctx, "session:"+sessionID).Result()
	if err != nil {
		t.Fatalf("read resumed ttl: %v", err)
	}
	if ttlResumed <= 0 || ttlResumed > time.Minute {
		t.Fatalf("resumed ttl=%s, want around active session TTL", ttlResumed)
	}
}
