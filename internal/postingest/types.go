package postingest

import (
	"context"
	"database/sql"
	"time"

	"knox-media/internal/coreiface"
	"knox-media/internal/store"
)

type TaskType string

const (
	TaskPoster            TaskType = "poster"
	TaskPosterRepair      TaskType = "poster_repair"
	TaskThumbnail         TaskType = "thumbnail"
	TaskPreview           TaskType = "preview"
	TaskKeyframe          TaskType = "keyframe"
	TaskSubtitle          TaskType = "subtitle"
	TaskAtrack            TaskType = "atrack"
	TaskEncrypt           TaskType = "encrypt"
	TaskSubtitleRecognize TaskType = "subtitle_recognize"
	TaskAIAnalysis        TaskType = "ai_analysis"
)

type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type FailureKind int

const (
	FailureRetryable FailureKind = iota
	FailurePermanent
	FailureCancelled
	FailureShutdown
)

type Task struct {
	ID          int64
	MediaID     int64
	ScanTaskID  *int64
	RunID       *int64
	StepID      *int64
	Type        TaskType
	Status      Status
	Attempts    int
	MaxAttempts int
	Generation  int64
	RetryRound  int
	LeaseOwner  string
	LeaseUntil  time.Time
	LastError   string
	// Scheduler admission metadata frozen at enqueue.
	SourceClass            int
	BasePriority           int
	LibraryID              *int64
	ResourceProfileVersion int
	ResourceProfileJSON    string
}

type Queue struct {
	db                   *sql.DB
	owner                string
	metrics              *store.SQLiteMetrics
	isScanCancelled      func(context.Context, int64) (bool, error)
	beforeFailTransition func()
	registry             coreiface.CapabilityRegistry
	// immediateTx overrides store.WithImmediateConnTx for tests (ambiguous commit seams).
	immediateTx func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error)
}

type compatibilityCapabilities struct{}

func (compatibilityCapabilities) Available(string) bool { return true }

func NewQueue(db *sql.DB, owner string, metrics *store.SQLiteMetrics, registries ...coreiface.CapabilityRegistry) *Queue {
	var registry coreiface.CapabilityRegistry = compatibilityCapabilities{}
	if len(registries) > 0 {
		registry = registries[0]
	}
	return &Queue{db: db, owner: owner, metrics: metrics, registry: registry}
}

func (q *Queue) withImmediate(ctx context.Context, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
	if q != nil && q.immediateTx != nil {
		return q.immediateTx(ctx, q.db, fn)
	}
	return store.WithImmediateConnTx(ctx, q.db, fn)
}
