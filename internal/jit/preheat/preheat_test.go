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
		{name: "both sensors missing keeps default", total: 50, cpu: -1, gpu: -1, wantSegments: 18},
		{name: "high cpu shrinks preheat window", total: 50, cpu: 92, gpu: -1, wantSegments: 6},
		{name: "high gpu shrinks preheat window", total: 50, cpu: -1, gpu: 95, wantSegments: 6},
		{name: "low load keeps baseline window", total: 50, cpu: 20, gpu: 18, wantSegments: 18},
		{name: "uses stricter tier between cpu and gpu", total: 50, cpu: 72, gpu: 22, wantSegments: 12},
		{name: "never exceeds total segments", total: 5, cpu: 10, gpu: 10, wantSegments: 5},
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

func TestEnqueueInitialSegments_UsesLoadTunedCountAndDualBitrate(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	// Simulate heavy CPU load so preheat window shrinks to 6 segments.
	if err := rdb.Set(ctx, "jit:metrics:cpu_percent", "90", 0).Err(); err != nil {
		t.Fatalf("set cpu metric: %v", err)
	}
	// Low GPU would allow more, but SegmentCount should pick the stricter tier.
	if err := rdb.Set(ctx, "jit:metrics:gpu_percent", "20", 0).Err(); err != nil {
		t.Fatalf("set gpu metric: %v", err)
	}

	if err := EnqueueInitialSegments(ctx, rdb, "file-1", 20); err != nil {
		t.Fatalf("enqueue initial segments: %v", err)
	}

	members, err := rdb.ZRange(ctx, queueLow, 0, -1).Result()
	if err != nil {
		t.Fatalf("read queue members: %v", err)
	}
	const wantTasks = 6 * 2 // N segments * {500k, 2000k}
	if len(members) != wantTasks {
		t.Fatalf("queued tasks=%d, want %d", len(members), wantTasks)
	}

	seenBySegmentBitrate := make(map[string]bool, wantTasks)
	for _, raw := range members {
		var task models.TranscodeTask
		if err := json.Unmarshal([]byte(raw), &task); err != nil {
			t.Fatalf("decode queued task: %v", err)
		}
		if task.FileID != "file-1" {
			t.Fatalf("unexpected file_id=%q", task.FileID)
		}
		if task.SessionID != "prefetch" {
			t.Fatalf("unexpected session_id=%q, want prefetch", task.SessionID)
		}
		if task.Priority != 2 {
			t.Fatalf("unexpected priority=%d, want 2", task.Priority)
		}
		if task.Bitrate != "500k" && task.Bitrate != "2000k" {
			t.Fatalf("unexpected bitrate=%q", task.Bitrate)
		}
		k := task.Bitrate + ":" + strconv.Itoa(task.SegmentID)
		seenBySegmentBitrate[k] = true
	}

	// Confirm the queue covers every segment from 0..5 in both bitrates.
	for seg := 0; seg < 6; seg++ {
		for _, br := range []string{"500k", "2000k"} {
			k := br + ":" + strconv.Itoa(seg)
			if !seenBySegmentBitrate[k] {
				t.Fatalf("missing preheat task for segment=%d bitrate=%s", seg, br)
			}
		}
	}
}
