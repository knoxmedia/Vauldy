package metrics

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/shirou/gopsutil/v3/cpu"
)

const redisCPUPercentKey = "jit:metrics:cpu_percent"

// StartCPUPercentWriter periodically samples host CPU and stores a rolling value in Redis for JIT preheat tuning.
func StartCPUPercentWriter(ctx context.Context, rdb *redis.Client, interval time.Duration) {
	if interval < 5*time.Second {
		interval = 12 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		sampleWait := interval / 3
		if sampleWait < 800*time.Millisecond {
			sampleWait = 800 * time.Millisecond
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				v, err := cpu.Percent(sampleWait, false)
				if err != nil || len(v) == 0 {
					continue
				}
				_ = rdb.Set(ctx, redisCPUPercentKey, fmt.Sprintf("%.2f", v[0]), 3*interval).Err()
			}
		}
	}()
}

// StartJITMetricsWriters runs CPU (gopsutil) and GPU (nvidia-smi, when available) sampling into Redis.
func StartJITMetricsWriters(ctx context.Context, rdb *redis.Client, interval time.Duration) {
	StartCPUPercentWriter(ctx, rdb, interval)
	StartGPUPercentWriter(ctx, rdb, interval)
}

// ReadCPUPercent returns the last stored CPU % or -1 if missing/invalid.
func ReadCPUPercent(ctx context.Context, rdb *redis.Client) float64 {
	s, err := rdb.Get(ctx, redisCPUPercentKey).Result()
	if err != nil {
		return -1
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return v
}
