package aitask

import (
	"context"
	"database/sql"
	"fmt"
)

// Adapter wraps AI capability subtasks in a postingest-compatible adapter.
type Adapter struct {
	db     *sql.DB
	store  *Store
	worker *Worker
}

// NewAdapter creates a postingest adapter for AI capability subtasks.
func NewAdapter(db *sql.DB, worker *Worker) *Adapter {
	return &Adapter{
		db:     db,
		store:  NewStore(db),
		worker: worker,
	}
}

// QueueTask is the task shape passed by the postingest dispatcher.
type QueueTask struct {
	ID          int64
	MediaID     int64
	TaskType    string
	Status      string
	LeaseOwner  string
	Generation  int64
	RetryRound  int
	Attempts    int
	MaxAttempts int
}

// Execute runs a claimed AI capability subtask.
func (a *Adapter) Execute(ctx context.Context, qt QueueTask) error {
	if a.worker == nil {
		return fmt.Errorf("ai adapter: no worker configured")
	}

	task, err := a.store.Get(ctx, qt.ID)
	if err != nil {
		return fmt.Errorf("ai adapter: %w", err)
	}

	if task.Status != StatusRunning {
		return FenceError{Reason: "task not running"}
	}
	if task.LeaseOwner != qt.LeaseOwner {
		return FenceError{Reason: "lease owner mismatch"}
	}

	return a.worker.ExecuteClaimed(ctx, task)
}

// EnqueueAllCapabilities enqueues all three capability subtasks (summary,
// classification, tags) for a given media item. Each is independently keyed
// and has no inter-capability prerequisites.
func (a *Adapter) EnqueueAllCapabilities(ctx context.Context, input SubTaskInput) ([]SubTask, error) {
	caps := []Capability{CapSummary, CapClassification, CapTags}
	var tasks []SubTask
	for _, cap := range caps {
		input.Capability = cap
		task, err := a.store.Enqueue(ctx, input)
		if err != nil {
			if _, ok := err.(DuplicateError); ok {
				continue
			}
			return tasks, err
		}
		tasks = append(tasks, *task)
	}
	return tasks, nil
}
