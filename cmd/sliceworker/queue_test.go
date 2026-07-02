package sliceworker

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"

	models "knox-media/internal/model"
)

func newTestSliceWorker(t *testing.T) (*SliceWorker, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &SliceWorker{
		redis:    rdb,
		ffmpeg:   "/bin/true",
		ffprobe:  "/bin/true",
		logger:   zap.NewNop(),
		workerID: "test-worker",
	}, rdb
}

func TestRecoverStaleSlicing_ReenqueuesAfterTimeout(t *testing.T) {
	w, rdb := newTestSliceWorker(t)
	ctx := context.Background()

	fileID := "f-stale-recover"
	old := time.Now().Add(-2*SliceStaleAfter - time.Second).Unix()
	if err := rdb.HSet(ctx, "video:meta:"+fileID,
		"status", "slicing",
		"file_path", "/tmp/x.mp4",
		"slicing_started_at", strconv.FormatInt(old, 10),
		"slicing_heartbeat_at", strconv.FormatInt(old, 10),
	).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := rdb.Set(ctx, "lock:slice:"+fileID, "stale-owner", time.Hour).Err(); err != nil {
		t.Fatalf("seed lock: %v", err)
	}

	w.scanAndRecoverOnce(ctx)

	// Status should be cleared so a future ensureVideoSliced can restart cleanly.
	st, _ := rdb.HGet(ctx, "video:meta:"+fileID, "status").Result()
	if st == "slicing" {
		t.Fatalf("expected status cleared, got %q", st)
	}
	if n, _ := rdb.Exists(ctx, "lock:slice:"+fileID).Result(); n != 0 {
		t.Fatalf("expected lock cleared")
	}
	llen, err := rdb.LLen(ctx, SliceQueueKey).Result()
	if err != nil {
		t.Fatalf("llen: %v", err)
	}
	if llen != 1 {
		t.Fatalf("expected 1 task pushed to %s, got %d", SliceQueueKey, llen)
	}
	raw, err := rdb.LIndex(ctx, SliceQueueKey, 0).Result()
	if err != nil {
		t.Fatalf("lindex: %v", err)
	}
	var task models.SliceTask
	if err := json.Unmarshal([]byte(raw), &task); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if task.FileID != fileID {
		t.Fatalf("task.FileID=%q, want %q", task.FileID, fileID)
	}
}

func TestRecoverStaleSlicing_LeavesFreshAlone(t *testing.T) {
	w, rdb := newTestSliceWorker(t)
	ctx := context.Background()

	fileID := "f-fresh-keep"
	now := time.Now().Unix()
	_ = rdb.HSet(ctx, "video:meta:"+fileID,
		"status", "slicing",
		"file_path", "/tmp/x.mp4",
		"slicing_started_at", strconv.FormatInt(now, 10),
		"slicing_heartbeat_at", strconv.FormatInt(now, 10),
	).Err()

	w.scanAndRecoverOnce(ctx)

	st, _ := rdb.HGet(ctx, "video:meta:"+fileID, "status").Result()
	if st != "slicing" {
		t.Fatalf("expected status preserved=slicing, got %q", st)
	}
	llen, _ := rdb.LLen(ctx, SliceQueueKey).Result()
	if llen != 0 {
		t.Fatalf("did not expect re-enqueue for fresh task, got %d", llen)
	}
}

func TestParseInt_HandlesBadInput(t *testing.T) {
	cases := []struct {
		in   interface{}
		want int64
	}{
		{nil, 0},
		{"", 0},
		{"abc", 0},
		{"42", 42},
	}
	for _, c := range cases {
		if got := parseInt(c.in); got != c.want {
			t.Fatalf("parseInt(%v)=%d, want %d", c.in, got, c.want)
		}
	}
}
