package pretranscode

import (
	"context"
	"database/sql"
	"errors"
	"knox-media/internal/store"
)

// RecoverExpiredPrepareParents atomically resets current expired parents or cancels stale ones.
func RecoverExpiredPrepareParents(ctx context.Context, db *sql.DB, limit int) (int, error) {
	if db == nil {
		return 0, errors.New("prepare recovery database required")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM transcode_task WHERE status='running' AND ingest_run_id IS NOT NULL AND ingest_step_id IS NOT NULL AND generation IS NOT NULL AND lease_until<CURRENT_TIMESTAMP ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	changed := 0
	for _, id := range ids {
		_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
			var current int
			err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_task t JOIN media_ingest_step s ON s.id=t.ingest_step_id JOIN media_ingest_run r ON r.id=t.ingest_run_id JOIN media m ON m.id=t.media_id WHERE t.id=? AND t.status='running' AND t.lease_until<CURRENT_TIMESTAMP AND s.run_id=t.ingest_run_id AND s.media_id=t.media_id AND s.generation=t.generation AND s.status='running' AND s.lease_owner=t.lease_owner AND r.media_id=t.media_id AND r.generation=t.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=t.generation`, id).Scan(&current)
			if err != nil {
				return err
			}
			status := "waiting"
			jobStatus := "waiting"
			if current != 1 {
				status = "cancelled"
				jobStatus = "cancelled"
			}
			r, err := tx.ExecContext(ctx, `UPDATE transcode_task SET status=?,lease_owner=NULL,lease_until=NULL,completed_at=CASE WHEN ?='cancelled' THEN CURRENT_TIMESTAMP ELSE NULL END WHERE id=? AND status='running' AND lease_until<CURRENT_TIMESTAMP`, status, status, id)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n != 1 {
				return nil
			}
			r, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status=?,lease_owner=NULL,lease_until=NULL,finished_at=CASE WHEN ?='cancelled' THEN CURRENT_TIMESTAMP ELSE NULL END,updated_at=CURRENT_TIMESTAMP WHERE id=(SELECT ingest_step_id FROM transcode_task WHERE id=?) AND status='running'`, status, status, id)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n != 1 {
				return errors.New("prepare recovery step mismatch")
			}
			_, err = tx.ExecContext(ctx, `UPDATE pretranscode_rendition_job SET status=?,lease_owner=NULL,lease_until=NULL,started_at=CASE WHEN ?='waiting' THEN NULL ELSE started_at END,completed_at=CASE WHEN ?='cancelled' THEN CURRENT_TIMESTAMP ELSE completed_at END WHERE task_id=? AND status='running'`, jobStatus, jobStatus, jobStatus, id)
			if err == nil {
				changed++
			}
			return err
		})
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}
