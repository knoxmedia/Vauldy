package publication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"knox-media/internal/store"
)

// ReconcileStartupPublicationV2 atomically replaces active policy-v1 generations,
// validates current v2 plans, and aggregates their required outcomes.
func ReconcileStartupPublicationV2(ctx context.Context, db *sql.DB, planner *Planner) (int, error) {
	if db == nil || planner == nil {
		return 0, errors.New("publication v2 startup reconcile: database and planner are required")
	}
	replaced := 0
	for {
		var runID int64
		err := db.QueryRowContext(ctx, `SELECT r.id FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE COALESCE(r.policy_version,1)=1 AND r.status='processing' AND r.superseded_at IS NULL ORDER BY r.id LIMIT 1`).Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return replaced, err
		}
		changed := false
		_, err = store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
			var mediaID, generation int64
			var state string
			if e := tx.QueryRowContext(ctx, `SELECT r.media_id,r.generation,m.publication_state FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.id=? AND COALESCE(r.policy_version,1)=1 AND r.status='processing' AND r.superseded_at IS NULL`, runID).Scan(&mediaID, &generation, &state); errors.Is(e, sql.ErrNoRows) {
				return nil
			} else if e != nil {
				return e
			}
			preserve := state == "published" || state == "degraded"
			_, e := planner.planReplacement(ctx, tx, mediaID, ReplacementOptions{Reason: PlanReasonRepair, PreserveVisibility: preserve, ExpectedGeneration: generation})
			if errors.Is(e, ErrGenerationConflict) {
				return nil
			}
			if e != nil {
				return e
			}
			changed = true
			return nil
		})
		if err != nil {
			return replaced, fmt.Errorf("publication v2 startup replace run %d: %w", runID, err)
		}
		if changed {
			replaced++
		} else {
			break
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT r.id FROM media_ingest_run r JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation WHERE r.policy_version=2 AND r.superseded_at IS NULL ORDER BY r.id`)
	if err != nil {
		return replaced, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return replaced, err
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return replaced, err
	}
	for _, id := range ids {
		_, err = store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
			if e := validateCurrentV2Run(ctx, tx, id); e != nil {
				return e
			}
			return AggregateTx(ctx, tx, id)
		})
		if err != nil {
			return replaced, fmt.Errorf("publication v2 startup validate run %d: %w", id, err)
		}
	}
	return replaced, nil
}

func validateCurrentV2Run(ctx context.Context, q store.SQLExecutor, runID int64) error {
	var raw string
	var mediaID, generation int64
	if err := q.QueryRowContext(ctx, `SELECT media_id,generation,config_snapshot_json FROM media_ingest_run WHERE id=? AND policy_version=2`, runID).Scan(&mediaID, &generation, &raw); err != nil {
		return err
	}
	var valid int
	if err := q.QueryRowContext(ctx, `SELECT json_valid(?)`, raw).Scan(&valid); err != nil || valid != 1 {
		return errors.New("malformed config snapshot")
	}
	var mismatch int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step WHERE run_id=? AND (media_id<>? OR generation<>?)`, runID, mediaID, generation).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch != 0 {
		return errors.New("step identity mismatch")
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id LEFT JOIN media_ingest_step p ON p.id=d.depends_on_step_id WHERE s.run_id=? AND (s.media_id<>? OR s.generation<>? OR (p.id IS NOT NULL AND (p.run_id<>? OR p.media_id<>? OR p.generation<>?)))`, runID, mediaID, generation, runID, mediaID, generation).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch != 0 {
		return errors.New("dependency identity mismatch")
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence WHERE run_id=? AND (media_id<>? OR generation<>?)`, runID, mediaID, generation).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch != 0 {
		return errors.New("evidence identity mismatch")
	}
	return nil
}
