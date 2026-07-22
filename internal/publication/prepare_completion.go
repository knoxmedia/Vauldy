package publication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CompletePrepareTx is the completion contract for enterprise prepare
// executors. It fences the exact immutable link, transitions the required step,
// and aggregates the run in the caller transaction.
func CompletePrepareTx(ctx context.Context, tx *sql.Tx, runID, stepID, generation int64, success bool, lastError string) error {
	if tx == nil || runID <= 0 || stepID <= 0 || generation <= 0 {
		return errors.New("publication prepare completion: invalid linkage")
	}
	status := "done"
	if !success {
		status = "failed"
		if lastError == "" {
			lastError = "prepare failed"
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status=?,attempts=CASE WHEN ?='failed' THEN max_attempts ELSE attempts END,last_error=?,finished_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND generation=? AND step_type='prepare' AND status IN ('waiting','running')`, status, status, lastError, stepID, runID, generation)
	if err != nil {
		return fmt.Errorf("publication prepare completion: update step: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("publication prepare completion: linked step mismatch or already terminal")
	}
	if err = AggregateTx(ctx, tx, runID); err != nil {
		return fmt.Errorf("publication prepare completion: aggregate: %w", err)
	}
	return nil
}
