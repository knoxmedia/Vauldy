package publication

import (
	"context"
	"database/sql"
	"errors"
)

var ErrIngestNotFound = errors.New("publication ingest not found")
var ErrNoRetryableWork = errors.New("publication ingest has no retryable work")

// RetryIngest retries exactly one current media generation by planning a fresh
// immutable generation from current policy. Degraded media remains visible while
// its replacement is processing.
func RetryIngest(ctx context.Context, db *sql.DB, mediaID int64, planner *Planner) error {
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
