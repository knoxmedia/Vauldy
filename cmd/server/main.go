package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"golang.org/x/crypto/bcrypt"

	"knox-media/api"
	"knox-media/api/handler"
	"knox-media/cmd/scheduler"
	"knox-media/cmd/sliceworker"
	"knox-media/cmd/transcodeworker"
	"knox-media/internal/app"
	"knox-media/internal/atrack"
	"knox-media/internal/config"
	"knox-media/internal/doccover"
	"knox-media/internal/jit/hwenc"
	"knox-media/internal/imagethumb"
	jitsession "knox-media/internal/jit/session"
	"knox-media/internal/jit/ingestprepare"
	"knox-media/internal/keystore"
	jitmetrics "knox-media/internal/jit/metrics"
	"knox-media/internal/keyframe"
	"knox-media/internal/lyrictask"
	"knox-media/internal/monitor"
	"knox-media/internal/photoclass"
	"knox-media/internal/photoface"
	"knox-media/internal/preview"
	"knox-media/internal/scanner"
	"knox-media/internal/storage"
	"knox-media/internal/store"
	"knox-media/internal/subtitle"
	"knox-media/internal/transcode"
	"knox-media/internal/upload"
	"knox-media/internal/zapglobal"
	"knox-media/pkg/ffprobe"
)

func main() {
	zlog := zapglobal.MustReplaceGlobals()
	defer func() { _ = zlog.Sync() }()

	cfgPath := config.ResolveConfigPath()
	cfgPath, err := config.EnsureConfigFile(cfgPath)
	if err != nil {
		log.Fatalf("config bootstrap: %v", err)
	}
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
	store.ResetInterruptedTasks(db)

	if err := seedUsers(db); err != nil {
		log.Fatalf("seed: %v", err)
	}

	application := &app.App{Config: cfg, ConfigPath: cfgPath, DB: db}
	application.AvailableHardwareAcceleration = hwenc.ListAvailableHardwareAcceleration(cfg.FFmpeg.FFmpegPath)
	if err := handler.EnsureHardwareAccelDefaults(db, cfg.FFmpeg.FFmpegPath, application.AvailableHardwareAcceleration); err != nil {
		log.Printf("hardware acceleration defaults: %v", err)
	}
	if len(application.AvailableHardwareAcceleration) == 0 {
		log.Printf("hardware acceleration: none detected, using software encoding")
	} else {
		log.Printf("hardware acceleration detected: available=%v best=%s",
			application.AvailableHardwareAcceleration,
			hwenc.DetectHWAccel(cfg.FFmpeg.FFmpegPath),
		)
	}
	transcodeSettings := loadSystemOptionsTranscodeSettings(db)
	keyVault, assetEncryptor := storage.NewAssetEncryptorFromConfig(cfg, db)
	derivedStore := storage.NewDerivedAssetStoreFromConfig(cfg, db, keyVault)
	worker := transcode.NewWorker(db, cfg.FFmpeg.FFmpegPath, cfg.Data.Transcode)
	packageWorker := transcode.NewPackageWorker(db, cfg, keyVault)
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
	previewWorker := preview.NewWorker(db, keyVault, derivedStore, cfg.FFmpeg.FFmpegPath, cfg.Data.Preview)
	ocrScript := strings.TrimSpace(cfg.Subtitle.GraphicalOCR.ScriptPath)
	if ocrScript == "" {
		if abs, err := filepath.Abs(filepath.Join(filepath.Dir(cfgPath), "tools", "subtitle_ocr", "bitmap_subtitle_ocr.py")); err == nil {
			ocrScript = abs
		}
	}
subSvc := subtitle.NewService(db, keyVault, derivedStore, filepath.Dir(cfgPath), cfg.FFmpeg.FFmpegPath, cfg.FFmpeg.FFprobePath, cfg.Data.Subtitle, subtitle.ASRConfig{
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
subSvc.AIProofread = cfg.SubtitleAIProofreadEnabled()
	up := &upload.Service{UploadDir: cfg.Data.Upload, ChunksDir: cfg.Data.Chunks}
	atrackWorker := atrack.NewWorker(db, keyVault, derivedStore, cfg.FFmpeg.FFmpegPath, cfg.FFmpeg.FFprobePath, cfg.Data.ATracks)
	keyframeWorker := keyframe.NewWorker(db, keyVault, derivedStore, cfg.FFmpeg.FFprobePath, cfg.Data.Keyframes)
	lyricWorkDir := filepath.Join(cfg.Data.Dir, "lyrics")
	lyricWorker := lyrictask.NewWorker(db, derivedStore, lyricWorkDir, cfg.FFmpeg.FFprobePath, subSvc)
	photoClassifyWorker := photoclass.NewWorker(db, keyVault, filepath.Dir(cfgPath), cfg.FFmpeg.FFmpegPath, cfg.Data.Preview, func() config.PhotoClassifyConfig {
		return cfg.PhotoClassify
	})
	photoFaceWorker := photoface.NewWorker(db, keyVault, derivedStore, filepath.Dir(cfgPath), cfg.FFmpeg.FFmpegPath, cfg.Data.Preview, func() config.PhotoFaceConfig {
		faceCfg := cfg.PhotoFace
		if strings.TrimSpace(faceCfg.PythonPath) == "" {
			faceCfg.PythonPath = cfg.PhotoClassify.PythonPath
		}
		if strings.TrimSpace(faceCfg.ScriptPath) == "" {
			faceCfg.ScriptPath = "tools/photo_face/detect.py"
		}
		return faceCfg
	})

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
	instantScheduler.SetHLSMultiAudioEnabled(cfg.HLSMultiAudioEnabled())
	instantScheduler.SetHLSContinuous(cfg.JITContinuousHLSEnabled())

	// New Redis-free session manager (clears dataDir/jit from previous runs).
	sessionMgr, err := jitsession.NewManager(cfg.FFmpeg.FFmpegPath, cfg.FFmpeg.FFprobePath, cfg.Data.Dir, cfg.Data.Keyframes, db, keyVault)
	if err != nil {
		log.Fatalf("jit session manager: %v", err)
	}
	storage.SetMediaPlaintextBusy(sessionMgr.HasActiveMedia)
	go storage.KickPendingPlaintextCleanups(db)
	go storage.KickEncryptedMP4PipeRepairs(assetEncryptor)
	go storage.KickPendingMediaEncryption(assetEncryptor, cfg)

	instantSliceWorker := sliceworker.NewSliceWorker(&sliceworker.Config{
		RedisAddr:   redisAddr,
		StoragePath: instantStorage,
		FFmpegPath:  cfg.FFmpeg.FFmpegPath,
		FFprobePath: cfg.FFmpeg.FFprobePath,
		WorkerID:    "embedded-slice",
		NoPreheat:   cfg.JITContinuousHLSEnabled(),
	})
	instantTranscodeWorker := transcodeworker.NewTranscodeWorker(&transcodeworker.Config{
		RedisAddr:            redisAddr,
		StoragePath:          instantStorage,
		FFmpegPath:           cfg.FFmpeg.FFmpegPath,
		WorkerID:             "embedded-transcode",
		MaxConcurrent:        instantMaxConcurrent(),
		HLSContinuousEnabled: cfg.JITContinuousHLSEnabled(),
		VideoEncoder:         string(transcodeSettings.EffectiveHWEncoderID()),
	})
	// Redis-free session JIT replaces these; only start old workers if Redis is available.
	redisAvailable := false
	if _, err := instantRedis.Ping(context.Background()).Result(); err == nil {
		redisAvailable = true
	}
	if redisAvailable {
		go instantSliceWorker.Start()
		go instantTranscodeWorker.Start()
	} else {
		log.Printf("Redis not available; slice/transcode workers disabled (session-based JIT active)")
	}

	var ffprobeExtra []string
	if cfg.LibraryScanFastFFprobe() {
		ffprobeExtra = ffprobe.ScanProbeExtraFast()
	}
	mediaRoot := filepath.Dir(cfgPath)
	docCoverWorker := doccover.NewWorker(doccover.WorkerConfig{
		DB:         db,
		Vault:      keyVault,
		Derived:    derivedStore,
		MediaRoot:  mediaRoot,
		PreviewDir: cfg.Data.Preview,
		FFmpegPath: cfg.FFmpeg.FFmpegPath,
		DocTrans:   cfg.DocTrans,
		TimeoutSec: cfg.DocTransTimeoutSeconds,
	})
	sc := &scanner.Scanner{
		DB:           db,
		Vault:        keyVault,
		FFprobePath:  cfg.FFmpeg.FFprobePath,
		SkipHash:     !cfg.LibraryScanFileHash(),
		FFprobeExtra: ffprobeExtra,
	}
	sc.OnDocumentScanned = func(mediaID int64) {
		docCoverWorker.Enqueue(mediaID)
	}
	sc.OnMediaAdded = func(mediaID int64, _ string, ft string) {
		go enqueueAutoTasksOnMediaAdded(db, keyVault, cfg, assetEncryptor, derivedStore, previewWorker, docCoverWorker, subSvc, atrackWorker, keyframeWorker, lyricWorker, photoClassifyWorker, photoFaceWorker, mediaID, ft)
		if ft == "video" {
			var drmEnabled int
			_ = db.QueryRow(`
				SELECT COALESCE(l.drm_enabled,0) FROM media m LEFT JOIN library l ON l.id = m.library_id WHERE m.id = ?
			`, mediaID).Scan(&drmEnabled)
			if drmEnabled == 0 {
				go func(id int64) {
					_, _ = packageWorker.EnqueueForMedia(id)
				}(mediaID)
			}
			go ingestprepare.Kick(db, instantScheduler, mediaID)
		}
	}
	mon := monitor.NewService(db, sc, 15*time.Second)
	go mon.Start(context.Background())

	engine := api.NewEngine(cfg, application, worker, packageWorker, previewWorker, subSvc, up, instantScheduler, sessionMgr, atrackWorker, keyframeWorker, lyricWorker, photoClassifyWorker, docCoverWorker)
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

// instantMaxConcurrent picks how many ffmpeg children the embedded JIT transcode worker may run
// in parallel. Default = max(2, NumCPU/2) so single-quality user requests never sit behind
// prefetch tasks. Override with KNOX_MEDIA_JIT_MAX_CONCURRENT.
func instantMaxConcurrent() int {
	if v := strings.TrimSpace(os.Getenv("KNOX_MEDIA_JIT_MAX_CONCURRENT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	n := runtime.NumCPU() / 2
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	return n
}

func enqueueAutoTasksOnMediaAdded(db *sql.DB, vault *keystore.Vault, cfg *config.Config, assetEnc *storage.AssetEncryptor, derivedStore *storage.DerivedAssetStore, previewWorker *preview.Worker, dcw *doccover.Worker, subSvc *subtitle.Service, atw *atrack.Worker, kfw *keyframe.Worker, lw *lyrictask.Worker, pcw *photoclass.Worker, pfw *photoface.Worker, mediaID int64, fileType string) {
	if db == nil || cfg == nil || mediaID <= 0 {
		return
	}
	// 顺序与产品流水线对齐：本地预览图/海报 → 刮削（元数据与远端配图）。
	// JIT 关键帧索引：仅 jit_prepare_on_ingest 时在 scanner/upload 路径触发；传输流实时加密在点播时处理。
	enqueueAutoPreviewTask(db, mediaID, fileType)
	ensureAutoPreviewGeneration(db, previewWorker, mediaID, fileType)
	capturePosterOnScan(db, vault, derivedStore, cfg, mediaID, fileType)
	generatePhotoVariantsOnScan(db, vault, cfg, mediaID, fileType)
	if fileType == "document" && dcw != nil {
		dcw.Enqueue(mediaID)
	}
	if fileType != "image" {
		enqueueAutoScrapeTask(db, mediaID)
	}
	if subSvc != nil && cfg.SubtitleAutoOnScan() && fileType == "video" {
		_ = subSvc.EnsurePendingSubtitleTask(mediaID)
	}
	if lw != nil && cfg.LyricAutoOnScan() {
		_ = lw.EnsurePendingIfNoLyrics(mediaID, fileType)
	}
	if pcw != nil && cfg.PhotoClassifyAutoOnScan() && fileType == "image" {
		_ = pcw.EnsurePendingIfPhoto(mediaID, fileType)
	}
	if pfw != nil && cfg.PhotoFaceAutoOnScan() && fileType == "image" {
		_ = pfw.EnsurePendingIfPhoto(mediaID, fileType)
	}
	if fileType == "video" {
		if atw != nil && cfg.ATrackAutoOnScan() {
			atw.Enqueue(mediaID)
		}
		if kfw != nil {
			kfw.Enqueue(mediaID)
		}
	}
	storage.KickEncryptMedia(assetEnc, cfg, mediaID)
}

func enqueueAutoScrapeTask(db *sql.DB, mediaID int64) {
	var exists int
	_ = db.QueryRow(`SELECT COUNT(1) FROM scrape_task WHERE media_id = ? AND status IN ('waiting','running','failed','abandoned')`, mediaID).Scan(&exists)
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
	_ = preview.UpsertWaitingPreviewTask(db, mediaID, intervalSec, countNum)
}

func ensureAutoPreviewGeneration(db *sql.DB, previewWorker *preview.Worker, mediaID int64, fileType string) {
	if db == nil || previewWorker == nil || mediaID <= 0 || fileType != "video" {
		return
	}
	var libraryID sql.NullInt64
	var filePath sql.NullString
	var duration sql.NullInt64
	var enabled sql.NullInt64
	if err := db.QueryRow(`
		SELECT m.library_id, m.file_path, COALESCE(m.duration,0), COALESCE(l.preview_extract,0)
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&libraryID, &filePath, &duration, &enabled); err != nil || enabled.Int64 != 1 {
		return
	}
	inputPath := storage.PreferredFFmpegPath(db, mediaID, libraryID.Int64, filePath.String)
	if inputPath == "" {
		return
	}
	_, _ = previewWorker.Ensure(context.Background(), mediaID, inputPath, duration.Int64)
}

func capturePosterOnScan(db *sql.DB, vault *keystore.Vault, derivedStore *storage.DerivedAssetStore, cfg *config.Config, mediaID int64, fileType string) {
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
	var libraryID sql.NullInt64
	if err := db.QueryRow(`SELECT library_id, file_path, COALESCE(duration,0), COALESCE(meta_json,'') FROM media WHERE id = ? LIMIT 1`, mediaID).
		Scan(&libraryID, &filePath, &duration, &metaRaw); err != nil {
		return
	}
	inputPath := storage.PreferredFFmpegPath(db, mediaID, libraryID.Int64, filePath.String)
	if inputPath == "" {
		return
	}
	posterDir := filepath.Join(uploadDir, "posters")
	if err := os.MkdirAll(posterDir, 0o755); err != nil {
		return
	}
	posterFile := filepath.Join(posterDir, fmt.Sprintf("%d.jpg", mediaID))

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
	post := []string{"-frames:v", "1", "-q:v", "3", posterFile}
	pre := storage.PosterSeekPreInput(snapSec, inputPath)
	if _, err := storage.RunFFmpeg(context.Background(), db, vault, ffmpegPath, mediaID, inputPath, 0, 0, pre, post, ""); err != nil {
		return
	}
	posterURL, err := storage.FinalizeLocalPoster(context.Background(), derivedStore, db, mediaID, posterFile)
	if err != nil {
		_ = os.Remove(posterFile)
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

func generatePhotoVariantsOnScan(db *sql.DB, vault *keystore.Vault, cfg *config.Config, mediaID int64, fileType string) {
	if fileType != "image" || db == nil || cfg == nil || mediaID <= 0 {
		return
	}
	ffmpegPath := strings.TrimSpace(cfg.FFmpeg.FFmpegPath)
	if ffmpegPath == "" {
		return
	}
	derivedStore := storage.NewDerivedAssetStoreFromConfig(cfg, db, vault)
	var filePath sql.NullString
	var metaRaw sql.NullString
	if err := db.QueryRow(`SELECT file_path, COALESCE(meta_json,'') FROM media WHERE id = ? LIMIT 1`, mediaID).
		Scan(&filePath, &metaRaw); err != nil {
		return
	}
	if strings.TrimSpace(filePath.String) == "" {
		return
	}
	cacheDir := filepath.Join(cfg.Data.Preview, "photos")
	paths, err := imagethumb.Ensure(context.Background(), db, vault, derivedStore, ffmpegPath, filePath.String, cacheDir, mediaID)
	if err != nil {
		return
	}
	var root map[string]any
	if strings.TrimSpace(metaRaw.String) != "" {
		_ = json.Unmarshal([]byte(metaRaw.String), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	photo, _ := root["photo"].(map[string]any)
	if photo == nil {
		photo = map[string]any{}
	}
	photo["thumb_path"] = paths.Thumb
	photo["medium_path"] = paths.Medium
	root["photo"] = photo
	merged, _ := json.Marshal(root)
	_, _ = db.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, string(merged), mediaID)
}

func loadSystemOptionsTranscodeSettings(db *sql.DB) transcode.Settings {
	if db == nil {
		return transcode.DefaultSettings()
	}
	var raw sql.NullString
	if err := db.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw); err != nil {
		return transcode.DefaultSettings()
	}
	return transcode.SettingsFromOptionsJSON(raw.String)
}
