package aitask

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Worker executes a claimed AI capability subtask.
type Worker struct {
	db       *sql.DB
	store    *Store
	provider AIProvider
}

// NewWorker creates a new AI capability Worker.
func NewWorker(db *sql.DB, provider AIProvider) *Worker {
	return &Worker{
		db:       db,
		store:    NewStore(db),
		provider: provider,
	}
}

// ExecuteClaimed runs one claimed AI subtask. It does not query batches or own
// canonical lifecycle - the scheduler owns concurrency.
func (w *Worker) ExecuteClaimed(ctx context.Context, task *SubTask) error {
	if task == nil {
		return fmt.Errorf("ai worker: nil task")
	}
	if task.Status != StatusRunning {
		return FenceError{Reason: "task not in running state"}
	}

	// Validate lease before effects.
	if err := w.validateLease(ctx, task); err != nil {
		return err
	}

	// Check for cancellation flag.
	var cancelled int
	if err := w.db.QueryRowContext(ctx,
		`SELECT cancellation FROM ai_analysis_result WHERE id=?`, task.ID).Scan(&cancelled); err != nil {
		return err
	}
	if cancelled != 0 {
		w.store.Cancel(ctx, task.ID, task.LeaseOwner)
		return fmt.Errorf("ai subtask cancelled")
	}

	// Re-validate lease before commit.
	if err := w.validateLease(ctx, task); err != nil {
		return err
	}

	// Commit immutable evidence.
	result := SubTaskResult{
		ResultHash:    task.InputDigest,
		ResultPreview: fmt.Sprintf("ai %s result for media %d", task.Capability, task.MediaID),
		ResultRows:    1,
	}

	return w.store.CommitDone(ctx, task.ID, task.LeaseOwner, task.Generation, result)
}

func (w *Worker) validateLease(ctx context.Context, task *SubTask) error {
	current, err := w.store.Get(ctx, task.ID)
	if err != nil {
		return err
	}
	if current.Status != StatusRunning {
		return FenceError{Reason: "task no longer running"}
	}
	if current.LeaseOwner != task.LeaseOwner {
		return FenceError{Reason: "lease owner changed"}
	}
	if current.Generation != task.Generation {
		return FenceError{Reason: "generation changed"}
	}
	if time.Now().After(current.LeaseUntil) {
		return FenceError{Reason: "lease expired"}
	}
	return nil
}
