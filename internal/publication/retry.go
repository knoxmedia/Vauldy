package publication

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"knox-media/internal/coreiface"
	"knox-media/internal/store"
)

var ErrIngestNotFound = errors.New("publication ingest not found")
var ErrNoRetryableWork = errors.New("publication ingest has no retryable work")

// RetryIngest retries exactly one current media generation by planning a fresh
// immutable generation from current policy. Degraded media remains visible while
// its replacement is processing.
var retryIngestPolicy = store.RetryPolicy{
	Operation: "publication_retry_ingest", MaxElapsed: 2 * time.Second,
	BaseBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
}

var retryIngestAttemptFn = retryIngestAttempt

func RetryIngest(ctx context.Context, db *sql.DB, mediaID int64, planner *Planner) error {
	var uncertain *store.ImmediateCommitError
	err := store.WithBusyRetryPolicyContext(ctx, nil, retryIngestPolicy, func(attemptCtx context.Context) error {
		attemptErr := retryIngestAttemptFn(attemptCtx, db, mediaID, planner)
		if errors.As(attemptErr, &uncertain) {
			return retryCommitOutcomeUncertain{err: attemptErr}
		}
		return attemptErr
	})
	var protected retryCommitOutcomeUncertain
	if errors.As(err, &protected) {
		return protected.err
	}
	return err
}

type retryCommitOutcomeUncertain struct{ err error }

func (e retryCommitOutcomeUncertain) Error() string { return e.err.Error() }

func retryIngestAttempt(ctx context.Context, db *sql.DB, mediaID int64, planner *Planner) error {
	if db == nil || mediaID <= 0 {
		return ErrIngestNotFound
	}
	_, err := store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
		var attemptErr error
		var generation int64
		var mediaState string
		if attemptErr = tx.QueryRowContext(ctx, `SELECT ingest_generation,publication_state FROM media WHERE id=?`, mediaID).Scan(&generation, &mediaState); attemptErr != nil {
			if errors.Is(attemptErr, sql.ErrNoRows) {
				return ErrIngestNotFound
			}
			return attemptErr
		}
		var runState string
		if attemptErr = tx.QueryRowContext(ctx, `SELECT status FROM media_ingest_run WHERE media_id=? AND generation=?`, mediaID, generation).Scan(&runState); attemptErr != nil {
			if errors.Is(attemptErr, sql.ErrNoRows) {
				return ErrIngestNotFound
			}
			return attemptErr
		}

		preserveVisibility := mediaState == "degraded" && runState == "degraded"
		terminalRetry := (mediaState == "failed" && runState == "failed") || (mediaState == "cancelled" && runState == "cancelled")
		if !preserveVisibility && !terminalRetry {
			return ErrNoRetryableWork
		}
		if planner == nil {
			return errors.New("publication retry: nil planner")
		}
		_, attemptErr = planner.planReplacement(ctx, tx, mediaID, ReplacementOptions{
			Reason: PlanReasonManualRetry, PreserveVisibility: preserveVisibility, ExpectedGeneration: generation,
		})
		if errors.Is(attemptErr, ErrGenerationConflict) {
			return ErrNoRetryableWork
		}
		if attemptErr != nil {
			return attemptErr
		}
		return nil
	})
	return err
}

type OptionalPostIngestRetryRequest struct {
	MediaID, StepID, ActorID int64
	Reason                   string
}

// OptionalScrapeRetryRequest identifies one exhausted optional scrape step in
// the current published or degraded generation.
type OptionalScrapeRetryRequest struct {
	MediaID, StepID, ActorID int64
	Reason                   string
}

// RetryOptionalScrape reopens one exhausted optional scrape step without
// changing the terminal publication outcome.
func RetryOptionalScrape(ctx context.Context, db *sql.DB, req OptionalScrapeRetryRequest, registry coreiface.CapabilityRegistry) error {
	if db == nil || req.MediaID <= 0 || req.StepID <= 0 || req.ActorID <= 0 || strings.TrimSpace(req.Reason) == "" || registry == nil || !registry.Available("scrape") {
		return ErrNoRetryableWork
	}
	var changed bool
	var committedRetryRound int
	err := store.WithBusyRetryPolicyContext(ctx, nil, retryIngestPolicy, func(attempt context.Context) error {
		changed = false
		outcome, err := store.WithImmediateConnTx(attempt, db, func(tx store.ImmediateConnTx) error {
			var runID, generation int64
			var mediaState, runState, stepType, stepStatus, queueStatus, queueError, stepError string
			var attempts, queueAttempts, queueMaxAttempts, queueRound int
			err := tx.QueryRowContext(attempt, `SELECT r.id,r.generation,m.publication_state,r.status,s.step_type,s.status,s.attempts,s.last_error,q.status,q.fail_count,3,q.message,q.retry_round FROM media m JOIN media_ingest_run r ON r.media_id=m.id AND r.generation=m.ingest_generation JOIN media_ingest_step s ON s.run_id=r.id AND s.media_id=m.id AND s.generation=r.generation JOIN scrape_task q ON q.ingest_step_id=s.id AND q.ingest_run_id=r.id AND q.media_id=m.id AND q.generation=r.generation WHERE m.id=? AND s.id=? AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL`, req.MediaID, req.StepID).Scan(&runID, &generation, &mediaState, &runState, &stepType, &stepStatus, &attempts, &stepError, &queueStatus, &queueAttempts, &queueMaxAttempts, &queueError, &queueRound)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoRetryableWork
			}
			if err != nil {
				return err
			}
			if (mediaState != "published" && mediaState != "degraded") || (runState != "published" && runState != "degraded") || stepType != "scrape" || (stepStatus != "failed" && stepStatus != "cancelled") || (queueStatus != "failed" && queueStatus != "cancelled") || queueAttempts < queueMaxAttempts {
				return ErrNoRetryableWork
			}
			nextRound := queueRound + 1
			committedRetryRound = nextRound
			if _, err = tx.ExecContext(attempt, `INSERT INTO media_ingest_optional_retry_audit(media_id,run_id,step_id,generation,task_family,task_type,actor_id,reason,previous_queue_status,previous_step_status,previous_attempts,previous_queue_error,previous_step_error,retry_round) VALUES(?,?,?,?, 'scrape',?,?,?,?,?,?,?,?,?)`, req.MediaID, runID, req.StepID, generation, stepType, req.ActorID, req.Reason, queueStatus, stepStatus, attempts, queueError, stepError, nextRound); err != nil {
				return err
			}
			r, err := tx.ExecContext(attempt, `UPDATE scrape_task SET status='waiting',fail_count=0,retry_round=?,progress=0,message='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=CURRENT_TIMESTAMP WHERE ingest_step_id=? AND ingest_run_id=? AND generation=? AND status IN ('failed','cancelled') AND fail_count>=3 AND retry_round=?`, nextRound, req.StepID, runID, generation, queueRound)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n != 1 {
				return ErrNoRetryableWork
			}
			r, err = tx.ExecContext(attempt, `UPDATE media_ingest_step SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND generation=? AND required=0 AND status IN ('failed','cancelled')`, req.StepID, runID, generation)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n != 1 {
				return ErrNoRetryableWork
			}
			changed = true
			return nil
		})
		if err != nil && outcome.CommitAttempted {
			var audit int
			if reconcileErr := db.QueryRowContext(attempt, `SELECT COUNT(*) FROM media_ingest_optional_retry_audit WHERE media_id=? AND step_id=? AND retry_round=? AND task_family='scrape'`, req.MediaID, req.StepID, committedRetryRound).Scan(&audit); reconcileErr == nil && audit == 1 {
				changed = true
				return nil
			}
			return retryCommitOutcomeUncertain{err: err}
		}
		return err
	})
	if err != nil {
		return err
	}
	if !changed {
		return ErrNoRetryableWork
	}
	return nil
}

// RetryOptionalPostIngest reopens one exhausted optional post-ingest step without changing the terminal publication outcome.
func RetryOptionalPostIngest(ctx context.Context, db *sql.DB, req OptionalPostIngestRetryRequest) error {
	if db == nil || req.MediaID <= 0 || req.StepID <= 0 || req.ActorID <= 0 || strings.TrimSpace(req.Reason) == "" {
		return ErrNoRetryableWork
	}
	var changed bool
	var committedRetryRound int
	err := store.WithBusyRetryPolicyContext(ctx, nil, retryIngestPolicy, func(attempt context.Context) error {
		changed = false
		outcome, err := store.WithImmediateConnTx(attempt, db, func(tx store.ImmediateConnTx) error {
			var runID, generation int64
			var mediaState, runState, stepType, stepStatus, queueStatus, queueError, stepError string
			var attempts, queueAttempts, queueMaxAttempts, queueRound int
			err := tx.QueryRowContext(attempt, `SELECT r.id,r.generation,m.publication_state,r.status,s.step_type,s.status,s.attempts,s.last_error,q.status,q.attempts,q.max_attempts,q.last_error,q.retry_round FROM media m JOIN media_ingest_run r ON r.media_id=m.id AND r.generation=m.ingest_generation JOIN media_ingest_step s ON s.run_id=r.id AND s.media_id=m.id AND s.generation=r.generation JOIN post_ingest_task q ON q.ingest_step_id=s.id AND q.ingest_run_id=r.id AND q.media_id=m.id AND q.generation=r.generation WHERE m.id=? AND s.id=? AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL`, req.MediaID, req.StepID).Scan(&runID, &generation, &mediaState, &runState, &stepType, &stepStatus, &attempts, &stepError, &queueStatus, &queueAttempts, &queueMaxAttempts, &queueError, &queueRound)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoRetryableWork
			}
			if err != nil {
				return err
			}
			if (mediaState != "published" && mediaState != "degraded") || (runState != "published" && runState != "degraded") || (stepType != "preview" && stepType != "subtitle") || (stepStatus != "failed" && stepStatus != "cancelled") || (queueStatus != "failed" && queueStatus != "cancelled") || queueAttempts < queueMaxAttempts {
				return ErrNoRetryableWork
			}
			nextRound := queueRound + 1
			committedRetryRound = nextRound
			_, err = tx.ExecContext(attempt, `INSERT INTO media_ingest_optional_retry_audit(media_id,run_id,step_id,generation,task_family,task_type,actor_id,reason,previous_queue_status,previous_step_status,previous_attempts,previous_queue_error,previous_step_error,retry_round) VALUES(?,?,?,?, 'post_ingest',?,?,?,?,?,?,?,?,?)`, req.MediaID, runID, req.StepID, generation, stepType, req.ActorID, req.Reason, queueStatus, stepStatus, attempts, queueError, stepError, nextRound)
			if err != nil {
				return err
			}
			r, err := tx.ExecContext(attempt, `UPDATE post_ingest_task SET status='waiting',attempts=0,retry_round=?,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE ingest_step_id=? AND ingest_run_id=? AND generation=? AND status IN ('failed','cancelled') AND attempts>=max_attempts AND retry_round=?`, nextRound, req.StepID, runID, generation, queueRound)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n != 1 {
				return ErrNoRetryableWork
			}
			r, err = tx.ExecContext(attempt, `UPDATE media_ingest_step SET status='waiting',attempts=0,last_error='',lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,available_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND generation=? AND required=0 AND status IN ('failed','cancelled')`, req.StepID, runID, generation)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n != 1 {
				return ErrNoRetryableWork
			}
			changed = true
			return nil
		})
		if err != nil && outcome.CommitAttempted {
			var audit int
			reconcileErr := db.QueryRowContext(attempt, `SELECT COUNT(*) FROM media_ingest_optional_retry_audit WHERE media_id=? AND step_id=? AND retry_round=?`, req.MediaID, req.StepID, committedRetryRound).Scan(&audit)
			if reconcileErr == nil && audit == 1 {
				changed = true
				return nil
			}
			return retryCommitOutcomeUncertain{err: err}
		}
		return err
	})
	if err != nil {
		return err
	}
	if !changed {
		return ErrNoRetryableWork
	}
	return nil
}
