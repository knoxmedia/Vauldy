package publication

import (
	"context"
	"database/sql"
	"fmt"
)

// CancelRunTx records an explicit whole-run cancellation before fencing all
// outstanding work. The caller owns the transaction so intent and work
// cancellation commit or roll back together.
func CancelRunTx(ctx context.Context, tx *sql.Tx, runID int64, reason string) (bool, error) {
	if tx == nil || runID <= 0 || reason == "" {
		return false, fmt.Errorf("publication cancel: invalid transaction, run, or reason")
	}
	res, err := tx.ExecContext(ctx, `UPDATE media_ingest_run SET status='cancelled',terminal_reason=?,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='processing' AND superseded_by_generation IS NULL AND superseded_at IS NULL AND EXISTS(SELECT 1 FROM media m WHERE m.id=media_ingest_run.media_id AND m.ingest_generation=media_ingest_run.generation)`, reason, runID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE media_ingest_step SET status='cancelled',last_error=CASE WHEN last_error='' THEN ? ELSE last_error END,lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND status IN ('waiting','running')`, []any{reason, runID}},
		{`UPDATE post_ingest_task SET status='cancelled',last_error=CASE WHEN last_error='' THEN ? ELSE last_error END,lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND status IN ('waiting','running')`, []any{reason, runID}},
		{`UPDATE scrape_task SET status='cancelled',message=CASE WHEN COALESCE(message,'')='' THEN ? ELSE message END,lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND status IN ('waiting','running')`, []any{reason, runID}},
		{`UPDATE pretranscode_rendition_job SET status='cancelled',error_message=CASE WHEN COALESCE(error_message,'')='' THEN ? ELSE error_message END,lease_owner=NULL,lease_until=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE task_id IN (SELECT id FROM transcode_task WHERE ingest_run_id=?) AND status IN ('waiting','running')`, []any{reason, runID}},
		{`UPDATE transcode_task SET status='cancelled',error_message=CASE WHEN COALESCE(error_message,'')='' THEN ? ELSE error_message END,lease_owner=NULL,lease_until=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND status IN ('waiting','running','paused')`, []any{reason, runID}},
	}
	for _, stmt := range statements {
		if _, err = tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			return false, err
		}
	}
	if err = AggregateTx(ctx, tx, runID); err != nil {
		return false, err
	}
	return true, nil
}
