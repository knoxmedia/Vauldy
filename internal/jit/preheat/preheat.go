// Package preheat enqueues low-priority JIT transcodes for the first wall-clock segments after slicing completes.
package preheat

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"

	"knox-media/internal/jit/metrics"
	models "knox-media/internal/model"
)

const queueLow = "transcode:queue:low"

// SegmentCount picks how many leading segments to pre-transcode based on CPU/GPU load.
// We've moved to single-quality JIT so the previous 18-28 segment burst is overkill;
// 3-8 segments are enough for the player's initial buffer while respecting host load.
func SegmentCount(totalSegments int, cpuLoad, gpuLoad float64) int {
	if totalSegments <= 0 {
		return 0
	}
	n := 6
	tiers := []int{n}
	if cpuLoad >= 0 {
		tiers = append(tiers, cpuTargetSegments(cpuLoad))
	}
	if gpuLoad >= 0 {
		tiers = append(tiers, gpuTargetSegments(gpuLoad))
	}
	n = tiers[0]
	for _, t := range tiers[1:] {
		if t < n {
			n = t
		}
	}
	if n > totalSegments {
		n = totalSegments
	}
	return n
}

func cpuTargetSegments(cpu float64) int {
	switch {
	case cpu >= 88:
		return 0
	case cpu >= 70:
		return 2
	case cpu <= 30:
		return 8
	default:
		return 6
	}
}

func gpuTargetSegments(gpu float64) int {
	switch {
	case gpu >= 90:
		return 0
	case gpu >= 75:
		return 2
	case gpu <= 25:
		return 8
	default:
		return 6
	}
}

func resolutionForBitrate(bitrate string) string {
	switch bitrate {
	case "8000k":
		return "3840x2160"
	case "4000k":
		return "1920x1080"
	case "2000k":
		return "1280x720"
	case "1000k":
		return "854x480"
	case "500k":
		return "640x360"
	default:
		return "1280x720"
	}
}

// EnqueueInitialSegments pushes prefetch transcode jobs for the first N segments at the
// requested single-quality bitrate. Empty bitrate defaults to 2000k (720p) preview tier.
//
// 单清晰度 JIT 模式下不再 fan-out 多档（参见 internal/jit/profile.Pick），preheat 只为
// 用户即将拉取的那一档预热，避免低优先队列被占满堵住前台请求。
func EnqueueInitialSegments(ctx context.Context, rdb *redis.Client, fileID string, totalSegments int, bitrate string) error {
	cpu := metrics.ReadCPUPercent(ctx, rdb)
	gpu := metrics.ReadGPUPercent(ctx, rdb)
	n := SegmentCount(totalSegments, cpu, gpu)
	if n <= 0 {
		return nil
	}
	br := strings.TrimSpace(bitrate)
	if br == "" {
		br = "2000k"
	}
	base := float64(time.Now().Unix()) * 1e6
	now := time.Now().Unix()
	for seg := 0; seg < n; seg++ {
		task := models.TranscodeTask{
			FileID:     fileID,
			SegmentID:  seg,
			Bitrate:    br,
			Resolution: resolutionForBitrate(br),
			Codec:      "",
			Preset:     "veryfast",
			SessionID:  "prefetch",
			Priority:   2,
			CreatedAt:  now,
		}
		data, err := json.Marshal(task)
		if err != nil {
			continue
		}
		score := base + float64(seg)
		if err := rdb.ZAdd(ctx, queueLow, &redis.Z{Score: score, Member: data}).Err(); err != nil {
			return err
		}
	}
	return nil
}
