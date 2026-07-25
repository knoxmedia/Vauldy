// Package coreiface defines the enterprise module contract shared between the
// community (Vauldy) and commercial (knox-media) repositories. The community
// build registers nil stubs; the commercial build injects real implementations
// via init() so the shared main.go entrypoint compiles in both trees.
package coreiface

import (
	"context"
	"database/sql"
	"sync"

	"github.com/gin-gonic/gin"

	"knox-media/internal/config"
	"knox-media/internal/keystore"
)

// RouterGroup is the minimal gin surface enterprise modules need to mount
// their own routes. gin.RouterGroup satisfies this interface.
type RouterGroup interface {
	GET(string, ...gin.HandlerFunc) gin.IRoutes
	POST(string, ...gin.HandlerFunc) gin.IRoutes
	PUT(string, ...gin.HandlerFunc) gin.IRoutes
	DELETE(string, ...gin.HandlerFunc) gin.IRoutes
	PATCH(string, ...gin.HandlerFunc) gin.IRoutes
	Group(string, ...gin.HandlerFunc) *gin.RouterGroup
	Use(...gin.HandlerFunc) gin.IRoutes
}

// WorkerScheduler is the minimal hook enterprise modules use to register
// background loops. The base app calls Start/Shutdown on lifecycle events.
type WorkerScheduler interface {
	Start(ctx context.Context)
	Shutdown(ctx context.Context) error
}

// ModuleDeps bundles shared dependencies handed to every enterprise module.
type ModuleDeps struct {
	DB     *sql.DB
	Config *config.Config
	Vault  *keystore.Vault
	// TranscodeDir is the configured output root for transcode artifacts.
	TranscodeDir string
	// FFmpegPath is the resolved ffmpeg binary path.
	FFmpegPath string
	// FFprobePath is the resolved ffprobe binary path.
	FFprobePath  string
	Capabilities CapabilityRegistry
}

// EnterpriseModule is the contract every commercial-only module implements.
// The community build leaves EnterpriseModules empty; the commercial build
// appends implementations in cmd/server/main.go init().
type EnterpriseModule interface {
	Name() string
	Init(ctx context.Context, deps ModuleDeps) error
	RegisterRoutes(r RouterGroup, deps ModuleDeps)
	RegisterWorkers(scheduler WorkerScheduler)
	Shutdown(ctx context.Context) error
}

// EnterpriseModules is the global registry populated by init() in the
// commercial main.go. It stays nil in the community build.
var EnterpriseModules []EnterpriseModule

// PretranscodeMod is the nil-by-default pretranscode capability handle.
// The commercial build assigns a real implementation in init(); the
// community build leaves it nil so play/delete paths skip pretranscode.
var pretranscodeModMu sync.RWMutex
var PretranscodeMod PretranscodeModule

func PretranscodeModuleHandle() PretranscodeModule {
	pretranscodeModMu.RLock()
	defer pretranscodeModMu.RUnlock()
	return PretranscodeMod
}
func SetPretranscodeModule(mod PretranscodeModule) {
	pretranscodeModMu.Lock()
	PretranscodeMod = mod
	pretranscodeModMu.Unlock()
}
func ClearPretranscodeModuleIfOwned(mod PretranscodeModule) {
	pretranscodeModMu.Lock()
	if PretranscodeMod == mod {
		PretranscodeMod = nil
	}
	pretranscodeModMu.Unlock()
}

// IngestPreparePlan is registered by the commercial pretranscode package at
// package initialization. It has no process or database dependency and remains
// nil when that package is absent from a community build.
var ingestPrepareMu sync.RWMutex
var IngestPreparePlan IngestPreparePlanner

func IngestPreparePlannerHandle() IngestPreparePlanner {
	ingestPrepareMu.RLock()
	defer ingestPrepareMu.RUnlock()
	return IngestPreparePlan
}

// RegisterIngestPreparePlanner installs the startup capability and returns a test-friendly restore closure.
func RegisterIngestPreparePlanner(planner IngestPreparePlanner) func() {
	ingestPrepareMu.Lock()
	previous := IngestPreparePlan
	IngestPreparePlan = planner
	ingestPrepareMu.Unlock()
	return func() {
		ingestPrepareMu.Lock()
		if IngestPreparePlan == planner {
			IngestPreparePlan = previous
		}
		ingestPrepareMu.Unlock()
	}
}

// RegisterEnterpriseModule appends a module to the global registry. Called
// from commercial init() functions; a no-op in the community build.
func RegisterEnterpriseModule(m EnterpriseModule) {
	EnterpriseModules = append(EnterpriseModules, m)
}
