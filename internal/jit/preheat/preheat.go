// Package preheat enqueues low-priority JIT transcodes for the first wall-clock segments after slicing completes.
package preheat

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-redis/redis/v8"

	"knox-media/internal/jit/metrics"
	models "knox-media/internal/model"
)

const queueLow = "transcode:queue:low"

// SegmentCount picks how many leading segments to pre-transcode from Redis CPU/GPU metrics (missing sensors ignored).
func SegmentCount(totalSegments int, cpuLoad, gpuLoad float64) int {
	if totalSegments <= 0 {
		return 0
	}
	n := 18
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
		return 6
	case cpu >= 70:
		return 12
	case cpu <= 30:
		return 28
	default:
		return 18
	}
}

func gpuTargetSegments(gpu float64) int {
	switch {
	case gpu >= 90:
		return 6
	case gpu >= 75:
		return 12
	case gpu <= 25:
		return 28
	default:
		return 18
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

// EnqueueInitialSegments pushes prefetch transcode jobs for the first N segments at 500k + 2000k.
func EnqueueInitialSegments(ctx context.Context, rdb *redis.Client, fileID string, totalSegments int) error {
	cpu := metrics.ReadCPUPercent(ctx, rdb)
	gpu := metrics.ReadGPUPercent(ctx, rdb)
	n := SegmentCount(totalSegments, cpu, gpu)
	if n <= 0 {
		return nil
	}
	base := float64(time.Now().Unix()) * 1e6
	brs := []struct {
		name   string
		preset string
	}{
		{"500k", "veryfast"},
		{"2000k", "fast"},
	}
	now := time.Now().Unix()
	for seg := 0; seg < n; seg++ {
		for brIdx, br := range brs {
			task := models.TranscodeTask{
				FileID:     fileID,
				SegmentID:  seg,
				Bitrate:    br.name,
				Resolution: resolutionForBitrate(br.name),
				Codec:      "",
				Preset:     br.preset,
				SessionID:  "prefetch",
				Priority:   2,
				CreatedAt:  now,
			}
			data, err := json.Marshal(task)
			if err != nil {
				continue
			}
			score := base + float64(seg*100+brIdx)
			if err := rdb.ZAdd(ctx, queueLow, &redis.Z{Score: score, Member: data}).Err(); err != nil {
				return err
			}
		}
	}
	return nil
}
