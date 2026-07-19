package store

import (
	"context"
	"errors"
	"math/rand"
	"sync/atomic"
	"time"

	"knox-media/internal/sqliteretry"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type SQLiteMetrics struct {
	BusyRetries      atomic.Uint64
	BusyExhausted    atomic.Uint64
	ProgressBatches  atomic.Uint64
	LogBatches       atomic.Uint64
	LogBatchFailures atomic.Uint64
	DroppedLogs      atomic.Uint64
}

func IsSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
}

func IsSQLiteConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}

func WithBusyRetry(ctx context.Context, metrics *SQLiteMetrics, op func() error) error {
	if metrics == nil {
		return sqliteretry.WithBusyRetry(ctx, op)
	}
	return withBusyRetry(ctx, metrics, sleepContext, jitterDelay, op)
}

func withBusyRetry(
	ctx context.Context,
	metrics *SQLiteMetrics,
	sleep func(context.Context, time.Duration) error,
	jitter func(time.Duration) time.Duration,
	op func() error,
) error {
	backoffs := [...]time.Duration{
		25 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
	}

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := op()
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !IsSQLiteBusy(err) {
			return err
		}
		if attempt == len(backoffs) {
			if metrics != nil {
				metrics.BusyExhausted.Add(1)
			}
			return err
		}

		if metrics != nil {
			metrics.BusyRetries.Add(1)
		}
		if err := sleep(ctx, jitter(backoffs[attempt])); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func jitterDelay(base time.Duration) time.Duration {
	return base + time.Duration(rand.Int63n(int64(base)/4+1))
}
