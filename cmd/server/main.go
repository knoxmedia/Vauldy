package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"golang.org/x/crypto/bcrypt"

	"knox-media/api"
	"knox-media/cmd/scheduler"
	"knox-media/cmd/sliceworker"
	"knox-media/cmd/transcodeworker"
	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/jit/ingestprepare"
	jitmetrics "knox-media/internal/jit/metrics"
	"knox-media/internal/monitor"
	"knox-media/internal/preview"
	"knox-media/internal/scanner"
	"knox-media/internal/store"
	"knox-media/internal/subtitle"
	"knox-media/internal/transcode"
	"knox-media/internal/upload"
	"knox-media/pkg/ffprobe"
)

func main() {
	cfgPath := resolveConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	cfg.ResolveExecutablePaths(filepath.Dir(cfgPath))
	if err := cfg.EnsureDirs(); err != nil {
		log.Fatalf("dirs: %v", err)
	}
	log.Printf("build marker: no_audio_master_patch=v1")

	db, err := store.OpenSQLite(cfg.Data.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	if err := seedUsers(db); err != nil {
		log.Fatalf("seed: %v", err)
	}

	application := &app.App{Config: cfg, DB: db}
	worker := transcode.NewWorker(db, cfg.FFmpeg.FFmpegPath, cfg.Data.Transcode)
	packageWorker := transcode.NewPackageWorker(db, cfg)
	go func() {
		scanned, fixed, err := packageWorker.HealLegacyInitFiles()
		if err != nil {
			log.Printf("drm startup self-check failed: %v", err)
			return
		}
		if fixed > 0 {
			log.Printf("drm startup self-check repaired legacy init files: scanned=%d fixed=%d", scanned, fixed)
		} else {
			log.Printf("drm startup self-check complete: scanned=%d fixed=0", scanned)
		}
	}()
	previewWorker := preview.NewWorker(db, cfg.FFmpeg.FFmpegPath, cfg.Data.Preview)
	ocrScript := strings.TrimSpace(cfg.Subtitle.GraphicalOCR.ScriptPath)
	if ocrScript == "" {
		if abs, err := filepath.Abs(filepath.Join(filepath.Dir(cfgPath), "tools", "subtitle_ocr", "bitmap_subtitle_ocr.py")); err == nil {
			ocrScript = abs
		}
	}
	subSvc := subtitle.NewService(db, cfg.FFmpeg.FFmpegPath, cfg.FFmpeg.FFprobePath, cfg.Data.Subtitle, subtitle.ASRConfig{
		Provider:    cfg.Subtitle.ASR.Provider,
		WhisperPath: cfg.Subtitle.ASR.WhisperPath,
		ExtraArgs:   cfg.Subtitle.ASR.ExtraArgs,
		Shell:       cfg.Subtitle.ASR.Shell,
	}, subtitle.OCRConfig{
		Enabled:        cfg.Subtitle.GraphicalOCR.Enabled,
		TesseractPath:  cfg.Subtitle.GraphicalOCR.TesseractPath,
		TessdataPrefix: cfg.Subtitle.GraphicalOCR.TessdataPrefix,
		Languages:      cfg.Subtitle.GraphicalOCR.Languages,
		PythonPath:     cfg.Subtitle.GraphicalOCR.PythonPath,
		ScriptPath:     ocrScript,
		PgsripPath:     cfg.Subtitle.GraphicalOCR.PgsripPath,
		MkvextractPath: cfg.Subtitle.GraphicalOCR.MkvextractPath,
		MkvmergePath:   cfg.Subtitle.GraphicalOCR.MkvmergePath,
	})
	up := &upload.Service{UploadDir: cfg.Data.Upload, ChunksDir: cfg.Data.Chunks}

	redisAddr := strings.TrimSpace(os.Getenv("KNOX_MEDIA_REDIS_ADDR"))
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6379"
	}
	instantStorage := cfg.Data.Transcode
	instantRedis := redis.NewClient(&redis.Options{Addr: redisAddr})
	jitmetrics.StartJITMetricsWriters(context.Background(), instantRedis, 12*time.Second)
	instantScheduler := scheduler.NewScheduler(
		instantRedis,
		scheduler.NewLocalStorage(instantStorage),
	)
	instantSliceWorker := sliceworker.NewSliceWorker(&sliceworker.Config{
		RedisAddr:   redisAddr,
		StoragePath: instantStorage,
		FFmpegPath:  cfg.FFmpeg.FFmpegPath,
		WorkerID:    "embedded-slice",
	})
	instantTranscodeWorker := transcodeworker.NewTranscodeWorker(&transcodeworker.Config{
		RedisAddr:     redisAddr,
		StoragePath:   instantStorage,
		FFmpegPath:    cfg.FFmpeg.FFmpegPath,
		WorkerID:      "embedded-transcode",
		MaxConcurrent: 2,
	})
	go instantSliceWorker.Start()
	go instantTranscodeWorker.Start()

	var ffprobeExtra []string
	if cfg.LibraryScanFastFFprobe() {
		ffprobeExtra = ffprobe.ScanProbeExtraFast()
	}
	sc := &scanner.Scanner{
		DB:           db,
		FFprobePath:  cfg.FFmpeg.FFprobePath,
		SkipHash:     !cfg.LibraryScanFileHash(),
		FFprobeExtra: ffprobeExtra,
	}
	sc.OnMediaAdded = func(mediaID int64, _ string, ft string) {
		go enqueueAutoTasksOnMediaAdded(db, cfg, subSvc, mediaID, ft)
		if ft == "video" {
			go func(id int64) {
				_, _ = packageWorker.EnqueueForMedia(id)
			}(mediaID)
			go ingestprepare.Kick(db, instantScheduler, worker, mediaID)
		}
	}
	mon := monitor.NewService(db, sc, 15*time.Second)
	go mon.Start(context.Background())

	engine := api.NewEngine(cfg, application, worker, packageWorker, previewWorker, subSvc, up, instantScheduler)
	log.Printf("knox-media listening on http://%s", cfg.Addr())
	if err := engine.Run(cfg.Addr()); err != nil {
		log.Fatal(err)
	}
}

// seedUsers creates default admin + demo viewer when DB is empty; ensures viewer exists on old DBs.
func seedUsers(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM user`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		h1, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO user (username, password, role) VALUES (?, ?, ?)`, "admin", string(h1), "admin"); err != nil {
			return err
		}
		h2, err := bcrypt.GenerateFromPassword([]byte("viewer123"), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		_, err = db.Exec(`INSERT INTO user (username, password, role) VALUES (?, ?, ?)`, "viewer", string(h2), "user")
		return err
	}
	var vn int
	if err := db.QueryRow(`SELECT COUNT(1) FROM user WHERE username = ?`, "viewer").Scan(&vn); err != nil {
		return err
	}
	if vn == 0 {
		h2, err := bcrypt.GenerateFromPassword([]byte("viewer123"), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		_, err = db.Exec(`INSERT INTO user (username, password, role) VALUES (?, ?, ?)`, "viewer", string(h2), "user")
		return err
	}
	return nil
}

// resolveConfigPath finds config.yml when cwd is the repo root (e.g. VS Code debug) or media/.
func resolveConfigPath() string {
	if p := os.Getenv("KNOX_MEDIA_CONFIG"); p != "" {
		return p
	}
	candidates := []string{
		"config.yml",
		filepath.Join("media", "config.yml"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			if p, err := filepath.Abs(c); err == nil {
				return p
			}
			return c
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		next := filepath.Join(dir, "config.yml")
		if _, err := os.Stat(next); err == nil {
			return next
		}
	}
	return "config.yml"
}

func enqueueAutoTasksOnMediaAdded(db *sql.DB, cfg *config.Config, subSvc *subtitle.Service, mediaID int64, fileType string) {
	if db == nil || cfg == nil || mediaID <= 0 {
		return
	}
	// 顺序与产品流水线对齐：本地预览图/海报 → 刮削（元数据与远端配图）。
	// JIT 关键帧索引与预编码：库级 jit_prepare_on_ingest 或 drm_enabled 时在 scanner/upload 路径由 ingestprepare.Kick 触发；否则仍在首次 JIT 点播时触发。
	enqueueAutoPreviewTask(db, mediaID, fileType)
	capturePosterOnScan(db, cfg, mediaID, fileType)
	enqueueAutoScrapeTask(db, mediaID)
	if subSvc != nil && cfg.SubtitleAutoOnScan() && fileType == "video" {
		_ = subSvc.EnsurePendingSubtitleTask(mediaID)
	}
}

func enqueueAutoScrapeTask(db *sql.DB, mediaID int64) {
	var exists int
	_ = db.QueryRow(`SELECT COUNT(1) FROM scrape_task WHERE media_id = ? AND status IN ('waiting','running')`, mediaID).Scan(&exists)
	if exists > 0 {
		return
	}
	_, _ = db.Exec(`INSERT INTO scrape_task (media_id, source, status, progress, created_by) VALUES (?, ?, 'waiting', 0, 0)`, mediaID, "auto-scan")
}

func enqueueAutoPreviewTask(db *sql.DB, mediaID int64, fileType string) {
	if fileType != "video" {
		return
	}
	var enabled sql.NullInt64
	var duration sql.NullInt64
	if err := db.QueryRow(`
		SELECT COALESCE(l.preview_extract,0), COALESCE(m.duration,0)
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&enabled, &duration); err != nil || enabled.Int64 != 1 {
		return
	}
	dur := duration.Int64
	if dur <= 0 {
		dur = 600
	}
	intervalSec := int(math.Ceil(float64(dur) / 100.0))
	if intervalSec < 5 {
		intervalSec = 5
	}
	countNum := int(math.Ceil(float64(dur) / float64(intervalSec)))
	if countNum < 1 {
		countNum = 1
	}
	if countNum > 100 {
		countNum = 100
	}
	_, _ = db.Exec(
		`INSERT INTO preview_task (media_id, status, interval_sec, thumb_count, thumb_width, thumb_height, updated_at)
		 VALUES (?, 'waiting', ?, ?, 240, 135, CURRENT_TIMESTAMP)
		 ON CONFLICT(media_id) DO UPDATE SET
		   status='waiting',
		   interval_sec=excluded.interval_sec,
		   thumb_count=excluded.thumb_count,
		   updated_at=CURRENT_TIMESTAMP,
		   error_message=NULL`,
		mediaID, intervalSec, countNum,
	)
}

func capturePosterOnScan(db *sql.DB, cfg *config.Config, mediaID int64, fileType string) {
	if fileType != "video" {
		return
	}
	ffmpegPath := strings.TrimSpace(cfg.FFmpeg.FFmpegPath)
	uploadDir := strings.TrimSpace(cfg.Data.Upload)
	if ffmpegPath == "" || uploadDir == "" {
		return
	}
	var filePath sql.NullString
	var duration sql.NullInt64
	var metaRaw sql.NullString
	if err := db.QueryRow(`SELECT file_path, COALESCE(duration,0), COALESCE(meta_json,'') FROM media WHERE id = ? LIMIT 1`, mediaID).
		Scan(&filePath, &duration, &metaRaw); err != nil {
		return
	}
	if strings.TrimSpace(filePath.String) == "" {
		return
	}
	posterDir := filepath.Join(uploadDir, "posters")
	if err := os.MkdirAll(posterDir, 0o755); err != nil {
		return
	}
	posterFile := filepath.Join(posterDir, fmt.Sprintf("%d.jpg", mediaID))
	posterURL := "/uploads/posters/" + fmt.Sprintf("%d.jpg", mediaID)

	snapSec := 10
	if duration.Int64 > 0 {
		sec := int(duration.Int64 / 5)
		if sec < 10 {
			sec = 10
		}
		if sec > 180 {
			sec = 180
		}
		snapSec = sec
	}
	if out, err := exec.Command(ffmpegPath, "-y", "-ss", strconv.Itoa(snapSec), "-i", filePath.String, "-frames:v", "1", "-q:v", "3", posterFile).CombinedOutput(); err != nil {
		_ = out
		return
	}

	var root map[string]any
	if strings.TrimSpace(metaRaw.String) != "" {
		_ = json.Unmarshal([]byte(metaRaw.String), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	scrape, _ := root["scrape"].(map[string]any)
	if scrape == nil {
		scrape = map[string]any{}
	}
	extra, _ := scrape["extra"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
	}
	if strings.TrimSpace(fmt.Sprintf("%v", extra["poster"])) == "" {
		extra["poster"] = posterURL
	}
	scrape["extra"] = extra
	root["scrape"] = scrape
	merged, _ := json.Marshal(root)
	_, _ = db.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, string(merged), mediaID)
}
