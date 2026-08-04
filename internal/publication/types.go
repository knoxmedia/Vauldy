package publication

import (
	"context"
	"knox-media/internal/coreiface"
	"knox-media/internal/libraryprocessing"
)

type PlanReason string

const (
	PlanReasonScan           PlanReason = "scan"
	PlanReasonRepair         PlanReason = "repair"
	PlanReasonManualRetry    PlanReason = "manual_retry"
	PlanReasonEvent          PlanReason = "event"
	PlanReasonUpload         PlanReason = "upload"
	PlanReasonSourceReplaced PlanReason = "source_replaced"
)

type ReplacementOptions struct {
	Reason             PlanReason
	PreserveVisibility bool
	ExpectedGeneration int64
}
type ReplacementResult struct {
	Run                          Run
	OldGeneration, NewGeneration int64
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
	StepPoster            StepType = "poster"
	StepThumbnail         StepType = "thumbnail"
	StepScrape            StepType = "scrape"
	StepPreview           StepType = "preview"
	StepKeyframe          StepType = "keyframe"
	StepSubtitle          StepType = "subtitle"
	StepAtrack            StepType = "atrack"
	StepEncrypt           StepType = "encrypt"
	StepPrepare           StepType = "prepare"
	StepPackage           StepType = "package"
	StepPretranscode      StepType = "pretranscode"
	StepMetadata          StepType = "metadata"
	StepMediaVisible      StepType = "media_visible"
	StepSubtitleExtract   StepType = "subtitle_extract"
	StepAtrackExtract     StepType = "atrack_extract"
	StepSubtitleRecognize StepType = "subtitle_recognize"
	StepKeyframeExtract   StepType = "keyframe_extract"
	StepAIAnalysis        StepType = "ai_analysis"
	// Phase 5 media task types
	StepLyricRecognize   StepType = "lyric_recognize"
	StepAudioAnalysis    StepType = "audio_analysis"
	StepPhotoClassify    StepType = "photo_classify"
	StepPhotoGeocode     StepType = "photo_geocode"
	StepPhotoFace        StepType = "photo_face"
	StepImageOCR         StepType = "image_ocr"
	StepDocumentConvert  StepType = "document_convert"
	StepDocumentFulltext StepType = "document_fulltext"
	StepPersonScrape     StepType = "person_scrape"
	StepArtworkCover     StepType = "artwork_cover"
)
const (
	PolicyV2             = 2
	PolicyV3             = 3
	PolicyV4             = 4
	CurrentPolicyVersion = PolicyV3
)

type DependencyKind string

const (
	DependencySuccess  DependencyKind = "success"
	DependencyTerminal DependencyKind = "terminal"
)

type Dependency struct {
	Step                StepType       `json:"step"`
	Kind                DependencyKind `json:"kind"`
	DependsOn           *StepType      `json:"depends_on,omitempty"`
	Generation          int64          `json:"generation,omitempty"`
	DependsOnGeneration int64          `json:"depends_on_generation,omitempty"`
}
type PlanNode struct {
	Step       StepType `json:"step"`
	Generation int64    `json:"generation"`
	Required   bool     `json:"required"`
}
type PlanGraph struct {
	Nodes []PlanNode   `json:"nodes"`
	Edges []Dependency `json:"edges"`
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
type EncryptedSourceStrategy string

const (
	EncryptedSourceStreamDecrypt   EncryptedSourceStrategy = "stream_decrypt"
	EncryptedSourceMaterializeTemp EncryptedSourceStrategy = "materialize_temp"
	EncryptedSourceDerivative      EncryptedSourceStrategy = "encrypted_derivative"
)

type EncryptedSourceContract struct {
	Strategy  EncryptedSourceStrategy `json:"strategy"`
	Validated bool                    `json:"validated"`
}
type EncryptedSourceRegistry interface {
	Contract(StepType) (EncryptedSourceContract, bool)
}
type EncryptionCleanupBasis struct {
	Encryption      bool `json:"encryption"`
	Package         bool `json:"package"`
	CleanupEligible bool `json:"cleanup_eligible"`
}
type ExecutableTaskAdapter interface {
	TaskType() StepType
	Execute(context.Context, int64) error
}
type ExecutableAdapterRegistry interface {
	Adapter(StepType) (ExecutableTaskAdapter, bool)
}

type PlanOptions struct {
	SubtitleAuto              bool
	ATrackAuto                bool
	EncryptGlobal             bool
	PreparePlanner            coreiface.IngestPreparePlanner
	Capabilities              coreiface.CapabilityRegistry
	ExecutableAdapters        ExecutableAdapterRegistry
	PrepareAvailable          bool
	EncryptionValidator       EncryptionPolicyValidator
	EncryptedSourceStrategies EncryptedSourceRegistry
}
// SourceClass reflects the canonical priority tier for scheduler admission.
// Values are ordered so higher numbers win scheduling races: manual/run-now > upload > scan > repair.
type SourceClass int

const (
	SourceClassScheduled      SourceClass = 100 // scheduled/repair/backfill
	SourceClassManualScan     SourceClass = 200 // manual scan
	SourceClassUploadDiscovery SourceClass = 300 // upload/discovery
	SourceClassManualRunNow   SourceClass = 400 // manual/run-now
)

// SourceClassFromReason maps a plan reason and origin to its canonical SourceClass.
func SourceClassFromReason(reason PlanReason, ingestItemID int64) SourceClass {
	switch reason {
	case PlanReasonManualRetry:
		return SourceClassManualRunNow
	case PlanReasonUpload, PlanReasonEvent:
		return SourceClassUploadDiscovery
	case PlanReasonScan:
		if ingestItemID > 0 {
			return SourceClassUploadDiscovery
		}
		return SourceClassManualScan
	case PlanReasonRepair, PlanReasonSourceReplaced:
		return SourceClassScheduled
	default:
		return SourceClassScheduled
	}
}

// BasePriority returns the canonical base priority for a SourceClass.
func (sc SourceClass) BasePriority() int { return int(sc) }

// ResourceProfile is a lightweight descriptor profile version and resource request snapshot.
type ResourceProfile struct {
	PolicyVersion int   `json:"policy_version"`
	LibraryID     int64 `json:"library_id"`
}

type NewMedia struct {
	MediaID, ScanTaskID int64
	IngestItemID        int64
	FileType            string
	MetadataAttempt     MetadataAttempt
}
type Run struct {
	ID, MediaID, ScanTaskID, IngestItemID, LibraryID, Generation int64
	State                                                        State
	Steps                                                        []StepType
}
type ConfigSnapshot struct {
	PolicyVersion        int                          `json:"policy_version"`
	LibraryID            int64                        `json:"library_id"`
	FileType             string                       `json:"file_type"`
	ProcessingExplicit   libraryprocessing.Options    `json:"processing_explicit"`
	ProcessingEffective  libraryprocessing.Options    `json:"processing_effective"`
	ProcessingProvenance libraryprocessing.Provenance `json:"processing_provenance"`
	// LegacyOptionDefaults explains compatibility-selected work without changing library-derived provenance.
	LegacyOptionDefaults      []string                             `json:"legacy_option_defaults"`
	EncryptedSourceStrategies map[StepType]EncryptedSourceContract `json:"encrypted_source_strategies"`
	CleanupBasis              EncryptionCleanupBasis               `json:"cleanup_basis"`
	PreviewExtract            bool                                 `json:"preview"`
	SubtitleAuto              bool                                 `json:"subtitle"`
	ATrackAuto                bool                                 `json:"atrack"`
	Encrypt                   bool                                 `json:"encrypt"`
	Prepare                   bool                                 `json:"prepare"`
	Steps                     []StepType                           `json:"steps"`
	Metadata                  MetadataAttempt                      `json:"metadata"`
	RequiredSteps             []StepType                           `json:"required_steps"`
	OptionalSteps             []StepType                           `json:"optional_steps"`
	Dependencies              []Dependency                         `json:"dependencies"`
	Graph                     PlanGraph                            `json:"graph"`
}
