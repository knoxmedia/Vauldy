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

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"

	models "knox-media/internal/model"
)

func TestEndSession_RemovesArtifactsWhenLastSession(t *testing.T) {
	s, rdb, base := newTestScheduler(t)
	ctx := context.Background()

	fileID := "f-cleanup"
	tsPath := filepath.Join(base, "ts", "video", fileID, "2000k", "0.ts")
	if err := os.MkdirAll(filepath.Dir(tsPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(tsPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write ts: %v", err)
	}
	rawAudio := filepath.Join(base, "raw", "audio", fileID)
	if err := os.MkdirAll(rawAudio, 0o755); err != nil {
		t.Fatalf("mkdir audio: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rawAudio, "segment_00000.m4a"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	// 单一会话
	if err := rdb.HSet(ctx, "session:onlyone", "file_id", fileID).Err(); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := rdb.Set(ctx, "video:index:"+fileID, "{}", 0).Err(); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if err := rdb.HSet(ctx, "video:meta:"+fileID, "status", "ready").Err(); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	s.EndSession("onlyone")

	if _, err := os.Stat(tsPath); !os.IsNotExist(err) {
		t.Fatalf("expected ts file removed, stat err=%v", err)
	}
	if _, err := os.Stat(rawAudio); !os.IsNotExist(err) {
		t.Fatalf("expected raw audio dir removed, stat err=%v", err)
	}
	if n, _ := rdb.Exists(ctx, "video:index:"+fileID).Result(); n != 0 {
		t.Fatalf("video:index not deleted")
	}
	if n, _ := rdb.Exists(ctx, "video:meta:"+fileID).Result(); n != 0 {
		t.Fatalf("video:meta not deleted")
	}
}

func TestEndSession_KeepsArtifactsWhenOtherSessionActive(t *testing.T) {
	s, rdb, base := newTestScheduler(t)
	ctx := context.Background()

	fileID := "f-shared"
	tsPath := filepath.Join(base, "ts", "video", fileID, "2000k", "0.ts")
	_ = os.MkdirAll(filepath.Dir(tsPath), 0o755)
	if err := os.WriteFile(tsPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write ts: %v", err)
	}

	if err := rdb.HSet(ctx, "session:viewer-a", "file_id", fileID).Err(); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := rdb.HSet(ctx, "session:viewer-b", "file_id", fileID).Err(); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	s.EndSession("viewer-a")

	if _, err := os.Stat(tsPath); err != nil {
		t.Fatalf("ts should still exist for active viewer-b: %v", err)
	}
}

func TestDropStaleTranscodeTasksForFile_RemovesPrefetch(t *testing.T) {
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	enqueue := func(fid string, seg int) {
		task := models.TranscodeTask{FileID: fid, SegmentID: seg, Bitrate: "2000k", SessionID: "prefetch"}
		raw, _ := json.Marshal(task)
		_ = rdb.ZAdd(ctx, "transcode:queue:low", &redis.Z{Score: float64(seg), Member: string(raw)}).Err()
	}
	enqueue("f-stale", 1)
	enqueue("f-stale", 2)
	enqueue("f-other", 3)

	s.dropStaleTranscodeTasksForFile("f-stale")

	left, err := rdb.ZRange(ctx, "transcode:queue:low", 0, -1).Result()
	if err != nil {
		t.Fatalf("zrange: %v", err)
	}
	if len(left) != 1 {
		t.Fatalf("expected 1 leftover task for f-other, got %d", len(left))
	}
}

func TestSeekTriggersBoostFromSegmentJump(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, rdb, base := newTestScheduler(t)
	ctx := context.Background()

	fileID := "f-jump"
	// Pre-existing index (so the segment-id path finds metadata if needed). The segment file exists,
	// so handleVideoSegment serves it directly without spawning ffmpeg.
	idx := models.SegmentIndex{
		FileID:        fileID,
		Status:        "ready",
		VideoSegments: []models.VideoSegmentInfo{{ID: 10, Duration: 6, Status: "indexed"}},
	}
	raw, _ := json.Marshal(idx)
	_ = rdb.Set(ctx, "video:index:"+fileID, raw, 0).Err()

	body := []byte("seg-10")
	tsPath := filepath.Join(base, "ts", "video", fileID, "2000k", "10.ts")
	if err := os.MkdirAll(filepath.Dir(tsPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(tsPath, body, 0o644); err != nil {
		t.Fatalf("write ts: %v", err)
	}

	// 起始位置：current_segment=0
	if err := rdb.HSet(ctx, "session:jumper", "current_segment", "0", "file_id", fileID).Err(); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jit/segment/"+fileID+"/video/2000k/10", nil)
	req.Header.Set("X-Session-ID", "jumper")
	c.Request = req
	c.Params = gin.Params{
		{Key: "fileId", Value: fileID},
		{Key: "bitrate", Value: "2000k"},
		{Key: "segId", Value: "10"},
	}
	s.handleVideoSegment(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if n, _ := rdb.Exists(ctx, "jit:session_seek_boost:jumper").Result(); n == 0 {
		t.Fatalf("expected seek boost set after jump 0->10")
	}
}

func TestHandleMasterPlaylist_ReturnsImmediatelyWithoutSlicing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	fileID := "f-master-fast"
	if err := rdb.HSet(ctx, "video:meta:"+fileID, "file_path", "/tmp/x.mp4", "height", "1080", "duration", "120").Err(); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jit/master/"+fileID, nil)
	c.Request = req
	c.Params = gin.Params{{Key: "fileId", Value: fileID}}

	start := time.Now()
	s.handleMasterPlaylist(c)
	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("master status=%d body=%s", w.Code, w.Body.String())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("master took %v; expected near-instant", elapsed)
	}
}
