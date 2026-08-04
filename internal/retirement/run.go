package retirement

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RunReconciler periodically repairs interrupted retirement filesystem states.
func RunReconciler(ctx context.Context, db *sql.DB, opts RecoveryOptions, interval time.Duration, report func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		if err := ReconcileStartup(ctx, db, opts); err != nil && report != nil && !errors.Is(err, context.Canceled) {
			report(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// RunWorkerLoop claims and executes due retirement work until ctx is cancelled.
func RunWorkerLoop(ctx context.Context, w *Worker, interval time.Duration, report func(error)) {
	if w == nil {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	run := func() {
		for {
			if err := ctx.Err(); err != nil {
				return
			}
			row, err := w.ClaimReady(ctx)
			if err != nil {
				if errors.Is(err, ErrNotClaimable) || errors.Is(err, context.Canceled) {
					return
				}
				if report != nil {
					report(err)
				}
				return
			}
			if err := w.Execute(ctx, *row); err != nil && report != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrBarrierBlocked) && !errors.Is(err, ErrNotClaimable) {
				report(err)
			}
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
