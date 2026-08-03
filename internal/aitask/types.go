package aitask

import "time"

// Status is the unified lifecycle status for AI subtasks.
type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Capability identifies which AI analysis capability a subtask targets.
type Capability string

const (
	CapSummary       Capability = "summary"
	CapClassification Capability = "classification"
	CapTags          Capability = "tags"
)

// SubTask represents a durable AI capability subtask.
type SubTask struct {
	ID             int64
	MediaID        int64
	ParentTaskID   int64
	Capability     Capability
	Status         Status
	LeaseOwner     string
	LeaseUntil     time.Time
	Provider       string
	ProviderID     string
	Model          string
	ModelVersion   string
	InputDigest    string
	Generation     int64
	RetryRound     int
	Attempts       int
	MaxAttempts    int
	Progress       float64
	Cancellation   bool
	ResultHash     string
	ResultPreview  string
	ResultRows     int
	LastError      string
	Prerequisites  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// FenceError is returned when a generation or lease fence is breached.
type FenceError struct {
	Reason string
}

func (e FenceError) Error() string {
	return "ai task fence: " + e.Reason
}

// NotFoundError indicates a subtask was not found.
type NotFoundError struct {
	ID int64
}

func (e NotFoundError) Error() string {
	return "ai subtask not found"
}

// DuplicateError indicates an idempotent duplicate was detected.
type DuplicateError struct {
	ExistingTaskID int64
}

func (e DuplicateError) Error() string {
	return "duplicate ai subtask"
}

// StaleError indicates a stale replacement was rejected.
type StaleError struct {
	CurrentGeneration int64
	RequestGeneration int64
}

func (e StaleError) Error() string {
	return "stale ai subtask request"
}

// SiblingError indicates a sibling subtask was overwritten by accident.
type SiblingError struct {
	Capability Capability
	Reason     string
}

func (e SiblingError) Error() string {
	return "ai sibling conflict: " + e.Reason
}

// SubTaskInput is the input for enqueuing an AI capability subtask.
type SubTaskInput struct {
	MediaID       int64
	ParentTaskID  int64
	Capability    Capability
	Provider      string
	ProviderID    string
	Model         string
	ModelVersion  string
	InputDigest   string
	Generation    int64
	Prerequisites string
}

// SubTaskResult is the output of a successful AI capability execution.
type SubTaskResult struct {
	ResultHash    string
	ResultPreview string
	ResultRows    int
}

// ClaimResult is the result of claiming a subtask.
type ClaimResult struct {
	Task       SubTask
	LeaseUntil time.Time
}
