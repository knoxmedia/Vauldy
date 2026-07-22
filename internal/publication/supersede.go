package publication

import (
	"context"
	"database/sql"
	"fmt"

	"knox-media/internal/store"
)

const supersededTerminalReason = "superseded_by_policy_v2"

func publicationTableExistsTx(ctx context.Context, q store.SQLExecutor, table string) (bool, error) {
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
		return false, err
	}
	return exists == 1, nil
}

func execSupersedeTx(ctx context.Context, tx *sql.Tx, label, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("publication supersede: %s: %w", label, err)
	}
	if _, err = result.RowsAffected(); err != nil {
		return fmt.Errorf("publication supersede: read %s affected rows: %w", label, err)
	}
	return nil
}

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
	metaExists, err := publicationTableExistsTx(ctx, tx, "pretranscode_task_meta")
	if err != nil {
		return fmt.Errorf("publication supersede: inspect pretranscode metadata table: %w", err)
	}
	jobsExist, err := publicationTableExistsTx(ctx, tx, "pretranscode_rendition_job")
	if err != nil {
		return fmt.Errorf("publication supersede: inspect pretranscode jobs table: %w", err)
	}
	if metaExists != jobsExist {
		return fmt.Errorf("publication supersede: partial enterprise schema: metadata=%t jobs=%t", metaExists, jobsExist)
	}
	if err = execSupersedeTx(ctx, tx, "update run", `UPDATE media_ingest_run SET
status=CASE WHEN status='processing' THEN 'cancelled' ELSE status END,
terminal_reason=CASE WHEN status='processing' THEN ? ELSE terminal_reason END,
superseded_by_generation=?,superseded_at=COALESCE(superseded_at,CURRENT_TIMESTAMP),
finished_at=CASE WHEN status='processing' THEN COALESCE(finished_at,CURRENT_TIMESTAMP) ELSE finished_at END,
updated_at=CURRENT_TIMESTAMP WHERE id=?`, supersededTerminalReason, newGeneration, runID); err != nil {
		return err
	}
	statements := []struct {
		label string
		query string
		args  []any
	}{
		{"cancel ingest steps", `UPDATE media_ingest_step SET status='cancelled',lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE run_id=? AND generation=? AND status IN ('waiting','running')`, []any{runID, oldGeneration}},
		{"cancel post-ingest tasks", `UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND generation=? AND status IN ('waiting','running')`, []any{runID, oldGeneration}},
		{"cancel scrape tasks", `UPDATE scrape_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND generation=? AND status IN ('waiting','running')`, []any{runID, oldGeneration}},
	}
	if jobsExist {
		statements = append(statements, struct {
			label string
			query string
			args  []any
		}{"cancel pretranscode jobs", `UPDATE pretranscode_rendition_job SET status='cancelled',lease_owner=NULL,lease_until=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE task_id IN (SELECT id FROM transcode_task WHERE ingest_run_id=? AND generation=?) AND status IN ('waiting','running')`, []any{runID, oldGeneration}})
	}
	statements = append(statements, struct {
		label string
		query string
		args  []any
	}{"cancel transcode tasks", `UPDATE transcode_task SET status='cancelled',lease_owner=NULL,lease_until=NULL,completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE ingest_run_id=? AND generation=? AND status IN ('waiting','running')`, []any{runID, oldGeneration}})
	for _, statement := range statements {
		if err = execSupersedeTx(ctx, tx, statement.label, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}
