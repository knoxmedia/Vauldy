package medialibrary

import (
	"database/sql"
	"strings"
)

// StreamPolicy describes per-media library streaming / encryption behavior.
type StreamPolicy struct {
	DRMEnabled         bool
	EncryptionMode     string // standard | powerdrm | drm
	JITPrepareOnIngest bool
}

// LoadStreamPolicy reads library flags for a media row.
func LoadStreamPolicy(db *sql.DB, mediaID int64) (StreamPolicy, error) {
	var p StreamPolicy
	if db == nil || mediaID <= 0 {
		return p, nil
	}
	var drm, jit int
	var mode string
	err := db.QueryRow(`
		SELECT COALESCE(l.drm_enabled,0), COALESCE(l.encryption_mode,'drm'), COALESCE(l.jit_prepare_on_ingest,0)
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&drm, &mode, &jit)
	if err != nil {
		return p, err
	}
	p.DRMEnabled = drm == 1
	p.JITPrepareOnIngest = jit == 1
	p.EncryptionMode = NormalizeEncryptionMode(mode)
	return p, nil
}

// NormalizeEncryptionMode returns standard | powerdrm | drm.
func NormalizeEncryptionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "standard", "hls_aes_128", "aes_128":
		return "standard"
	case "powerdrm":
		return "powerdrm"
	default:
		return "drm"
	}
}

// PrepackageOnIngest is false when transport stream real-time encryption is enabled.
func (p StreamPolicy) PrepackageOnIngest() bool {
	return !p.DRMEnabled
}

// PlaybackPlanMode maps encryption_mode to HLSInfo / player plan mode when stream DRM is on.
func (p StreamPolicy) PlaybackPlanMode() string {
	if !p.DRMEnabled {
		return ""
	}
	switch p.EncryptionMode {
	case "standard":
		return "hls_aes_128"
	case "powerdrm":
		return "hls_powerdrm"
	default:
		return "hls_drm"
	}
}
