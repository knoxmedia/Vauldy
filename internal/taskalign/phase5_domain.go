package taskalign

import "time"

// SubjectKind classifies what entity a task operates on.
type SubjectKind string

const (
	SubjectMedia  SubjectKind = "media"
	SubjectPerson SubjectKind = "person"
)

// Phase5TaskType enumerates the 11 frozen task types for the unified orchestrator.
type Phase5TaskType struct {
	Key              string
	Family           string
	SubjectKind      SubjectKind
	MaxRetries       int
	Timeout          time.Duration
	Cancellable      bool
	CapabilitySubtask bool // only ai_analysis may have multiple capability subtasks
	EncryptedSource  bool
	Available        bool
	ProjectionSource string
}

// Phase5Domain encodes the full lifecycle matrix for the frozen Phase 5 registry.
type Phase5Domain struct {
	Types []Phase5TaskType
}

// AllPhase5Types returns the canonical, ordered enumeration of the 11 Phase 5
// task types per the frozen specification.
func AllPhase5Types() []Phase5TaskType {
	return []Phase5TaskType{
		{
			Key: "lyric_recognize", Family: "audio_processing",
			SubjectKind: SubjectMedia, MaxRetries: 3, Timeout: 10 * time.Minute,
			Cancellable: true, EncryptedSource: true, Available: true,
			ProjectionSource: "lyric_task",
		},
		{
			Key: "audio_analysis", Family: "audio_processing",
			SubjectKind: SubjectMedia, MaxRetries: 3, Timeout: 10 * time.Minute,
			Cancellable: true, EncryptedSource: true, Available: false,
			ProjectionSource: "audio_analysis_task",
		},
		{
			Key: "photo_classify", Family: "image_processing",
			SubjectKind: SubjectMedia, MaxRetries: 3, Timeout: 5 * time.Minute,
			Cancellable: true, EncryptedSource: true, Available: true,
			ProjectionSource: "photo_classify_task",
		},
		{
			Key: "photo_geocode", Family: "image_processing",
			SubjectKind: SubjectMedia, MaxRetries: 3, Timeout: 5 * time.Minute,
			Cancellable: true, EncryptedSource: true, Available: true,
			ProjectionSource: "photo_geocode_task",
		},
		{
			Key: "photo_face", Family: "image_processing",
			SubjectKind: SubjectMedia, MaxRetries: 3, Timeout: 10 * time.Minute,
			Cancellable: true, EncryptedSource: true, Available: true,
			ProjectionSource: "photo_face_task",
		},
		{
			Key: "image_ocr", Family: "image_processing",
			SubjectKind: SubjectMedia, MaxRetries: 3, Timeout: 10 * time.Minute,
			Cancellable: true, EncryptedSource: true, Available: false,
			ProjectionSource: "image_ocr_task",
		},
		{
			Key: "document_convert", Family: "document_processing",
			SubjectKind: SubjectMedia, MaxRetries: 3, Timeout: 15 * time.Minute,
			Cancellable: true, EncryptedSource: true, Available: false,
			ProjectionSource: "document_convert_task",
		},
		{
			Key: "document_fulltext", Family: "document_processing",
			SubjectKind: SubjectMedia, MaxRetries: 3, Timeout: 30 * time.Minute,
			Cancellable: true, EncryptedSource: true, Available: false,
			ProjectionSource: "document_fulltext_task",
		},
		{
			Key: "ai_analysis", Family: "media_ingestion",
			SubjectKind: SubjectMedia, MaxRetries: 2, Timeout: 15 * time.Minute,
			Cancellable: true, EncryptedSource: false, Available: true,
			CapabilitySubtask: true,
			ProjectionSource:  "post_ingest_task",
		},
		{
			Key: "person_scrape", Family: "system",
			SubjectKind: SubjectPerson, MaxRetries: 3, Timeout: 5 * time.Minute,
			Cancellable: true, EncryptedSource: false, Available: false,
			ProjectionSource: "person_scrape_task",
		},
		{
			Key: "artwork_cover", Family: "media_ingestion",
			SubjectKind: SubjectMedia, MaxRetries: 3, Timeout: 5 * time.Minute,
			Cancellable: true, EncryptedSource: true, Available: false,
			ProjectionSource: "artwork_cover_task",
		},
	}
}

// Phase5Subtasks returns the set of capability subtasks permitted exclusively
// for ai_analysis.
func Phase5Subtasks() []string {
	return []string{"summary", "classification", "tags"}
}

// ValidStatuses returns the six frozen lifecycle statuses.
func ValidStatuses() []string {
	return []string{"waiting", "running", "done", "failed", "cancelled", "skipped"}
}

// NonTerminalStatuses returns the statuses that are not final.
func NonTerminalStatuses() []string {
	return []string{"waiting", "running"}
}

// TerminalStatuses returns the final statuses.
func TerminalStatuses() []string {
	return []string{"done", "failed", "cancelled", "skipped"}
}

// NoCapableWorkerStatus returns the status exposed when no capable worker exists.
const NoCapableWorkerStatus = "no_capable_worker"
