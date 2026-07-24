package pretranscode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"knox-media/internal/publication"
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
			var runID, mediaID, generation int64
			var current, superseded int
			if err := tx.QueryRowContext(ctx, `SELECT t.ingest_run_id,t.media_id,t.generation,CASE WHEN m.ingest_generation=t.generation THEN 1 ELSE 0 END,CASE WHEN r.superseded_at IS NOT NULL OR r.superseded_by_generation IS NOT NULL THEN 1 ELSE 0 END FROM transcode_task t JOIN media_ingest_run r ON r.id=t.ingest_run_id JOIN media m ON m.id=t.media_id WHERE t.id=? AND t.status='running' AND t.lease_until<CURRENT_TIMESTAMP`, id).Scan(&runID, &mediaID, &generation, &current, &superseded); err != nil {
				return err
			}
			if current == 0 && superseded == 0 {
				return fmt.Errorf("prepare recovery task %d generation mismatch without supersession", id)
			}
			status := "waiting"
			if superseded == 1 {
				status = "cancelled"
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
			_, err = tx.ExecContext(ctx, `UPDATE pretranscode_rendition_job SET status=?,lease_owner=NULL,lease_until=NULL,started_at=CASE WHEN ?='waiting' THEN NULL ELSE started_at END,completed_at=CASE WHEN ?='cancelled' THEN CURRENT_TIMESTAMP ELSE completed_at END WHERE task_id=? AND status='running'`, status, status, status, id)
			if err != nil {
				return err
			}
			if superseded == 1 {
				if err = publication.AggregateTx(ctx, tx, runID); err != nil {
					return err
				}
			}
			changed++
			_ = mediaID
			_ = generation
			return nil
		})
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}
