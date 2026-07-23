package publication

import (
	"context"
	"errors"
	"fmt"

	"knox-media/internal/store"
)

func CompletePrepareTx(ctx context.Context, tx store.SQLExecutor, parent PrepareParentIdentity, success bool, lastError string) error {
	if tx == nil || parent.TaskID <= 0 || parent.RunID <= 0 || parent.StepID <= 0 || parent.MediaID <= 0 || parent.Generation <= 0 {
		return errors.New("publication prepare completion: invalid identity")
	}
	status := "done"
	if !success {
		status = "failed"
		if lastError == "" {
			lastError = "prepare failed"
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE transcode_task SET status=?,error_message=?,completed_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL WHERE id=? AND media_id=? AND ingest_run_id=? AND ingest_step_id=? AND generation=? AND ((status='running' AND lease_owner=?) OR EXISTS(SELECT 1 FROM media_ingest_run r WHERE r.id=? AND r.policy_version=1))`, status, lastError, parent.TaskID, parent.MediaID, parent.RunID, parent.StepID, parent.Generation, parent.Owner, parent.RunID)
	if err != nil {
		return fmt.Errorf("publication prepare completion: update parent: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("publication prepare completion: parent ownership or identity mismatch")
	}
	res, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status=?,attempts=CASE WHEN ?='failed' THEN max_attempts ELSE attempts END,last_error=?,finished_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND media_id=? AND generation=? AND step_type='prepare' AND ((status='running' AND lease_owner=?) OR EXISTS(SELECT 1 FROM media_ingest_run r WHERE r.id=? AND r.policy_version=1))`, status, status, lastError, parent.StepID, parent.RunID, parent.MediaID, parent.Generation, parent.Owner, parent.RunID)
	if err != nil {
		return fmt.Errorf("publication prepare completion: update step: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("publication prepare completion: step ownership or identity mismatch")
	}
	return AggregateTx(ctx, tx, parent.RunID)
}
