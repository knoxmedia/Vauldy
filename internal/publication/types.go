package publication

import "knox-media/internal/coreiface"

type PlanReason string

const (
	PlanReasonScan        PlanReason = "scan"
	PlanReasonRepair      PlanReason = "repair"
	PlanReasonManualRetry PlanReason = "manual_retry"
)

type ReplacementOptions struct {
	Reason             PlanReason
	PreserveVisibility bool
	ExpectedGeneration int64
}

type ReplacementResult struct {
	Run           Run
	OldGeneration int64
	NewGeneration int64
}

type State string

const (
	StateProcessing State = "processing"
	StatePublished  State = "published"
	StateDegraded   State = "degraded"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
)

type StepType string

const (
	StepPoster    StepType = "poster"
	StepThumbnail StepType = "thumbnail"
	StepScrape    StepType = "scrape"
	StepPreview   StepType = "preview"
	StepKeyframe  StepType = "keyframe"
	StepSubtitle  StepType = "subtitle"
	StepAtrack    StepType = "atrack"
	StepEncrypt   StepType = "encrypt"
	StepPrepare   StepType = "prepare"
)
const PolicyV2 = 2

type DependencyKind string

const (
	DependencyStepDone     DependencyKind = "step_done"
	DependencyMediaVisible DependencyKind = "media_visible"
)

type Dependency struct {
	Step      StepType       `json:"step"`
	Kind      DependencyKind `json:"kind"`
	DependsOn *StepType      `json:"depends_on,omitempty"`
}
type MetadataDiagnostic struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}
type MetadataAttempt struct {
	Attempted bool                 `json:"attempted"`
	Fields    []string             `json:"fields"`
	Errors    []MetadataDiagnostic `json:"errors"`
}
type PlanOptions struct {
	SubtitleAuto        bool
	ATrackAuto          bool
	EncryptGlobal       bool
	PreparePlanner      coreiface.IngestPreparePlanner
	Capabilities        coreiface.CapabilityRegistry
	PrepareAvailable    bool
	EncryptionValidator EncryptionPolicyValidator
}
type NewMedia struct {
	MediaID         int64
	ScanTaskID      int64
	FileType        string
	MetadataAttempt MetadataAttempt
}
type Run struct {
	ID         int64
	MediaID    int64
	ScanTaskID int64
	LibraryID  int64
	Generation int64
	State      State
	Steps      []StepType
}
type ConfigSnapshot struct {
	PolicyVersion  int             `json:"policy_version"`
	LibraryID      int64           `json:"library_id"`
	FileType       string          `json:"file_type"`
	PreviewExtract bool            `json:"preview"`
	SubtitleAuto   bool            `json:"subtitle"`
	ATrackAuto     bool            `json:"atrack"`
	Encrypt        bool            `json:"encrypt"`
	Prepare        bool            `json:"prepare"`
	Steps          []StepType      `json:"steps"`
	Metadata       MetadataAttempt `json:"metadata"`
	RequiredSteps  []StepType      `json:"required_steps"`
	OptionalSteps  []StepType      `json:"optional_steps"`
	Dependencies   []Dependency    `json:"dependencies"`
}
