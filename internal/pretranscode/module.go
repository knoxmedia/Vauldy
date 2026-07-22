package pretranscode

import (
	"context"
	"database/sql"
	"log"
	"sync"

	"knox-media/internal/coreiface"
)

// Module is the commercial pretranscode enterprise module. It owns the
// preset/task/webhook services, the standalone worker, and the playback
// adapter that satisfies coreiface.PretranscodeModule.
type Module struct {
	Preset   *PresetService
	Task     *TaskService
	Webhook  *WebhookService
	Worker   *Worker
	Playback *PlaybackService

	cancel     context.CancelFunc
	dispatcher WebhookDispatcher
}

// NewModule returns an unconfigured module. Init() wires the DB and starts
// the worker goroutine.
func NewModule() *Module { return &Module{} }

func (m *Module) Name() string { return "pretranscode" }

// activeModule is the singleton set in Init(); handlers reach it via
// ActiveModule().
var moduleGlobalsMu sync.RWMutex
var activeModule *Module

// ActiveModule returns the initialized pretranscode module, or nil when the
// commercial build has not yet initialized it (e.g. in unit tests that don't
// run Init). Handler code must nil-check the result.
func ActiveModule() *Module {
	moduleGlobalsMu.RLock()
	defer moduleGlobalsMu.RUnlock()
	return activeModule
}

func (m *Module) Init(ctx context.Context, deps coreiface.ModuleDeps) error {
	if m.cancel != nil {
		m.cancel()
	}
	moduleGlobalsMu.Lock()
	activeModule = m
	m.Preset = &PresetService{DB: deps.DB}
	m.Task = &TaskService{DB: deps.DB, TranscodeDir: deps.TranscodeDir}
	m.Webhook = &WebhookService{DB: deps.DB}
	m.Playback = &PlaybackService{DB: deps.DB, TranscodeDir: deps.TranscodeDir}
	m.Worker = NewWorker(deps.DB, deps.Vault, deps.FFmpegPath, deps.TranscodeDir, 4, 2)
	m.dispatcher = &WebhookDispatcherAdapter{Service: m.Webhook}
	setWebhookDispatcher(m.dispatcher)
	coreiface.SetPretranscodeModule(m.Playback)
	moduleGlobalsMu.Unlock()

	// Recover orphaned tasks (waiting tasks with no rendition jobs).
	if n := m.Task.RecoverOrphanedTasks(); n > 0 {
		log.Printf("pretranscode recovered %d orphaned task(s)", n)
	}
	if n := m.Task.RepairStuckWaitingTasks(); n > 0 {
		log.Printf("pretranscode repaired %d stuck waiting rendition job(s)", n)
	}

	workerCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	go m.Worker.Start(workerCtx)
	log.Printf("pretranscode module initialized: transcode_dir=%s", deps.TranscodeDir)
	return nil
}

func (m *Module) RegisterRoutes(r coreiface.RouterGroup, deps coreiface.ModuleDeps) {
	// Route registration is performed by api/handler/pretranscode.go via the
	// handler layer so it can bind to Handler dependencies. The module only
	// exposes its services through the global PretranscodeMod handle.
}

func (m *Module) RegisterWorkers(s coreiface.WorkerScheduler) {
	// Worker is started in Init() because it needs the deps bound.
}

func (m *Module) Shutdown(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	moduleGlobalsMu.Lock()
	if activeModule == m {
		activeModule = nil
		coreiface.ClearPretranscodeModuleIfOwned(m.Playback)
		if CurrentWebhookDispatcher() == m.dispatcher {
			setWebhookDispatcher(nil)
		}
	}
	moduleGlobalsMu.Unlock()
	return nil
}

// DB helper for handler layer access.
func (m *Module) DB() *sql.DB {
	if m.Task != nil {
		return m.Task.DB
	}
	return nil
}

func init() {
	coreiface.RegisterEnterpriseModule(NewModule())
}
