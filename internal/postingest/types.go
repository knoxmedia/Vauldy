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
	TaskPoster       TaskType = "poster"
	TaskPosterRepair TaskType = "poster_repair"
	TaskThumbnail    TaskType = "thumbnail"
	TaskPreview      TaskType = "preview"
	TaskKeyframe     TaskType = "keyframe"
	TaskSubtitle     TaskType = "subtitle"
	TaskAtrack       TaskType = "atrack"
	TaskEncrypt      TaskType = "encrypt"
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
	LeaseOwner  string
	LeaseUntil  time.Time
	LastError   string
}

type Queue struct {
	db                   *sql.DB
	owner                string
	metrics              *store.SQLiteMetrics
	isScanCancelled      func(context.Context, int64) (bool, error)
	beforeFailTransition func()
	registry             coreiface.CapabilityRegistry
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
