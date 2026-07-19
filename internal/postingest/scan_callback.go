package postingest

import "context"

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
