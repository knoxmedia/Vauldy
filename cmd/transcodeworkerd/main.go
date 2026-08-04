package main

import (
	"log"
	"os"
	"strconv"
	"strings"

	"knox-media/cmd/transcodeworker"
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
	workerID := strings.TrimSpace(os.Getenv("KNOX_MEDIA_TRANSCODE_WORKER_ID"))
	if workerID == "" {
		workerID = "transcode-standalone"
	}
	maxConcurrent := 2
	if raw := strings.TrimSpace(os.Getenv("KNOX_MEDIA_TRANSCODE_MAX_CONCURRENT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			maxConcurrent = n
		}
	}

	w := transcodeworker.NewTranscodeWorker(&transcodeworker.Config{
		RedisAddr:     redisAddr,
		StoragePath:   cfg.Data.Transcode,
		FFmpegPath:    cfg.FFmpeg.FFmpegPath,
		WorkerID:      workerID,
		MaxConcurrent: maxConcurrent,
	})
	w.Start()
}

func resolveConfigPath() string {
	if p := os.Getenv("KNOX_MEDIA_CONFIG"); p != "" {
		return p
	}
	return "config.yml"
}
