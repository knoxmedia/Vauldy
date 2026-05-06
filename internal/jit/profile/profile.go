// Package profile chooses a single transcoding rung at runtime based on source resolution,
// client max_height and CPU/GPU load. Replaces the historical 5-tier ladder so that JIT
// only spends CPU/GPU on one variant – matching the user-requested behaviour:
// "即时转码根据机器性能仅包含一个清晰度".
package profile

import (
	"context"
	"strings"

	"github.com/go-redis/redis/v8"

	"knox-media/internal/jit/metrics"
)

// Variant is the picked rung.
type Variant struct {
	Name      string // e.g. "720p"
	Bitrate   string // ffmpeg -b:v friendly: "500k", "2000k"
	Bandwidth int    // bps for HLS BANDWIDTH
	Width     int
	Height    int
}

// builtinRungs lists rungs from highest to lowest. The picker chooses the highest
// rung whose Height fits source / client / load constraints.
var builtinRungs = []Variant{
	{Name: "2160p", Bitrate: "8000k", Bandwidth: 8000000, Width: 3840, Height: 2160},
	{Name: "1080p", Bitrate: "4000k", Bandwidth: 4000000, Width: 1920, Height: 1080},
	{Name: "720p", Bitrate: "2000k", Bandwidth: 2000000, Width: 1280, Height: 720},
	{Name: "480p", Bitrate: "1000k", Bandwidth: 1000000, Width: 854, Height: 480},
	{Name: "360p", Bitrate: "500k", Bandwidth: 500000, Width: 640, Height: 360},
}

// Pick returns the single rung this server should produce for fileID.
// rdb may be nil; callers passing nil get a load-agnostic decision based purely on srcHeight + maxClientHeight.
func Pick(ctx context.Context, rdb *redis.Client, srcHeight, maxClientHeight int) Variant {
	cap := constrainCap(srcHeight, maxClientHeight)
	cap = applyLoadLimit(ctx, rdb, cap)
	for _, v := range builtinRungs {
		if v.Height <= cap {
			return v
		}
	}
	return builtinRungs[len(builtinRungs)-1]
}

// PickByName returns the variant entry for an explicit rung name, fallback to 720p.
func PickByName(name string) Variant {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, v := range builtinRungs {
		if strings.EqualFold(v.Name, name) || v.Bitrate == name {
			return v
		}
	}
	for _, v := range builtinRungs {
		if v.Name == "720p" {
			return v
		}
	}
	return builtinRungs[2]
}

func constrainCap(srcHeight, maxClientHeight int) int {
	cap := 1<<31 - 1
	if srcHeight > 0 && srcHeight < cap {
		cap = srcHeight
	}
	if maxClientHeight > 0 && maxClientHeight < cap {
		cap = maxClientHeight
	}
	return cap
}

// applyLoadLimit reduces the rung cap based on CPU/GPU load:
// >85% busy -> max 480p, >70% -> max 720p, >50% -> max 1080p. Missing metrics ignored.
func applyLoadLimit(ctx context.Context, rdb *redis.Client, cap int) int {
	if rdb == nil {
		return cap
	}
	cpu := metrics.ReadCPUPercent(ctx, rdb)
	gpu := metrics.ReadGPUPercent(ctx, rdb)
	worst := -1.0
	if cpu >= 0 {
		worst = cpu
	}
	if gpu > worst {
		worst = gpu
	}
	if worst < 0 {
		return cap
	}
	switch {
	case worst >= 90:
		if cap > 360 {
			cap = 360
		}
	case worst >= 80:
		if cap > 480 {
			cap = 480
		}
	case worst >= 65:
		if cap > 720 {
			cap = 720
		}
	case worst >= 50:
		if cap > 1080 {
			cap = 1080
		}
	}
	return cap
}
