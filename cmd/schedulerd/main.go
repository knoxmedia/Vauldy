package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"

	"knox-media/cmd/scheduler"
	"knox-media/internal/config"
	jitmetrics "knox-media/internal/jit/metrics"
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
	cfg.ResolveExecutablePaths(filepath.Dir(cfgPath))

	redisAddr := strings.TrimSpace(os.Getenv("KNOX_MEDIA_REDIS_ADDR"))
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6379"
	}
	addr := strings.TrimSpace(os.Getenv("KNOX_MEDIA_SCHEDULER_ADDR"))
	if addr == "" {
		addr = ":8300"
	}

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	jitmetrics.StartJITMetricsWriters(context.Background(), rdb, 12*time.Second)
	s := scheduler.NewScheduler(
		rdb,
		scheduler.NewLocalStorage(cfg.Data.Transcode),
	)

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	api := r.Group("/api/v1")
	s.RegisterRoutes(api)

	log.Printf("scheduler listening on %s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}

func resolveConfigPath() string {
	if p := os.Getenv("KNOX_MEDIA_CONFIG"); p != "" {
		return p
	}
	return "config.yml"
}
