package preheat

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"

	models "knox-media/internal/model"
)

func TestSegmentCount_AdjustsByLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		total        int
		cpu          float64
		gpu          float64
		wantSegments int
	}{
		{name: "both sensors missing keeps default", total: 50, cpu: -1, gpu: -1, wantSegments: 6},
		{name: "high cpu pauses preheat", total: 50, cpu: 92, gpu: -1, wantSegments: 0},
		{name: "high gpu pauses preheat", total: 50, cpu: -1, gpu: 95, wantSegments: 0},
		{name: "low load keeps baseline window", total: 50, cpu: 20, gpu: 18, wantSegments: 6},
		{name: "uses stricter tier between cpu and gpu", total: 50, cpu: 72, gpu: 22, wantSegments: 2},
		{name: "never exceeds total segments", total: 3, cpu: 10, gpu: 10, wantSegments: 3},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SegmentCount(tc.total, tc.cpu, tc.gpu)
			if got != tc.wantSegments {
				t.Fatalf("SegmentCount(%d, %.2f, %.2f)=%d, want %d", tc.total, tc.cpu, tc.gpu, got, tc.wantSegments)
			}
		})
	}
}

func TestEnqueueInitialSegments_SingleBitrateRespectsLoadTier(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	// Idle host -> 6 segments preheat window.
	if err := rdb.Set(ctx, "jit:metrics:cpu_percent", "10", 0).Err(); err != nil {
		t.Fatalf("set cpu metric: %v", err)
	}
	if err := rdb.Set(ctx, "jit:metrics:gpu_percent", "10", 0).Err(); err != nil {
		t.Fatalf("set gpu metric: %v", err)
	}

	if err := EnqueueInitialSegments(ctx, rdb, "file-1", 20, "2000k"); err != nil {
		t.Fatalf("enqueue initial segments: %v", err)
	}

	members, err := rdb.ZRange(ctx, queueLow, 0, -1).Result()
	if err != nil {
		t.Fatalf("read queue members: %v", err)
	}
	if len(members) == 0 || len(members) > 8 {
		t.Fatalf("queued tasks=%d, want small single-bitrate set", len(members))
	}
	for _, raw := range members {
		var task models.TranscodeTask
		if err := json.Unmarshal([]byte(raw), &task); err != nil {
			t.Fatalf("decode queued task: %v", err)
		}
		if task.Bitrate != "2000k" {
			t.Fatalf("unexpected bitrate=%q, want single 2000k preheat", task.Bitrate)
		}
		if task.SessionID != "prefetch" {
			t.Fatalf("unexpected session_id=%q", task.SessionID)
		}
	}
	// Reaches segment 0 at minimum.
	first := members[0]
	var t0 models.TranscodeTask
	if err := json.Unmarshal([]byte(first), &t0); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if t0.SegmentID != 0 {
		t.Fatalf("first preheat segment=%d, want 0", t0.SegmentID)
	}
	_ = strconv.Itoa
}

func TestEnqueueInitialSegments_HighLoadEmitsNothing(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	_ = rdb.Set(ctx, "jit:metrics:cpu_percent", "95", 0).Err()
	_ = rdb.Set(ctx, "jit:metrics:gpu_percent", "95", 0).Err()

	if err := EnqueueInitialSegments(ctx, rdb, "file-busy", 20, "2000k"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	n, _ := rdb.ZCard(ctx, queueLow).Result()
	if n != 0 {
		t.Fatalf("expected 0 prefetch tasks under high load, got %d", n)
	}
}
