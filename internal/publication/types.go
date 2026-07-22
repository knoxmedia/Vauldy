package publication

import "knox-media/internal/coreiface"

// State is the externally visible publication state of media and ingest runs.
type State string

const (
	StateProcessing State = "processing"
	StatePublished  State = "published"
	StateDegraded   State = "degraded"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
)

// StepType identifies one immutable operation in an ingest plan.
type StepType string

const (
	StepPoster   StepType = "poster"
	StepScrape   StepType = "scrape"
	StepPreview  StepType = "preview"
	StepKeyframe StepType = "keyframe"
	StepSubtitle StepType = "subtitle"
	StepAtrack   StepType = "atrack"
	StepEncrypt  StepType = "encrypt"
	StepPrepare  StepType = "prepare"
)

// PlanOptions are DB-independent capabilities captured by a Planner at construction.
type PlanOptions struct {
	SubtitleAuto  bool
	ATrackAuto    bool
	EncryptGlobal bool
	// PreparePlanner is the actual enterprise capability. Production planning
	// derives availability exclusively from this handle.
	PreparePlanner coreiface.IngestPreparePlanner
	// Capabilities gates planning of enterprise-backed publication steps.
	Capabilities coreiface.CapabilityRegistry
	// PrepareAvailable is retained only for legacy tests; without a planner it
	PrepareAvailable bool
}

// NewMedia identifies media discovered by a scan transaction.
type NewMedia struct {
	MediaID    int64
	ScanTaskID int64
	FileType   string
}

// Run is the immutable plan persisted for one media generation.
type Run struct {
	ID         int64
	MediaID    int64
	ScanTaskID int64
	LibraryID  int64
	Generation int64
	State      State
	Steps      []StepType
}

// ConfigSnapshot is the stable JSON payload persisted with an ingest run.
type ConfigSnapshot struct {
	LibraryID      int64      `json:"library_id"`
	FileType       string     `json:"file_type"`
	PreviewExtract bool       `json:"preview"`
	SubtitleAuto   bool       `json:"subtitle"`
	ATrackAuto     bool       `json:"atrack"`
	Encrypt        bool       `json:"encrypt"`
	Prepare        bool       `json:"prepare"`
	Steps          []StepType `json:"steps"`
}
