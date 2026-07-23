package publication

import (
	"context"
	"database/sql"
	"fmt"
)

// AggregateTx reconciles a run and, when it is current, its media visibility.
func AggregateTx(ctx context.Context, tx *sql.Tx, runID int64) error {
	if tx == nil || runID <= 0 {
		return fmt.Errorf("publication aggregate: invalid transaction or run")
	}
	var mediaID, generation int64
	var preserve int
	var runState, terminalReason, runError string
	var supersededBy sql.NullInt64
	var supersededAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT media_id,generation,status,preserve_visibility,terminal_reason,error_message,superseded_by_generation,superseded_at FROM media_ingest_run WHERE id=?`, runID).Scan(&mediaID, &generation, &runState, &preserve, &terminalReason, &runError, &supersededBy, &supersededAt); err != nil {
		return err
	}
	if supersededBy.Valid || supersededAt.Valid {
		return nil
	}
	var pending, failed, cancelled int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(status IN ('waiting','running')),0),COALESCE(SUM(status='failed'),0),COALESCE(SUM(status='cancelled'),0) FROM media_ingest_step WHERE run_id=? AND required=1`, runID).Scan(&pending, &failed, &cancelled); err != nil {
		return err
	}
	next := runState
	switch {
	case runState == "cancelled":
		next = "cancelled"
	case runState == "failed":
		next = "failed"
	case pending > 0:
		next = "processing"
	case failed > 0 || cancelled > 0:
		if preserve == 1 {
			next = "degraded"
		} else {
			next = "failed"
		}
	default:
		next = "published"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_ingest_run SET status=?,error_message=CASE WHEN ?='cancelled' THEN error_message WHEN ? IN ('degraded','failed') THEN COALESCE(NULLIF((SELECT last_error FROM media_ingest_step WHERE run_id=? AND required=1 AND status IN ('failed','cancelled') AND NULLIF(last_error,'') IS NOT NULL ORDER BY CASE status WHEN 'failed' THEN 0 ELSE 1 END,id LIMIT 1),''),'required step exhausted') ELSE '' END,finished_at=CASE WHEN ? IN ('published','degraded','failed','cancelled') THEN COALESCE(finished_at,CURRENT_TIMESTAMP) ELSE NULL END WHERE id=?`, next, next, next, runID, next, runID); err != nil {
		return err
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&current); err != nil {
		return err
	}
	if current != generation {
		return nil
	}
	if next == "processing" && preserve == 1 {
		return nil
	}
	if next == "published" {
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='published',published_at=COALESCE(published_at,CURRENT_TIMESTAMP),publication_error='' WHERE id=?`, mediaID)
		return err
	}
	if next == "degraded" {
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='degraded',publication_error=COALESCE(NULLIF((SELECT last_error FROM media_ingest_step WHERE run_id=? AND required=1 AND status IN ('failed','cancelled') AND NULLIF(last_error,'') IS NOT NULL ORDER BY CASE status WHEN 'failed' THEN 0 ELSE 1 END,id LIMIT 1),''),'required step exhausted') WHERE id=?`, runID, mediaID)
		return err
	}
	if next == "cancelled" {
		if preserve == 1 {
			_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='degraded',publication_error=COALESCE(NULLIF(?,''),'cancelled') WHERE id=?`, terminalReason, mediaID)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='cancelled',published_at=NULL,publication_error=COALESCE(NULLIF(?,''),'cancelled') WHERE id=?`, terminalReason, mediaID)
		return err
	}
	if next == "failed" {
		_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='failed',published_at=NULL,publication_error=COALESCE(NULLIF((SELECT last_error FROM media_ingest_step WHERE run_id=? AND required=1 AND status IN ('failed','cancelled') AND NULLIF(last_error,'') IS NOT NULL ORDER BY CASE status WHEN 'failed' THEN 0 ELSE 1 END,id LIMIT 1),''),'required step exhausted') WHERE id=?`, runID, mediaID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE media SET publication_state='processing',publication_error='' WHERE id=?`, mediaID)
	return err
}
