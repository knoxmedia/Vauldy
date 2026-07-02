package profile

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
)

func newTestRDB(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestPick_PrefersSourceHeight(t *testing.T) {
	got := Pick(context.Background(), nil, 720, 0)
	if got.Height != 720 {
		t.Fatalf("rung=%s height=%d, want 720p", got.Name, got.Height)
	}
}

func TestPick_RespectsClientMaxHeight(t *testing.T) {
	got := Pick(context.Background(), nil, 2160, 480)
	if got.Height != 480 {
		t.Fatalf("rung=%s height=%d, want 480", got.Name, got.Height)
	}
}

func TestPick_DropsCapWhenLoadHigh(t *testing.T) {
	rdb := newTestRDB(t)
	ctx := context.Background()
	if err := rdb.Set(ctx, "jit:metrics:cpu_percent", "92.0", 0).Err(); err != nil {
		t.Fatalf("seed cpu: %v", err)
	}
	got := Pick(ctx, rdb, 1080, 0)
	if got.Height != 360 {
		t.Fatalf("rung=%s height=%d, want 360 under high CPU", got.Name, got.Height)
	}
}

func TestPick_GPULoadOverridesCPU(t *testing.T) {
	rdb := newTestRDB(t)
	ctx := context.Background()
	_ = rdb.Set(ctx, "jit:metrics:cpu_percent", "10.0", 0).Err()
	_ = rdb.Set(ctx, "jit:metrics:gpu_percent", "82.5", 0).Err()
	got := Pick(ctx, rdb, 2160, 0)
	if got.Height > 480 {
		t.Fatalf("rung=%s height=%d, want <=480 under high GPU", got.Name, got.Height)
	}
}

func TestPickByName_FallsBackTo720(t *testing.T) {
	got := PickByName("garbage")
	if got.Height != 720 {
		t.Fatalf("got %s, want 720p", got.Name)
	}
}

func TestPickByName_AcceptsBitrateAlias(t *testing.T) {
	got := PickByName("4000k")
	if got.Height != 1080 {
		t.Fatalf("got %s, want 1080p", got.Name)
	}
}

// quick smoke for builtin rung table
func TestBuiltinRungs_OrderedDescending(t *testing.T) {
	for i := 1; i < len(builtinRungs); i++ {
		if builtinRungs[i].Height >= builtinRungs[i-1].Height {
			t.Fatalf("rung %s height=%d >= prev %s height=%d (must be descending)",
				builtinRungs[i].Name, builtinRungs[i].Height,
				builtinRungs[i-1].Name, builtinRungs[i-1].Height)
		}
	}
}

// keep linker happy in case fmt is unused after refactors
var _ = fmt.Sprintf
