package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"knox-media/internal/publication"
	"knox-media/internal/scanner"
)

type MediaEnqueuer interface {
	EnqueueMedia(context.Context, int64, *int64, string) ([]TaskType, error)
}

type ScanMediaAddedFunc func(context.Context, int64, int64, string, string) error

// NewScanMediaAddedEnqueueCallback returns the production scan callback. Its
// dependency exposes only enqueueing, so scan discovery cannot synchronously
// launch post-ingest executors or external media processes.
func NewScanMediaAddedEnqueueCallback(enqueuer MediaEnqueuer) ScanMediaAddedFunc {
	return func(ctx context.Context, taskID, mediaID int64, _ string, fileType string) error {
		_, err := enqueuer.EnqueueMedia(ctx, mediaID, &taskID, fileType)
		return err
	}
}

type MediaPlanner interface {
	PlanNewMediaTx(context.Context, *sql.Tx, publication.NewMedia) (publication.Run, error)
}

type ScanMediaDiscoveredTxFunc func(context.Context, *sql.Tx, int64, scanner.ScanDiscovery) error
type ScanMediaDiscoveredFunc func(context.Context, int64, scanner.ScanDiscovery) error

// NewScanMediaDiscoveredTxCallback adapts the publication planner to a scan
// transaction while preserving scan task ownership.
func NewScanMediaDiscoveredTxCallback(planner MediaPlanner) ScanMediaDiscoveredTxFunc {
	return func(ctx context.Context, tx *sql.Tx, taskID int64, discovery scanner.ScanDiscovery) error {
		_, err := planner.PlanNewMediaTx(ctx, tx, publication.NewMedia{
			MediaID: discovery.MediaID, ScanTaskID: taskID, FileType: discovery.FileType,
			MetadataAttempt: publication.MetadataAttempt{
				Attempted: discovery.MetadataAttempt.Attempted,
				Fields:    append([]string(nil), discovery.MetadataAttempt.Fields...),
				Errors:    metadataDiagnostics(discovery.MetadataAttempt.Errors),
			},
		})
		return err
	}
}

// NewScanMediaDiscoveredFinalizer captures and finalizes the current plan after scanner commit.
func NewScanMediaDiscoveredFinalizer(cfg PreCaptureConfig) ScanMediaDiscoveredFunc {
	return func(ctx context.Context, taskID int64, discovery scanner.ScanDiscovery) error {
		if !strings.EqualFold(strings.TrimSpace(discovery.FileType), "video") {
			return nil
		}
		if cfg.DB == nil {
			return errors.New("precapture finalizer: database is not configured")
		}
		var run publication.Run
		err := cfg.DB.QueryRowContext(ctx, `SELECT r.id,r.media_id,r.scan_task_id,m.library_id,r.generation,r.status FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.media_id=? AND r.scan_task_id=? AND r.status='processing' AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL`, discovery.MediaID, taskID).Scan(&run.ID, &run.MediaID, &run.ScanTaskID, &run.LibraryID, &run.Generation, &run.State)
		if err != nil {
			return fmt.Errorf("precapture finalizer: load current run: %w", err)
		}
		captured, captureErr := CapturePoster(ctx, discovery.MediaID, run, cfg)
		if captureErr == nil {
			captureErr = FinalizeCapturedPoster(ctx, cfg.DB, captured)
		}
		if captureErr == nil {
			return nil
		}
		if rejectErr := RejectCapturedPoster(context.Background(), cfg.DB, capturedOrRun(captured, run)); rejectErr != nil {
			return errors.Join(captureErr, fmt.Errorf("precapture finalizer: reject media: %w", rejectErr))
		}
		return captureErr
	}
}

func capturedOrRun(captured CapturedPoster, run publication.Run) CapturedPoster {
	if captured.MediaID == 0 {
		captured.MediaID, captured.RunID, captured.Generation = run.MediaID, run.ID, run.Generation
	}
	return captured
}

func metadataDiagnostics(in []scanner.MetadataDiagnostic) []publication.MetadataDiagnostic {
	out := make([]publication.MetadataDiagnostic, len(in))
	for i, diagnostic := range in {
		out[i] = publication.MetadataDiagnostic{Source: diagnostic.Source, Message: diagnostic.Message}
	}
	return out
}
