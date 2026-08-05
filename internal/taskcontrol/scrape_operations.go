package taskcontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"knox-media/internal/coreiface"
	"knox-media/internal/publication"
	"knox-media/internal/store"
)

// ScrapeTaskController owns safe lifecycle controls for standalone scrape rows.
type ScrapeTaskController struct {
	db       *sql.DB
	registry coreiface.CapabilityRegistry
	mu       sync.Mutex
	active   map[int64]context.CancelFunc
}

func NewScrapeTaskController(db *sql.DB, registry ...coreiface.CapabilityRegistry) *ScrapeTaskController {
	c := &ScrapeTaskController{db: db, active: make(map[int64]context.CancelFunc)}
	if len(registry) > 0 {
		c.registry = registry[0]
	}
	return c
}

func (c *ScrapeTaskController) Register(id int64, cancel context.CancelFunc) func() {
	if c == nil || id <= 0 || cancel == nil {
		return func() {}
	}
	c.mu.Lock()
	c.active[id] = cancel
	c.mu.Unlock()
	return func() { c.mu.Lock(); delete(c.active, id); c.mu.Unlock() }
}

func (c *ScrapeTaskController) cancelActive(id int64) {
	c.mu.Lock()
	cancel := c.active[id]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func standaloneScrapeWhere() string {
	return "ingest_run_id IS NULL AND ingest_step_id IS NULL AND COALESCE(generation,0)=0"
}

func invalidScrapeOperation(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidOperation, msg) }

func (c *ScrapeTaskController) Abort(ctx context.Context, req ExternalOperationRequest) error {
	if c == nil || c.db == nil {
		return invalidScrapeOperation("scrape controller unavailable")
	}
	outcome, err := store.WithImmediateConnTx(ctx, c.db, func(tx store.ImmediateConnTx) error {
		result, err := tx.ExecContext(ctx, `UPDATE scrape_task AS q SET status='cancelled',progress=100,message='cancelled by user',lease_owner=NULL,lease_until=NULL,finished_at=CURRENT_TIMESTAMP WHERE q.id=? AND q.status='running' AND (q.ingest_run_id IS NULL AND q.ingest_step_id IS NULL AND COALESCE(q.generation,0)=0 OR EXISTS(SELECT 1 FROM media_ingest_step s JOIN media_ingest_run r ON r.id=s.run_id JOIN media m ON m.id=s.media_id WHERE s.id=q.ingest_step_id AND s.run_id=q.ingest_run_id AND s.media_id=q.media_id AND s.generation=q.generation AND r.media_id=q.media_id AND r.generation=q.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=q.generation))`, req.ID)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return invalidScrapeOperation("scrape task is not standalone or current linked running")
		}
		var runID, stepID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT ingest_run_id,ingest_step_id FROM scrape_task WHERE id=?`, req.ID).Scan(&runID, &stepID); err != nil {
			return err
		}
		if runID.Valid && stepID.Valid {
			res, err := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='cancelled',last_error='cancelled by user',lease_owner=NULL,lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND status='running'`, stepID.Int64, runID.Int64)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return invalidScrapeOperation("linked scrape step changed concurrently")
			}
			if err := publication.FinalizeNodeTransitionTx(ctx, tx, runID.Int64); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if outcome.CommitConfirmed {
		c.cancelActive(req.ID)
	}
	return nil
}

func (c *ScrapeTaskController) Reset(ctx context.Context, req ExternalOperationRequest) error {
	if c == nil || c.db == nil {
		return invalidScrapeOperation("scrape controller unavailable")
	}
	var mediaID, stepID int64
	var linked, current, optional, exhausted bool
	var status string
	err := c.db.QueryRowContext(ctx, `SELECT q.media_id,q.status,CASE WHEN q.ingest_run_id IS NOT NULL OR q.ingest_step_id IS NOT NULL OR COALESCE(q.generation,0)<>0 THEN 1 ELSE 0 END,COALESCE(q.ingest_step_id,0),CASE WHEN EXISTS(SELECT 1 FROM media_ingest_step s JOIN media_ingest_run r ON r.id=s.run_id JOIN media m ON m.id=s.media_id WHERE s.id=q.ingest_step_id AND s.run_id=q.ingest_run_id AND s.media_id=q.media_id AND s.generation=q.generation AND r.media_id=q.media_id AND r.generation=q.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=q.generation) THEN 1 ELSE 0 END,CASE WHEN EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.id=q.ingest_step_id AND s.run_id=q.ingest_run_id AND s.media_id=q.media_id AND s.generation=q.generation AND s.required=0) THEN 1 ELSE 0 END,CASE WHEN COALESCE(q.fail_count,0)>=? THEN 1 ELSE 0 END FROM scrape_task q WHERE q.id=?`, publication.DefaultNetworkMaxAttempts, req.ID).Scan(&mediaID, &status, &linked, &stepID, &current, &optional, &exhausted)
	if errors.Is(err, sql.ErrNoRows) {
		return invalidScrapeOperation("scrape task missing")
	}
	if err != nil {
		return err
	}
	if linked {
		if !current || !optional || !exhausted || (status != "failed" && status != "cancelled") {
			return invalidScrapeOperation("linked scrape task is not current optional exhausted terminal")
		}
		return publication.RetryOptionalScrape(ctx, c.db, publication.OptionalScrapeRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: req.ActorID, Reason: req.Reason}, c.registry)
	}
	_, err = store.WithImmediateConnTx(ctx, c.db, func(tx store.ImmediateConnTx) error {
		var round int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(retry_round,0) FROM scrape_task WHERE id=? AND status=? AND `+standaloneScrapeWhere(), req.ID, status).Scan(&round); err != nil {
			return invalidScrapeOperation("scrape task changed concurrently")
		}
		switch status {
		case "done", "skipped", "cancelled", "failed", "abandoned":
		default:
			return invalidScrapeOperation("scrape task is not terminal")
		}
		res, err := tx.ExecContext(ctx, `UPDATE scrape_task SET status='waiting',fail_count=0,progress=0,message='',lease_owner=NULL,lease_until=NULL,available_at=CURRENT_TIMESTAMP,finished_at=NULL,started_at=NULL,retry_round=? WHERE id=? AND status=? AND retry_round=? AND `+standaloneScrapeWhere(), round+1, req.ID, status, round)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return invalidScrapeOperation("scrape task changed concurrently")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_control_audit(task_identity,actor_id,action,reason,previous_status,new_status,new_retry_round) VALUES(?,?,'reset',?,?,'waiting',?)`, req.Identity, req.ActorID, req.Reason, status, round+1)
		return err
	})
	return err
}

func (c *ScrapeTaskController) Remove(ctx context.Context, req ExternalOperationRequest) error {
	if c == nil || c.db == nil {
		return invalidScrapeOperation("scrape controller unavailable")
	}
	_, err := store.WithImmediateConnTx(ctx, c.db, func(tx store.ImmediateConnTx) error {
		var status string
		var linked, stale bool
		err := tx.QueryRowContext(ctx, `SELECT q.status,CASE WHEN q.ingest_run_id IS NOT NULL OR q.ingest_step_id IS NOT NULL OR COALESCE(q.generation,0)<>0 THEN 1 ELSE 0 END,CASE WHEN EXISTS(SELECT 1 FROM media_ingest_step s JOIN media_ingest_run r ON r.id=s.run_id JOIN media m ON m.id=s.media_id WHERE s.id=q.ingest_step_id AND s.run_id=q.ingest_run_id AND s.media_id=q.media_id AND s.generation=q.generation AND r.media_id=q.media_id AND r.generation=q.generation AND (r.superseded_at IS NOT NULL OR r.superseded_by_generation IS NOT NULL OR m.ingest_generation<>q.generation)) THEN 1 ELSE 0 END FROM scrape_task q WHERE q.id=?`, req.ID).Scan(&status, &linked, &stale)
		if errors.Is(err, sql.ErrNoRows) {
			return invalidScrapeOperation("scrape task missing")
		}
		if err != nil {
			return err
		}
		terminal := status == "done" || status == "skipped" || status == "cancelled" || status == "failed" || status == "abandoned"
		if linked {
			if !stale || !terminal {
				return invalidScrapeOperation("linked scrape task is not stale terminal")
			}
		} else if status != "waiting" && !terminal {
			return invalidScrapeOperation("running scrape task cannot be removed")
		}
		var journals int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_asset_stage_journal WHERE scrape_task_id=? AND state='staged'`, req.ID).Scan(&journals); err == nil && journals != 0 {
			return invalidScrapeOperation("scrape task has staged artwork")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_control_audit(task_identity,actor_id,action,reason,previous_status,new_status) VALUES(?,?,'remove',?,?,'removed')`, req.Identity, req.ActorID, req.Reason, status); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM scrape_task WHERE id=? AND status=?`, req.ID, status)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return invalidScrapeOperation("scrape task changed concurrently")
		}
		return nil
	})
	return err
}

func StandaloneScrapeAction(row *ProjectionRow) bool { return row != nil && !row.Linked }
func ScrapeAbortAction(row *ProjectionRow) bool {
	return row != nil && (!row.Linked || (row.LinkValid && row.LinkCurrent))
}
func ScrapeResetAction(row *ProjectionRow) bool {
	return row != nil && (!row.Linked || (row.LinkValid && row.LinkCurrent && row.LinkOptional && row.Attempt >= row.MaxAttempts && (row.RawStatus == "failed" || row.RawStatus == "cancelled")))
}
func ScrapeRemoveAction(row *ProjectionRow) bool {
	return row != nil && (!row.Linked || (row.LinkValid && row.LinkStale))
}
