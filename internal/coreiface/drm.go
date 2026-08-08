package coreiface

import (
	"sync"

	"github.com/gin-gonic/gin"
)

// DRMStreamEncryption configures play-time AES-128 HLS segment encryption for
// a JIT session. It mirrors internal/jit/session.StreamEncryption so the
// coreiface contract does not couple the shared playback code to the jit
// session package.
type DRMStreamEncryption struct {
	// Mode is standard | powerdrm | drm.
	Mode        string
	KidHex      string
	KeyHex      string
	KeyInfoPath string
}

// DRMModule is the commercial-only DRM capability contract. The community
// build leaves the handle nil so /drm/* routes are not mounted, DRM packaging
// is rejected, and playback paths stay clear.
type DRMModule interface {
	// StreamEncryption materializes play-time AES-128 key material for a JIT
	// session of a drm_enabled library. Returns nil, nil when DRM is disabled.
	StreamEncryption(mediaID int64, drmEnabled bool, encryptionMode, sessionDir string) (*DRMStreamEncryption, error)
	// PlaylistKeyLine renders the EXT-X-KEY directive for an encrypted stream,
	// or "" when the stream is not DRM-encrypted.
	PlaylistKeyLine(base string, mediaID int64, enc *DRMStreamEncryption, accessToken string) string
	// RewriteManifestsToPowerDRM rewrites packaged manifests to PowerDRM key
	// tags. Only meaningful for powerdrm-mode package tasks.
	RewriteManifestsToPowerDRM(outDir string, kidHex string) error
	// RegisterRoutes mounts the /drm/* license/key routes and the admin DRM
	// audit routes. drmPlay and adm are the authenticated playback and admin
	// router groups (resolved to /api/v1/drm/* and /api/v1/admin/drm-*).
	RegisterRoutes(drmPlay, adm *gin.RouterGroup)
}

// DRMMod is the nil-by-default DRM capability handle. The commercial build
// assigns a real implementation when the handler is constructed; the community
// build leaves it nil so all DRM paths degrade to clear/no-DRM behavior.
var (
	drmModMu sync.RWMutex
	DRMMod   DRMModule
)

func DRMModuleHandle() DRMModule {
	drmModMu.RLock()
	defer drmModMu.RUnlock()
	return DRMMod
}

func SetDRMModule(mod DRMModule) {
	drmModMu.Lock()
	DRMMod = mod
	drmModMu.Unlock()
}

func ClearDRMModuleIfOwned(mod DRMModule) {
	drmModMu.Lock()
	if DRMMod == mod {
		DRMMod = nil
	}
	drmModMu.Unlock()
}
