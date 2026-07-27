package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"knox-media/api"
	"knox-media/api/handler"
	"knox-media/cmd/scheduler"
	"knox-media/cmd/sliceworker"
	"knox-media/cmd/transcodeworker"
	"knox-media/internal/app"
	"knox-media/internal/atrack"
	"knox-media/internal/buildinfo"
	"knox-media/internal/config"
	"knox-media/internal/coreiface"
	"knox-media/internal/doccover"
	"knox-media/internal/jit/hwenc"
	jitmetrics "knox-media/internal/jit/metrics"
	jitsession "knox-media/internal/jit/session"
	"knox-media/internal/keyframe"
	// Enterprise module imports 闂?their init() registers into
	// coreiface.EnterpriseModules. The community build excludes these
	// imports (and the packages themselves), leaving EnterpriseModules empty.
	_ "knox-media/internal/license"
	"knox-media/internal/lyrictask"
	"knox-media/internal/metadatalib"
	"knox-media/internal/monitor"
	"knox-media/internal/photoclass"
	"knox-media/internal/postingest"
	_ "knox-media/internal/pretranscode"
	"knox-media/internal/preview"
	"knox-media/internal/publication"
	"knox-media/internal/scancoord"
	"knox-media/internal/scanner"
	"knox-media/internal/storage"
	"knox-media/internal/store"
	"knox-media/internal/subtitle"
	"knox-media/internal/transcode"
	"knox-media/internal/upload"
	"knox-media/internal/zapglobal"
	"knox-media/pkg/ffprobe"
)

type cancelScanPersistence func(context.Context, int64) (int64, error)

func handleScanCancelled(ctx context.Context, taskID int64, persist cancelScanPersistence, cancelLocal func(int64)) error {
	// Stop local execution first so a running worker cannot race a durable cancel.
	// Persistence is still attempted and its error is returned to the coordinator.
	cancelLocal(taskID)
	_, err := persist(ctx, taskID)
	return err
}
func main() {
	zlog := zapglobal.MustReplaceGlobals()
	defer func() { _ = zlog.Sync() }()

	serverCtx, serverCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer serverCancel()

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
	// (1) Database, metrics, and validated configuration. Configuration is validated above; OpenSQLite lives here rather than app.New.
	db, err := store.OpenSQLiteContext(serverCtx, cfg.Data.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()
	info := buildinfo.Current()
	identity, identityOK := store.SQLiteIdentity(db)
	if !identityOK {
		identity = store.SQLiteDBIdentity{Path: "unknown"}
	}
	log.Printf("%s", startupBuildLog(info, identity))
	for _, warning := range buildinfo.ValidateDevelopment(info) {
		log.Printf("build metadata warning: %s", warning)
	}
	log.Printf("build marker: no_audio_master_patch=v1")
	sqliteMetrics := &store.SQLiteMetrics{}

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
	// (2) Vault, derived storage, and domain workers.
	transcodeSettings := loadSystemOptionsTranscodeSettings(db)
	keyVault, assetEnc := storage.NewAssetEncryptorFromConfig(cfg, db)
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
	// (3) Shared post-ingest queue, enqueuer, seven adapters, and dispatcher.
	publicationSteps := []string{"poster", "poster_repair", "thumbnail", "preview", "keyframe", "subtitle", "atrack", "encrypt", "scrape"}
	if coreiface.IngestPreparePlannerHandle() != nil {
		publicationSteps = append(publicationSteps, "prepare")
	}
	publicationCapabilities := publication.NewCapabilityMatrix(publicationSteps)
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown-host"
	}
	processID := fmt.Sprintf("%s-%d-%s", hostname, os.Getpid(), uuid.NewString())
	queueOwner := "postingest-" + processID
	postIngestQueue := postingest.NewQueue(db, queueOwner, sqliteMetrics, publicationCapabilities)
	postIngestEnqueuer := postingest.NewEnqueuer(db, cfg, sqliteMetrics)
	thumbnailWorker := &postingest.LocalThumbnailWorker{DB: db, Vault: keyVault, Derived: derivedStore, FFmpegPath: cfg.FFmpeg.FFmpegPath, PreviewDir: cfg.Data.Preview}
	posterRunner := &postingest.LocalPosterRunner{DB: db, Vault: keyVault, Derived: derivedStore, FFmpegPath: cfg.FFmpeg.FFmpegPath, FFprobePath: cfg.FFmpeg.FFprobePath, UploadDir: cfg.Data.Upload}
	adapters := postingest.AdapterSet{
		Thumbnail: postingest.NewThumbnailAdapter(db, thumbnailWorker),
		Poster:    postingest.NewPosterAdapter(db, cfg.Data.Upload, derivedStore, posterRunner),
		Preview:   postingest.NewPreviewAdapter(db, previewWorker),
		Keyframe:  postingest.NewKeyframeAdapter(db, keyframeWorker),
		Subtitle:  postingest.NewSubtitleAdapter(db, subSvc),
		Atrack:    postingest.NewAtrackAdapter(db, atrackWorker),
		Encrypt:   postingest.NewEncryptAdapter(assetEnc),
	}
	dispatcherOptions := buildDispatcherOptions(cfg, queueOwner)
	dispatcher, err := postingest.NewDispatcher(postIngestQueue, adapters, dispatcherOptions)
	if err != nil {
		log.Fatalf("post-ingest dispatcher: %v", err)
	}
	dispatcherDone := make(chan error, 1)

	preparePlanner := coreiface.IngestPreparePlannerHandle()

	publicationPlanner := publication.NewPlanner(publication.PlanOptions{
		SubtitleAuto: cfg.SubtitleAutoOnScan(), ATrackAuto: cfg.ATrackAutoOnScan(),
		EncryptGlobal: cfg.EncryptedAssetsEnabled(), PreparePlanner: preparePlanner, Capabilities: publicationCapabilities,
	})

	startupReady := make(chan struct{})
	startupRoots := StartupRecoveryRoots{Encryption: postingest.EncryptionRecoveryRoots{Quarantine: assetEnc.EncryptionPrivateRoot(), Resolver: assetEnc}, Thumbnail: postingest.ThumbnailRecoveryRoots{Preview: filepath.Join(cfg.Data.Preview, "photos"), Derived: filepath.Join(cfg.Data.Dir, ".derived")}, Poster: postingest.PosterRecoveryRoots{Upload: cfg.Data.Upload, Derived: filepath.Join(cfg.Data.Dir, ".derived")}, ScrapeArtwork: cfg.Data.MetadataLibrary}
	enterpriseCtx, enterpriseCancel := context.WithCancel(serverCtx)
	defer enterpriseCancel()
	for _, mod := range coreiface.EnterpriseModules {
		if err := mod.Init(enterpriseCtx, coreiface.ModuleDeps{DB: db, Config: cfg, Vault: keyVault, TranscodeDir: cfg.Data.Transcode, FFmpegPath: cfg.FFmpeg.FFmpegPath, FFprobePath: cfg.FFmpeg.FFprobePath, Capabilities: publicationCapabilities}); err != nil {
			log.Fatalf("enterprise module %s init failed: %v", mod.Name(), err)
		}
	}
	preparePlanner = coreiface.IngestPreparePlannerHandle()
	publicationResources := serverPublicationResources{Vault: keyVault, Encryptor: assetEnc, Derived: derivedStore, PosterRoot: cfg.Data.Upload, ThumbnailRoot: filepath.Join(cfg.Data.Preview, "photos")}
	publicationPlanner = publication.NewPlanner(publication.PlanOptions{SubtitleAuto: cfg.SubtitleAutoOnScan(), ATrackAuto: cfg.ATrackAutoOnScan(), EncryptGlobal: cfg.EncryptedAssetsEnabled(), PreparePlanner: preparePlanner, Capabilities: publicationCapabilities, EncryptionValidator: publicationResources})
	warnings, err := PreparePublicationV2Startup(serverCtx, publicationV2StartupHooks{
		Preflight: func(ctx context.Context) ([]string, error) {
			return publication.PreflightPublicationV2(ctx, db, publicationPlanner, publicationCapabilities, publicationResources)
		},
		RecoverArtifacts: func(ctx context.Context) error { return recoverStartupArtifacts(ctx, db, startupRoots) },
		RecoverLeases:    func(ctx context.Context) error { return recoverStartupLeases(ctx, db, postIngestQueue) },
		ReplaceActiveV1: func(ctx context.Context) error {
			_, reconcileErr := publication.ReplaceActiveV1Runs(ctx, db, publicationPlanner)
			return reconcileErr
		},
		ValidateAggregateV2: func(ctx context.Context) error { return publication.ValidateAggregateCurrentV2(ctx, db) },
		StartClaimers: func() {
			go func() {
				err := dispatcher.Start(serverCtx)
				dispatcherDone <- err
				if err != nil && serverCtx.Err() == nil {
					log.Printf("post-ingest dispatcher stopped: %v", err)
					serverCancel()
				}
			}()
			for _, mod := range coreiface.EnterpriseModules {
				if starter, ok := mod.(interface{ StartWorkers(context.Context) }); ok {
					starter.StartWorkers(enterpriseCtx)
				}
			}
		},
		StartSubmissionSources: func() { close(startupReady) },
	})
	if err != nil {
		log.Fatalf("publication v2 startup: %v", err)
	}
	for _, warning := range warnings {
		log.Printf("publication v2 preflight warning: %.300s", warning)
	}
	if cfg.EncryptedAssetsEnabled() {
		go func() {
			if err := postingest.EnqueuePendingMediaEncryption(serverCtx, db, func(ctx context.Context, mediaID int64, scanTaskID *int64, _ postingest.TaskType) (bool, error) {
				return postIngestQueue.Enqueue(ctx, mediaID, scanTaskID, postingest.TaskEncrypt)
			}); err != nil && serverCtx.Err() == nil {
				log.Printf("asset encrypt: pending enqueue: %v", err)
			}
		}()
	}
	// (4) Scanner dependencies and the process-wide scan coordinator.
	sc := &scanner.Scanner{
		DB:            db,
		Vault:         keyVault,
		FFprobePath:   cfg.FFmpeg.FFprobePath,
		SkipHash:      !cfg.LibraryScanFileHash(),
		PhotoCacheDir: filepath.Join(cfg.Data.Preview, "photos"),
		CleanupRoots:  []string{cfg.Data.Dir, cfg.Data.Preview},
		FFprobeExtra:  ffprobeExtra,
	}
	sc.OnDocumentScanned = func(mediaID int64) { docCoverWorker.Enqueue(mediaID) }
	coordinator, err := scancoord.New(db, scancoord.Options{
		LeaseDuration: 60 * time.Second, HeartbeatInterval: 20 * time.Second,
		OwnerInstanceID: "scancoord-" + processID, Scanner: sc, Metrics: sqliteMetrics,

		OnMediaDiscoveredTx: scancoord.MediaDiscoveredTxFunc(postingest.NewScanMediaDiscoveredTxCallback(publicationPlanner)),
		OnMediaDiscovered: func() scancoord.MediaDiscoveredFunc {
			if !cfg.PrecapturePosterEnabled() {
				return nil
			}
			return scancoord.MediaDiscoveredFunc(postingest.NewScanMediaDiscoveredFinalizer(postingest.PreCaptureConfig{
				DB: db, Runner: posterRunner, Derived: derivedStore, UploadDir: cfg.Data.Upload, Timeout: cfg.PrecapturePosterTimeout(),
			}))
		}(),
		OnScanCancelled: func(_ context.Context, taskID int64) error {
			dispatcher.CancelScan(taskID)
			return nil
		},

		OnError: func(err error) { log.Printf("scan coordinator: %v", err) },
	})
	if err != nil {
		log.Fatalf("scan coordinator: %v", err)
	}
	finalizeRecoveryDone := make(chan struct{})
	go func() {
		defer close(finalizeRecoveryDone)
		recoverPending := func() {
			if _, err := scancoord.RecoverPendingFinalizations(serverCtx, db, 16); err != nil && serverCtx.Err() == nil {
				log.Printf("scan finalize recovery: %v", err)
			}
		}
		recoverPending()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-serverCtx.Done():
				return
			case <-ticker.C:
				recoverPending()
			}
		}
	}()

	// (5) Admin overview uses the shared resource-control instances.
	background := &handler.BackgroundGroup{}
	background.Go(serverCtx, func(ctx context.Context) {
		metadatalib.RunScrapeArtworkStageReconciler(ctx, db, cfg.Data.MetadataLibrary, time.Minute, 100, func(err error) { log.Printf("scrape artwork stage reconcile: %v", err) })
	})
	background.Go(serverCtx, func(ctx context.Context) {
		postingest.RunEncryptionStageReconciler(ctx, db, postingest.EncryptionRecoveryRoots{Quarantine: assetEnc.EncryptionPrivateRoot(), Resolver: assetEnc}, time.Minute, 100, func(err error) { log.Printf("encryption stage reconcile: %v", err) })
	})
	background.Go(serverCtx, func(ctx context.Context) {
		postingest.RunThumbnailStageReconciler(ctx, db, postingest.ThumbnailRecoveryRoots{Preview: filepath.Join(cfg.Data.Preview, "photos"), Derived: filepath.Join(cfg.Data.Dir, ".derived")}, time.Minute, 100, func(err error) { log.Printf("thumbnail stage reconcile: %v", err) })
	})
	background.Go(serverCtx, func(ctx context.Context) {
		postingest.RunPosterStageReconciler(ctx, db, postingest.PosterRecoveryRoots{Upload: cfg.Data.Upload, Derived: filepath.Join(cfg.Data.Dir, ".derived")}, time.Minute, 100, func(err error) { log.Printf("poster stage reconcile: %v", err) })
	})
	deps := handler.Dependencies{
		ServerContext: serverCtx, Background: background, StartupReady: startupReady,
		Coordinator: coordinator, Queue: postIngestQueue, PostIngest: postIngestEnqueuer, Dispatcher: dispatcher, AdminOverviewBuilder: func() handler.OverviewBuilder {
			b := handler.NewAdminOverviewBuilder(db, dispatcher, sqliteMetrics)
			b.Capabilities = publicationCapabilities
			return b
		}(),
		Worker: worker, PackageWorker: packageWorker, PreviewWorker: previewWorker, Subtitle: subSvc, Upload: up,
		Instant: instantScheduler, SessionManager: sessionMgr, AtrackWorker: atrackWorker, KeyframeWorker: keyframeWorker,
		LyricWorker: lyricWorker, PhotoClassifyWorker: photoClassifyWorker, DocCoverWorker: docCoverWorker,
		KeyVault: keyVault, AssetEncryptor: assetEnc, DerivedStore: derivedStore, PublicationPlanner: publicationPlanner, PublicationCapabilities: publicationCapabilities,
	}

	// (6) Handler dependencies are injected into the API router.
	engine := api.NewEngine(cfg, application, deps)

	// Repair runs only after post-ingest, scrape/API, and enterprise prepare workers exist.
	// ResetInterruptedTasks already ran, so a restarted current repair suppresses duplicates.
	repairPlanner := publicationPlanner
	background.Go(serverCtx, func(repairCtx context.Context) {
		repaired, repairErr := publication.RepairLegacyMedia(repairCtx, db, repairPlanner, 64)
		if repairErr != nil && repairCtx.Err() == nil {
			log.Printf("publication legacy repair: %v", repairErr)
			return
		}
		if repaired > 0 {
			log.Printf("publication legacy repair scheduled: %d media", repaired)
		}
	})

	// (7) Monitor submits through the same coordinator and starts last.
	mon := monitor.NewService(db, coordinator, 15*time.Second)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		mon.Start(serverCtx)
	}()

	// (8) Root cancellation stops monitor, scans, and dispatcher.
	httpServer := &http.Server{Addr: cfg.Addr(), Handler: engine}
	serverDone := make(chan error, 1)
	go func() {
		log.Printf("knox-media listening on http://%s", cfg.Addr())
		serverDone <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-serverDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server stopped: %v", err)
		}
		serverCancel()
	case <-serverCtx.Done():
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http server shutdown: %v", err)
	}
	serverCancel()
	select {
	case <-finalizeRecoveryDone:
	case <-shutdownCtx.Done():
		log.Printf("scan finalize recovery shutdown: %v", shutdownCtx.Err())
	}
	// Wait for monitor submission to stop before waiting on the Coordinator WaitGroup.
	select {
	case <-monitorDone:
	case <-shutdownCtx.Done():
		log.Printf("monitor shutdown: %v", shutdownCtx.Err())
	}
	if err := background.Wait(shutdownCtx); err != nil {
		log.Printf("handler background shutdown: %v", err)
	}
	if err := coordinator.ShutdownContext(shutdownCtx); err != nil {
		log.Printf("scan coordinator shutdown: %v", err)
	}
	select {
	case err := <-dispatcherDone:
		if err != nil {
			log.Printf("post-ingest dispatcher shutdown: %v", err)
		}
	case <-shutdownCtx.Done():
		log.Printf("post-ingest dispatcher shutdown: %v", shutdownCtx.Err())
	}
}

func buildDispatcherOptions(cfg *config.Config, owner string) postingest.DispatcherOptions {
	opts := postingest.DefaultDispatcherOptions()
	opts.OwnerID = owner
	opts.Global = cfg.PostIngest.MaxConcurrent
	opts.Poster = cfg.PostIngest.PosterMaxConcurrent
	opts.Preview = cfg.PostIngest.PreviewMaxConcurrent
	return opts
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

func startupBuildLog(info buildinfo.Info, identity store.SQLiteDBIdentity) string {
	return fmt.Sprintf("build_info %s db_path=%s schema_version=%d user_version=%d", info.String(), identity.Path, identity.SchemaRevision, identity.UserRevision)
}
