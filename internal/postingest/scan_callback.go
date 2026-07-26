package postingest

import (
	"context"
	"database/sql"

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

// NewScanMediaDiscoveredTxWithPrecaptureCallback returns a scan callback that
// plans the ingest run AND synchronously captures the poster before the
// transaction commits. If poster capture fails, the error propagates to the
// scanner, which rolls back the transaction.
func NewScanMediaDiscoveredTxWithPrecaptureCallback(planner MediaPlanner, precapture PreCaptureConfig) ScanMediaDiscoveredTxFunc {
	return func(ctx context.Context, tx *sql.Tx, taskID int64, discovery scanner.ScanDiscovery) error {
		run, err := planner.PlanNewMediaTx(ctx, tx, publication.NewMedia{
			MediaID: discovery.MediaID, ScanTaskID: taskID, FileType: discovery.FileType,
			MetadataAttempt: publication.MetadataAttempt{
				Attempted: discovery.MetadataAttempt.Attempted,
				Fields:    append([]string(nil), discovery.MetadataAttempt.Fields...),
				Errors:    metadataDiagnostics(discovery.MetadataAttempt.Errors),
			},
		})
		if err != nil {
			return err
		}
		if discovery.FileType != "video" || run.ID == 0 {
			return nil
		}
		return PreCapturePoster(ctx, tx, discovery.MediaID, run, precapture)
	}
}

func metadataDiagnostics(in []scanner.MetadataDiagnostic) []publication.MetadataDiagnostic {
	out := make([]publication.MetadataDiagnostic, len(in))
	for i, diagnostic := range in {
		out[i] = publication.MetadataDiagnostic{Source: diagnostic.Source, Message: diagnostic.Message}
	}
	return out
}
