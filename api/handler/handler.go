package handler

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"knox-media/cmd/scheduler"
	"knox-media/internal/app"
	"knox-media/internal/atrack"
	"knox-media/internal/config"
	"knox-media/internal/coreiface"
	"knox-media/internal/doccover"
	"knox-media/internal/jit/session"
	"knox-media/internal/keyframe"
	"knox-media/internal/keystore"
	"knox-media/internal/lyrictask"
	"knox-media/internal/photoclass"
	"knox-media/internal/photoface"
	"knox-media/internal/photogeocode"
	"knox-media/internal/postingest"
	"knox-media/internal/preview"
	"knox-media/internal/publication"
	"knox-media/internal/scancoord"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
	"knox-media/internal/subtitle"
	"knox-media/internal/transcode"
	"knox-media/internal/upload"
)

type ScanCoordinator interface {
	Submit(context.Context, scancoord.ScanRequest) (scancoord.SubmitResult, error)
	Cancel(context.Context, int64) (scancoord.CancelResult, error)
}

type PostIngestEnqueuer interface {
	EnqueueMedia(context.Context, int64, *int64, string) ([]postingest.TaskType, error)
}

type Dependencies struct {
	ServerContext           context.Context
	Background              *BackgroundGroup
	Coordinator             ScanCoordinator
	Queue                   *postingest.Queue
	PostIngest              *postingest.Enqueuer
	Dispatcher              *postingest.Dispatcher
	AdminOverviewBuilder    OverviewBuilder
	Worker                  *transcode.Worker
	PackageWorker           *transcode.PackageWorker
	PreviewWorker           *preview.Worker
	Subtitle                *subtitle.Service
	Upload                  *upload.Service
	Instant                 *scheduler.Scheduler
	SessionManager          *session.Manager
	AtrackWorker            *atrack.Worker
	KeyframeWorker          *keyframe.Worker
	LyricWorker             *lyrictask.Worker
	PhotoClassifyWorker     *photoclass.Worker
	DocCoverWorker          *doccover.Worker
	KeyVault                *keystore.Vault
	AssetEncryptor          *storage.AssetEncryptor
	DerivedStore            *storage.DerivedAssetStore
	PublicationPlanner      *publication.Planner
	PublicationCapabilities coreiface.CapabilityRegistry
}

type Handler struct {
	App                     *app.App
	Worker                  *transcode.Worker
	PackageWorker           *transcode.PackageWorker
	PreviewWorker           *preview.Worker
	Subtitle                *subtitle.Service
	Upload                  *upload.Service
	Instant                 *scheduler.Scheduler
	SessionManager          *session.Manager
	AtrackWorker            *atrack.Worker
	KeyframeWorker          *keyframe.Worker
	LyricWorker             *lyrictask.Worker
	PhotoClassifyWorker     *photoclass.Worker
	PhotoGeocode            *photogeocode.Service
	PhotoLocationWorker     *photogeocode.Worker
	PhotoFaceWorker         *photoface.Worker
	DocCoverWorker          *doccover.Worker
	KeyVault                *keystore.Vault
	AssetEncryptor          *storage.AssetEncryptor
	DerivedStore            *storage.DerivedAssetStore
	PublicationPlanner      *publication.Planner
	PublicationCapabilities coreiface.CapabilityRegistry
	Queue                   *postingest.Queue
	PostIngestEnqueuer      PostIngestEnqueuer
	Dispatcher              *postingest.Dispatcher
	AdminOverviewBuilder    OverviewBuilder
	overviewStreamInterval  time.Duration
	overviewBuildTimeout    time.Duration
	ScanCoordinator         ScanCoordinator
	OnPostIngestError       func(error)
	Background              *BackgroundGroup
	ServerContext           context.Context
	PlayCompletionNow       func() time.Time
	libraryPreviewRefresh   func(context.Context, int64) error
	libraryPreviewScheduler *libraryPreviewScheduler
	scanMu                  sync.Mutex
	scrapeRunMu             sync.Mutex
	scrapeWithConfig        func(string, string, scraper.Config) (*scraper.ScrapeResult, error)
	runningScans            map[int64]scanRuntime
}

func New(a *app.App, deps Dependencies) *Handler {
	h := &Handler{
		App: a, Worker: deps.Worker, PackageWorker: deps.PackageWorker, PreviewWorker: deps.PreviewWorker,
		Subtitle: deps.Subtitle, Upload: deps.Upload, Instant: deps.Instant, SessionManager: deps.SessionManager,
		AtrackWorker: deps.AtrackWorker, KeyframeWorker: deps.KeyframeWorker, LyricWorker: deps.LyricWorker,
		PhotoClassifyWorker: deps.PhotoClassifyWorker, DocCoverWorker: deps.DocCoverWorker, KeyVault: deps.KeyVault,
		AssetEncryptor: deps.AssetEncryptor, DerivedStore: deps.DerivedStore, PublicationPlanner: deps.PublicationPlanner, PublicationCapabilities: deps.PublicationCapabilities, Queue: deps.Queue,
		PostIngestEnqueuer: deps.PostIngest, Dispatcher: deps.Dispatcher, AdminOverviewBuilder: deps.AdminOverviewBuilder, ScanCoordinator: deps.Coordinator,
		runningScans: map[int64]scanRuntime{}, Background: deps.Background, ServerContext: deps.ServerContext,
	}
	if deps.ServerContext != nil && deps.Background != nil {
		h.libraryPreviewScheduler = newLibraryPreviewScheduler(deps.ServerContext, deps.Background, libraryPreviewMaxConcurrent, libraryPreviewMaxPending, h.runLibraryPreviewRefresh)
	}
	if a == nil || a.DB == nil || a.Config == nil {
		return h
	}
	h.PhotoGeocode = photogeocode.New(a.DB)
	_ = h.PhotoGeocode.EnsureSchema()
	h.PhotoLocationWorker = photogeocode.NewWorker(a.DB, deps.KeyVault, h.PhotoGeocode)
	h.PhotoFaceWorker = photoface.NewWorker(a.DB, deps.KeyVault, deps.DerivedStore, filepath.Dir(a.ConfigPath), a.Config.FFmpeg.FFmpegPath, a.Config.Data.Preview, func() config.PhotoFaceConfig {
		cfg := a.Config.PhotoFace
		if strings.TrimSpace(cfg.PythonPath) == "" {
			cfg.PythonPath = a.Config.PhotoClassify.PythonPath
		}
		if strings.TrimSpace(cfg.ScriptPath) == "" {
			cfg.ScriptPath = "tools/photo_face/detect.py"
		}
		return cfg
	})
	return h
}

type scanRuntime struct {
	TaskID int64
	Cancel func()
}

func (h *Handler) logActivity(userID int64, username, action string, mediaID *int64, message string) {
	if h == nil || h.App == nil || h.App.DB == nil || action == "" {
		return
	}
	var uid any
	if userID > 0 {
		uid = userID
	}
	var mid any
	if mediaID != nil && *mediaID > 0 {
		mid = *mediaID
	}
	var uname any
	if username != "" {
		uname = username
	}
	var msg any
	if message != "" {
		msg = message
	}
	_, _ = h.App.DB.Exec(
		`INSERT INTO activity_log (user_id, username, action, media_id, message) VALUES (?, ?, ?, ?, ?)`,
		uid, uname, action, mid, msg,
	)
}

func nullInt64(v sql.NullInt64) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}
