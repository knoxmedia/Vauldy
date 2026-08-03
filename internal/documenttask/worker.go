package documenttask

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

// Converter is the primitive document-to-PDF converter interface.
type Converter interface {
	ConvertToPDF(ctx context.Context, sourcePath, stagingDir string) (string, error)
}

// Worker executes a claimed document conversion task.
type Worker struct {
	db       *sql.DB
	store    *Store
	artifact *ArtifactManager
	conv     Converter
}

// NewWorker creates a new document conversion Worker.
func NewWorker(db *sql.DB, artifact *ArtifactManager, conv Converter) *Worker {
	return &Worker{
		db:       db,
		store:    NewStore(db),
		artifact: artifact,
		conv:     conv,
	}
}

// ExecuteClaimed converts one claimed task. It does not query batches or own
// canonical lifecycle - the scheduler owns concurrency.
func (w *Worker) ExecuteClaimed(ctx context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("document worker: nil task")
	}
	if task.Status != StatusRunning {
		return FenceError{Reason: "task not in running state"}
	}
	if w.conv == nil {
		return fmt.Errorf("document worker: no converter configured")
	}

	// Validate lease before effects.
	if err := w.validateLease(ctx, task); err != nil {
		return err
	}

	// Create staging directory.
	stagingDir := w.artifact.StagePath(task.MediaID)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		w.store.MarkFailed(ctx, task.ID, task.LeaseOwner, err.Error())
		return err
	}

	// Convert to PDF in staging.
	stagedPath, err := w.conv.ConvertToPDF(ctx, task.SourcePath, stagingDir)
	if err != nil {
		w.store.MarkFailed(ctx, task.ID, task.LeaseOwner, err.Error())
		return err
	}

	// Validate the staged PDF.
	output, err := w.artifact.ValidatePDF(stagedPath)
	if err != nil {
		w.store.MarkFailed(ctx, task.ID, task.LeaseOwner, err.Error())
		return err
	}

	// Re-validate lease before atomic commit.
	if err := w.validateLease(ctx, task); err != nil {
		return err
	}

	// Atomic commit.
	committedPath, err := w.artifact.Commit(ctx, task.MediaID, stagedPath)
	if err != nil {
		w.store.MarkFailed(ctx, task.ID, task.LeaseOwner, err.Error())
		return err
	}

	output.PDFPath = committedPath
	var engine EngineKind
	_ = w.db.QueryRowContext(ctx, `SELECT engine_kind FROM document_task WHERE id=?`, task.ID).Scan(&engine)

	return w.store.CommitDone(ctx, task.ID, task.LeaseOwner, task.Generation, output, engine)
}

func (w *Worker) validateLease(ctx context.Context, task *Task) error {
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
