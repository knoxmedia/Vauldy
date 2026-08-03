package documenttask

import (
	"context"
	"database/sql"
	"fmt"
)

// Adapter wraps document conversion in a postingest-compatible adapter.
type Adapter struct {
	db     *sql.DB
	store  *Store
	worker *Worker
}

// NewAdapter creates a postingest adapter for document conversion.
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

// Execute runs a claimed document conversion task.
func (a *Adapter) Execute(ctx context.Context, qt QueueTask) error {
	if a.worker == nil {
		return fmt.Errorf("document adapter: no worker configured")
	}

	task, err := a.store.Get(ctx, qt.ID)
	if err != nil {
		return fmt.Errorf("document adapter: %w", err)
	}

	if task.Status != StatusRunning {
		return FenceError{Reason: "task not running"}
	}
	if task.LeaseOwner != qt.LeaseOwner {
		return FenceError{Reason: "lease owner mismatch"}
	}

	return a.worker.ExecuteClaimed(ctx, task)
}
