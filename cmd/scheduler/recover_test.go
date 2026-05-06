package scheduler

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestSliceLooksStale_NoHeartbeatIsStale(t *testing.T) {
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	fileID := "f-stale"
	old := time.Now().Add(-2 * sliceStaleAfter).Unix()
	if err := rdb.HSet(ctx, "video:meta:"+fileID,
		"status", "slicing",
		"slicing_started_at", strconv.FormatInt(old, 10),
		"slicing_heartbeat_at", strconv.FormatInt(old, 10),
	).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if !s.sliceLooksStale(fileID) {
		t.Fatalf("expected stale=true for %ds-old heartbeat", int(time.Since(time.Unix(old, 0)).Seconds()))
	}
}

func TestSliceLooksStale_FreshHeartbeatNotStale(t *testing.T) {
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	fileID := "f-fresh"
	now := time.Now().Unix()
	_ = rdb.HSet(ctx, "video:meta:"+fileID,
		"status", "slicing",
		"slicing_started_at", strconv.FormatInt(now, 10),
		"slicing_heartbeat_at", strconv.FormatInt(now, 10),
	).Err()

	if s.sliceLooksStale(fileID) {
		t.Fatalf("expected stale=false for fresh heartbeat")
	}
}

func TestForceReenqueueSlicing_PushesToDurableQueue(t *testing.T) {
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	fileID := "f-reenqueue"
	tmp := t.TempDir()
	srcPath := tmp + "/src.mp4"
	if err := writeDummyFile(srcPath); err != nil {
		t.Fatalf("write src: %v", err)
	}

	// 模拟僵死状态：lock 还在、status=slicing、心跳很久之前。
	old := time.Now().Add(-5 * time.Minute).Unix()
	_ = rdb.HSet(ctx, "video:meta:"+fileID,
		"status", "slicing",
		"file_path", srcPath,
		"slicing_started_at", strconv.FormatInt(old, 10),
		"slicing_heartbeat_at", strconv.FormatInt(old, 10),
	).Err()
	_ = rdb.Set(ctx, "lock:slice:"+fileID, "stale-owner", time.Hour).Err()

	s.forceReenqueueSlicing(fileID, "test")

	// status 应该被重新设回 slicing，新心跳更近。
	st, _ := rdb.HGet(ctx, "video:meta:"+fileID, "status").Result()
	if st != "slicing" {
		t.Fatalf("expected status=slicing after re-enqueue, got %q", st)
	}
	hb, _ := rdb.HGet(ctx, "video:meta:"+fileID, "slicing_heartbeat_at").Result()
	hbI, _ := strconv.ParseInt(hb, 10, 64)
	if hbI < old+1 {
		t.Fatalf("heartbeat not refreshed (got %d, was %d)", hbI, old)
	}

	// 锁被清除，让新 worker 能拿到锁。
	if n, _ := rdb.Exists(ctx, "lock:slice:"+fileID).Result(); n != 0 {
		t.Fatalf("expected lock removed after re-enqueue")
	}

	// 任务存在于持久队列里。
	queueLen, err := rdb.LLen(ctx, sliceQueueKey).Result()
	if err != nil {
		t.Fatalf("llen: %v", err)
	}
	if queueLen == 0 {
		t.Fatalf("expected at least 1 task in %s after re-enqueue", sliceQueueKey)
	}
}

func TestEnsureVideoSliced_RecoversOnEntryWhenStale(t *testing.T) {
	s, rdb, _ := newTestScheduler(t)
	ctx := context.Background()

	fileID := "f-stuck"
	tmp := t.TempDir()
	srcPath := tmp + "/src.mp4"
	if err := writeDummyFile(srcPath); err != nil {
		t.Fatalf("write src: %v", err)
	}

	old := time.Now().Add(-10 * time.Minute).Unix()
	_ = rdb.HSet(ctx, "video:meta:"+fileID,
		"status", "slicing",
		"file_path", srcPath,
		"slicing_started_at", strconv.FormatInt(old, 10),
		"slicing_heartbeat_at", strconv.FormatInt(old, 10),
	).Err()

	// ensureVideoSliced 在等待循环里阻塞最多 180s；用短超时调用 waitForSlicingComplete 不行，
	// 所以这里只验证“调用前已重入队列”。开一个 goroutine 跑、200ms 后查 slice 队列是否非空。
	done := make(chan struct{})
	go func() {
		_ = s.ensureVideoSliced(fileID, "client-1")
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	pushed := false
	for time.Now().Before(deadline) {
		n, _ := rdb.LLen(ctx, sliceQueueKey).Result()
		if n > 0 {
			pushed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !pushed {
		t.Fatalf("ensureVideoSliced did not re-enqueue stale task")
	}
	// 让 ensureVideoSliced 提前结束以释放 goroutine
	_ = rdb.HSet(ctx, "video:meta:"+fileID, "status", "ready").Err()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Logf("ensureVideoSliced still running; ok (will exit on next 500ms tick)")
	}
}

func writeDummyFile(p string) error {
	return os.WriteFile(p, []byte("fake source so os.Stat passes"), 0o644)
}
