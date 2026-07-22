package postingest

import (
	"context"
	"database/sql"

	"knox-media/internal/publication"
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

type ScanMediaDiscoveredTxFunc func(context.Context, *sql.Tx, int64, int64, string, string) error

// NewScanMediaDiscoveredTxCallback adapts the publication planner to a scan
// transaction while preserving scan task ownership.
func NewScanMediaDiscoveredTxCallback(planner MediaPlanner) ScanMediaDiscoveredTxFunc {
	return func(ctx context.Context, tx *sql.Tx, taskID, mediaID int64, _ string, fileType string) error {
		_, err := planner.PlanNewMediaTx(ctx, tx, publication.NewMedia{
			MediaID: mediaID, ScanTaskID: taskID, FileType: fileType,
		})
		return err
	}
}
