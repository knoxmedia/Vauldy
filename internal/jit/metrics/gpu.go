package metrics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

const redisGPUPercentKey = "jit:metrics:gpu_percent"

func resolveNvidiaSmiPath() string {
	if p := strings.TrimSpace(os.Getenv("KNOX_MEDIA_NVIDIA_SMI")); p != "" {
		return p
	}
	return "nvidia-smi"
}

// SampleNvidiaGPUUtil returns max GPU utilization % across visible CUDA GPUs via nvidia-smi (0–100).
func SampleNvidiaGPUUtil(ctx context.Context) (float64, bool) {
	smi := resolveNvidiaSmiPath()
	ctx2, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx2, smi, "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	var maxU float64
	var any bool
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		v, err := strconv.ParseFloat(line, 64)
		if err != nil {
			continue
		}
		any = true
		if v > maxU {
			maxU = v
		}
	}
	return maxU, any
}

// StartGPUPercentWriter writes jit:metrics:gpu_percent when nvidia-smi succeeds.
func StartGPUPercentWriter(ctx context.Context, rdb *redis.Client, interval time.Duration) {
	if interval < 5*time.Second {
		interval = 12 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				v, ok := SampleNvidiaGPUUtil(ctx)
				if !ok {
					continue
				}
				_ = rdb.Set(ctx, redisGPUPercentKey, fmt.Sprintf("%.2f", v), 3*interval).Err()
			}
		}
	}()
}

// ReadGPUPercent returns the last stored GPU % or -1 if missing/invalid.
func ReadGPUPercent(ctx context.Context, rdb *redis.Client) float64 {
	s, err := rdb.Get(ctx, redisGPUPercentKey).Result()
	if err != nil {
		return -1
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return v
}
