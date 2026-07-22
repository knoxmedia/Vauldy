package publication

import (
	"context"
	"database/sql"
	"fmt"
)

const supersededTerminalReason = "superseded_by_policy_v2"

// SupersedeGenerationTx fences every active worker linked to an ingest generation.
// Terminal run and task outcomes remain immutable; only supersession metadata is added.
func SupersedeGenerationTx(ctx context.Context, tx *sql.Tx, mediaID, oldGeneration, newGeneration int64) error {
	if tx == nil || mediaID <= 0 || oldGeneration < 0 || newGeneration <= oldGeneration {
		return fmt.Errorf("publication supersede: invalid transaction or generation")
	}
	if oldGeneration == 0 {
		return nil
	}
	var runID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM media_ingest_run WHERE media_id=? AND generation=?`, mediaID, oldGeneration).Scan(&runID); err != nil {
		return fmt.Errorf("publication supersede: load old run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_ingest_run SET
status=CASE WHEN status='processing' THEN 'cancelled' ELSE status END,
terminal_reason=CASE WHEN status='processing' THEN ? ELSE terminal_reason END,
superseded_by_generation=?,superseded_at=COALESCE(superseded_at,CURRENT_TIMESTAMP),
finished_at=CASE WHEN status='processing' THEN COALESCE(finished_at,CURRENT_TIMESTAMP) ELSE finished_at END,
updated_at=CURRENT_TIMESTAMP WHERE id=?`, supersededTerminalReason, newGeneration, runID); err != nil {
		return fmt.Errorf("publication supersede: update run: %w", err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE media_ingest_step SET status='cancelled',lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND generation=? AND status IN ('waiting','running')`, []any{runID, oldGeneration}},
		{`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND generation=? AND status IN ('waiting','running')`, []any{runID, oldGeneration}},
		{`UPDATE scrape_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND generation=? AND status IN ('waiting','running')`, []any{runID, oldGeneration}},
		{`UPDATE pretranscode_rendition_job SET status='cancelled',lease_owner=NULL,lease_until=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE task_id IN (SELECT id FROM transcode_task WHERE ingest_run_id=? AND generation=?) AND status IN ('waiting','running')`, []any{runID, oldGeneration}},
		{`UPDATE transcode_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND generation=? AND status IN ('waiting','running')`, []any{runID, oldGeneration}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("publication supersede: cancel linked work: %w", err)
		}
	}
	return nil
}
