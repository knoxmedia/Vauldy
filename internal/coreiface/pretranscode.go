package coreiface

import (
	"context"

	"knox-media/internal/store"
)

// PretranscodeModule is the playback-facing capability contract for the
// pretranscode subsystem. The community build registers nil; commercial
// build injects the real implementation.
type PretranscodeModule interface {
	// GetPretranscodeStatus returns aggregated status for a media file_id,
	// including available renditions and encryption mode. Returns
	// Available=false when no pretranscode output exists.
	GetPretranscodeStatus(ctx context.Context, fileID string) (*PretranscodeStatus, error)
	// GetMasterPlaylist returns the HLS master.m3u8 path for a pretranscoded
	// file, or empty string when none exists.
	GetMasterPlaylist(ctx context.Context, fileID string) (string, error)
	// HasPretranscodeOutput returns true when at least one rendition for the
	// file is in done state.
	HasPretranscodeOutput(fileID string) bool
	// OnMediaDeleted cascades cleanup of pretranscode tasks, rendition jobs,
	// and output files when a media item is removed.
	OnMediaDeleted(ctx context.Context, mediaID int64, fileIDs []string) error
}

// PretranscodeStatus mirrors SRS 1.5.3 PretranscodeStatus.
type PretranscodeStatus struct {
	Available    bool
	PresetName   string
	Renditions   []RenditionStatus
	Encryption   string
	OutputFormat string
}

// RenditionStatus mirrors SRS 1.5.3 RenditionStatus.
type RenditionStatus struct {
	Name     string
	Status   string
	Progress int
}

// IngestPreparePlanner is the narrow transaction-only capability used by the
// publication planner. Implementations must create executable work linked to
// the supplied immutable ingest run and step within the caller transaction.
type IngestPreparePlanner interface {
	PlanIngestPrepareTx(ctx context.Context, tx store.SQLExecutor, mediaID, runID, stepID, generation int64) error
}

// CapabilityRegistry is the narrow contract for checking registered
// publication capabilities.
type CapabilityRegistry interface {
	Available(step string) bool
}
