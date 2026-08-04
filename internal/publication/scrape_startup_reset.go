package publication

import (
	"context"
	"database/sql"
	"fmt"

	"knox-media/internal/store"
)

const (
	restartResetMessage           = "服务重启，任务已复位"
	scrapeRetriesExhaustedMessage = "服务重启，任务已耗尽重试次数"
)

// ResetInterruptedScrapeTasks resets in-flight scrape queue/step rows after process
// restart and finalizes linked plan projection in the same durable transaction.
// Exhausted retries become failed (full FinalizeNodeTransitionTx); recoverable
// running→waiting only recomputes plan completion.
func ResetInterruptedScrapeTasks(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("publication scrape restart reset: database is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	failRuns, err := scrapeRestartCandidateRuns(ctx, tx, true)
	if err != nil {
		return err
	}
	waitRuns, err := scrapeRestartCandidateRuns(ctx, tx, false)
	if err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, `UPDATE scrape_task SET status='failed', progress=100, finished_at=CURRENT_TIMESTAMP, message=?, lease_owner=NULL, lease_until=NULL WHERE status IN ('running','waiting') AND COALESCE(fail_count,0)>=?`, scrapeRetriesExhaustedMessage, DefaultNetworkMaxAttempts); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scrape_task SET status='waiting', progress=0, message=?, lease_owner=NULL, lease_until=NULL, available_at=COALESCE(available_at,CURRENT_TIMESTAMP) WHERE status='running' AND COALESCE(fail_count,0)<?`, restartResetMessage, DefaultNetworkMaxAttempts); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='failed', lease_owner=NULL, lease_until=NULL, last_error=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id IN (SELECT ingest_step_id FROM scrape_task WHERE status='failed' AND COALESCE(fail_count,0)>=? AND ingest_step_id IS NOT NULL) AND status IN ('running','waiting')`, scrapeRetriesExhaustedMessage, DefaultNetworkMaxAttempts); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='waiting', lease_owner=NULL, lease_until=NULL, available_at=(SELECT t.available_at FROM scrape_task t WHERE t.ingest_step_id=media_ingest_step.id AND t.status='waiting'), attempts=(SELECT COALESCE(t.fail_count,0) FROM scrape_task t WHERE t.ingest_step_id=media_ingest_step.id AND t.status='waiting'), last_error='', finished_at=NULL, updated_at=CURRENT_TIMESTAMP WHERE id IN (SELECT ingest_step_id FROM scrape_task WHERE status='waiting' AND ingest_step_id IS NOT NULL) AND status='running'`); err != nil {
		return err
	}

	seen := make(map[int64]struct{}, len(failRuns)+len(waitRuns))
	for _, runID := range failRuns {
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		if err := FinalizeNodeTransitionTx(ctx, tx, runID); err != nil {
			return err
		}
	}
	for _, runID := range waitRuns {
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		if err := RecomputePlanCompletionTx(ctx, tx, runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scrapeRestartCandidateRuns(ctx context.Context, tx store.SQLExecutor, exhausted bool) ([]int64, error) {
	var q string
	var args []any
	if exhausted {
		q = `SELECT DISTINCT ingest_run_id FROM scrape_task WHERE ingest_run_id IS NOT NULL AND status IN ('running','waiting') AND COALESCE(fail_count,0)>=?`
		args = []any{DefaultNetworkMaxAttempts}
	} else {
		q = `SELECT DISTINCT ingest_run_id FROM scrape_task WHERE ingest_run_id IS NOT NULL AND status='running' AND COALESCE(fail_count,0)<?`
		args = []any{DefaultNetworkMaxAttempts}
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
