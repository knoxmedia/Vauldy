package publication

import (
	"context"
	"fmt"

	"knox-media/internal/store"
)

const (
	parentRunFailedReason    = "cancelled: parent run failed"
	parentRunCancelledReason = "cancelled: parent run cancelled"
)

// convergeTerminalRunTx fences all unfinished execution belonging to one exact,
// non-superseded terminal run. Domain work descriptors (for example preview_task)
// are intentionally untouched because they are media-scoped rather than run-scoped.
func convergeTerminalRunTx(ctx context.Context, tx store.SQLExecutor, runID, generation int64, runStatus string) error {
	if tx == nil || runID <= 0 || generation <= 0 || (runStatus != "failed" && runStatus != "cancelled") {
		return fmt.Errorf("publication terminal convergence: invalid run")
	}
	reason := parentRunFailedReason
	if runStatus == "cancelled" {
		reason = parentRunCancelledReason
	}

	// Cancel rendition children before their parent task. Both updates clear leases,
	// so stale workers lose their commit fence when this transaction commits.
	hasRenditions, err := publicationTableExistsTx(ctx, tx, "pretranscode_rendition_job")
	if err != nil {
		return err
	}
	if hasRenditions {
		if _, err = tx.ExecContext(ctx, `UPDATE pretranscode_rendition_job SET status='cancelled',progress=100,error_message=?,lease_owner=NULL,lease_until=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE status IN ('waiting','running') AND task_id IN (SELECT id FROM transcode_task WHERE ingest_run_id=? AND generation=?)`, reason, runID, generation); err != nil {
			return fmt.Errorf("publication terminal convergence: rendition jobs: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error=?,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND generation=? AND status IN ('waiting','running')`, reason, runID, generation); err != nil {
		return fmt.Errorf("publication terminal convergence: post-ingest tasks: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scrape_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,message=?,progress=100,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND generation=? AND status IN ('waiting','running')`, reason, runID, generation); err != nil {
		return fmt.Errorf("publication terminal convergence: scrape tasks: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE transcode_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,error_message=?,progress=100,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND generation=? AND status IN ('waiting','running')`, reason, runID, generation); err != nil {
		return fmt.Errorf("publication terminal convergence: transcode tasks: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error=?,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND generation=? AND status IN ('waiting','running')`, reason, runID, generation); err != nil {
		return fmt.Errorf("publication terminal convergence: steps: %w", err)
	}
	return nil
}
