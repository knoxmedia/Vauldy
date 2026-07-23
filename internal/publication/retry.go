package publication

import (
	"context"
	"database/sql"
	"errors"
	"time"

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

func RetryIngest(ctx context.Context, db *sql.DB, mediaID int64, planner *Planner) error {
	return store.WithBusyRetryPolicyContext(ctx, nil, retryIngestPolicy, func(attemptCtx context.Context) error {
		return retryIngestAttempt(attemptCtx, db, mediaID, planner)
	})
}

func retryIngestAttempt(ctx context.Context, db *sql.DB, mediaID int64, planner *Planner) error {
	if db == nil || mediaID <= 0 {
		return ErrIngestNotFound
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var generation int64
	var mediaState string
	if err = tx.QueryRowContext(ctx, `SELECT ingest_generation,publication_state FROM media WHERE id=?`, mediaID).Scan(&generation, &mediaState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIngestNotFound
		}
		return err
	}
	var runState string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM media_ingest_run WHERE media_id=? AND generation=?`, mediaID, generation).Scan(&runState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIngestNotFound
		}
		return err
	}

	preserveVisibility := mediaState == "degraded" && runState == "degraded"
	terminalRetry := (mediaState == "failed" && runState == "failed") || (mediaState == "cancelled" && runState == "cancelled")
	if !preserveVisibility && !terminalRetry {
		return ErrNoRetryableWork
	}
	if planner == nil {
		return errors.New("publication retry: nil planner")
	}
	_, err = planner.PlanReplacementTx(ctx, tx, mediaID, ReplacementOptions{
		Reason: PlanReasonManualRetry, PreserveVisibility: preserveVisibility, ExpectedGeneration: generation,
	})
	if errors.Is(err, ErrGenerationConflict) {
		return ErrNoRetryableWork
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
