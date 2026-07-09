// Package preheat enqueues low-priority JIT transcodes for the first wall-clock segments after slicing completes.
package preheat

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"

	"knox-media/internal/jit/metrics"
	"knox-media/internal/jit/profile"
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

func resolutionForBitrate(bitrate string, sourceWidth, sourceHeight int) string {
	return profile.ResolutionForBitrate(bitrate, sourceWidth, sourceHeight)
}

func sourceDimensions(ctx context.Context, rdb *redis.Client, fileID string) (int, int) {
	if rdb == nil || strings.TrimSpace(fileID) == "" {
		return 0, 0
	}
	w, errW := rdb.HGet(ctx, "video:meta:"+fileID, "width").Int()
	h, errH := rdb.HGet(ctx, "video:meta:"+fileID, "height").Int()
	if errW != nil {
		w = 0
	}
	if errH != nil {
		h = 0
	}
	return w, h
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
	srcW, srcH := sourceDimensions(ctx, rdb, fileID)
	base := float64(time.Now().Unix()) * 1e6
	now := time.Now().Unix()
	for seg := 0; seg < n; seg++ {
		task := models.TranscodeTask{
			FileID:     fileID,
			SegmentID:  seg,
			Bitrate:    br,
			Resolution: resolutionForBitrate(br, srcW, srcH),
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
