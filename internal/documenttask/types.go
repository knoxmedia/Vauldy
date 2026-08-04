package documenttask

import (
	"time"
)

// Status is the unified lifecycle status for document conversion tasks.
type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// EngineKind identifies the conversion engine.
type EngineKind string

const (
	EngineOffice      EngineKind = "office"
	EngineWPS         EngineKind = "wps"
	EngineLibreOffice EngineKind = "libreoffice"
)

// Task represents a durable document conversion task.
type Task struct {
	ID          int64
	MediaID     int64
	Status      Status
	LeaseOwner  string
	LeaseUntil  time.Time
	Generation  int64
	RetryRound  int
	Attempts    int
	MaxAttempts int
	SourcePath  string
	SourceHash  string
	EngineKind  EngineKind
	OutputPath  string
	OutputSize  int64
	OutputHash  string
	PageCount   int
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
}

// PreviewEvidence is the committed artifact evidence for document_preview_pdf.
type PreviewEvidence struct {
	MediaID     int64
	SourcePath  string
	SourceHash  string
	PDFPath     string
	PDFSize     int64
	PDFHash     string
	PageCount   int
	EngineKind  EngineKind
	Generation  int64
	CommittedAt time.Time
}

// ConvertInput is the input for a document conversion.
type ConvertInput struct {
	MediaID    int64
	SourcePath string
	Generation int64
}

// ConvertOutput is the output of a successful conversion.
type ConvertOutput struct {
	PDFPath   string
	PDFSize   int64
	PDFHash   string
	PageCount int
}

// ClaimResult is the result of claiming a conversion task.
type ClaimResult struct {
	Task      Task
	LeaseUntil time.Time
}

// LeaseGuard is a function that validates the lease is still valid before effects.
type LeaseGuard func() error

// FenceError is returned when a generation or lease fence is breached.
type FenceError struct {
	Reason string
}

func (e FenceError) Error() string {
	return "document task fence: " + e.Reason
}

// NotFoundError indicates a task was not found.
type NotFoundError struct {
	MediaID int64
}

func (e NotFoundError) Error() string {
	return "document task not found"
}

// DuplicateError indicates an idempotent duplicate was detected.
type DuplicateError struct {
	ExistingTaskID int64
}

func (e DuplicateError) Error() string {
	return "duplicate document conversion task"
}

// StaleError indicates a stale replacement was rejected.
type StaleError struct {
	CurrentGeneration int64
	RequestGeneration int64
}

func (e StaleError) Error() string {
	return "stale document conversion request"
}
