package main

import (
	"context"
	"database/sql"
	"time"

	"knox-media/internal/scancoord"
)

func startFinalizeRecoveryLoop(ctx context.Context, db *sql.DB, interval time.Duration, report func(error)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		recoverPending := func() {
			if _, err := scancoord.RecoverPendingFinalizations(ctx, db, 16); err != nil && ctx.Err() == nil && report != nil {
				report(err)
			}
		}
		recoverPending()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				recoverPending()
			}
		}
	}()
	return done
}
