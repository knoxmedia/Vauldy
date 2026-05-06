package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"knox-media/cmd/sliceworker"
	"knox-media/internal/config"
	"knox-media/internal/zapglobal"
)

func main() {
	zlog := zapglobal.MustReplaceGlobals()
	defer func() { _ = zlog.Sync() }()

	cfgPath := resolveConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	redisAddr := strings.TrimSpace(os.Getenv("KNOX_MEDIA_REDIS_ADDR"))
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6379"
	}
	workerID := strings.TrimSpace(os.Getenv("KNOX_MEDIA_SLICE_WORKER_ID"))
	if workerID == "" {
		workerID = "slice-standalone"
	}

	w := sliceworker.NewSliceWorker(&sliceworker.Config{
		RedisAddr:         redisAddr,
		StoragePath:       cfg.Data.Transcode,
		FFmpegPath:        cfg.FFmpeg.FFmpegPath,
		FFprobePath:       cfg.FFmpeg.FFprobePath,
		WorkerID:          workerID,
		KeyframesCacheDir: filepath.Join(cfg.Data.Transcode, "keyframes"),
	})
	w.Start()
}

func resolveConfigPath() string {
	if p := os.Getenv("KNOX_MEDIA_CONFIG"); p != "" {
		return p
	}
	return "config.yml"
}
