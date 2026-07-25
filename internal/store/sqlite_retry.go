package store

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"knox-media/internal/sqliteretry"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type RetryPolicy = sqliteretry.RetryPolicy

type SQLiteMetrics struct {
	BusyRetries           atomic.Uint64
	BusyExhausted         atomic.Uint64
	ProgressBatches       atomic.Uint64
	LogBatches            atomic.Uint64
	LogBatchFailures      atomic.Uint64
	DroppedLogs           atomic.Uint64
	ImmediateTransactions atomic.Uint64

	operationsMu sync.Mutex
	operations   map[string]uint64
}

func (m *SQLiteMetrics) RecordImmediateTransaction() {
	if m != nil {
		m.ImmediateTransactions.Add(1)
	}
}

func (m *SQLiteMetrics) recordOperation(operation string) {
	if m == nil || operation == "" {
		return
	}
	m.operationsMu.Lock()
	defer m.operationsMu.Unlock()
	if m.operations == nil {
		m.operations = make(map[string]uint64)
	}
	m.operations[operation]++
}

func (m *SQLiteMetrics) OperationRetries(operation string) uint64 {
	if m == nil {
		return 0
	}
	m.operationsMu.Lock()
	defer m.operationsMu.Unlock()
	return m.operations[operation]
}

func IsSQLiteBusy(err error) bool { return sqliteretry.IsBusy(err) }

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

func WithBusyRetryPolicy(ctx context.Context, metrics *SQLiteMetrics, policy RetryPolicy, op func() error) error {
	return WithBusyRetryPolicyContext(ctx, metrics, policy, func(context.Context) error { return op() })
}

// WithBusyRetryPolicyContext is the budget-enforcing API for coordinator and
// lease-sensitive operations. The operation must honor its attempt context.
func WithBusyRetryPolicyContext(ctx context.Context, metrics *SQLiteMetrics, policy RetryPolicy, op func(context.Context) error) error {
	busyResults := uint64(0)
	err := sqliteretry.WithBusyRetryPolicyContext(ctx, policy, func(attemptCtx context.Context) error {
		opErr := op(attemptCtx)
		if IsSQLiteBusy(opErr) {
			busyResults++
		}
		return opErr
	})
	retries := busyResults
	if IsSQLiteBusy(err) && retries > 0 {
		retries--
		if metrics != nil {
			metrics.BusyExhausted.Add(1)
		}
	}
	if metrics != nil && retries > 0 {
		metrics.BusyRetries.Add(retries)
		for i := uint64(0); i < retries; i++ {
			metrics.recordOperation(policy.Operation)
		}
	}
	return err
}

// HeartbeatLeaseRetryPolicy reserves safetyMargin from the remaining lease.
// Task 11 can wire this policy into coordinator heartbeat operations.
func HeartbeatLeaseRetryPolicy(operation string, remainingLease, safetyMargin time.Duration) RetryPolicy {
	budget := remainingLease - safetyMargin
	if budget < 0 {
		budget = 0
	}
	return RetryPolicy{Operation: operation, MaxElapsed: budget, BaseBackoff: 25 * time.Millisecond, MaxBackoff: 200 * time.Millisecond}
}

func withBusyRetryPolicy(ctx context.Context, metrics *SQLiteMetrics, policy RetryPolicy, now func() time.Time, sleep func(context.Context, time.Duration) error, jitter func(time.Duration, time.Duration) time.Duration, op func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	start := now()
	backoff := policy.BaseBackoff
	if backoff <= 0 {
		backoff = 25 * time.Millisecond
	}
	maxBackoff := policy.MaxBackoff
	if maxBackoff <= 0 || maxBackoff < backoff {
		maxBackoff = backoff
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op()
		if err == nil || !IsSQLiteBusy(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		elapsed := now().Sub(start)
		delay := jitter(backoff, maxBackoff)
		if delay < 0 {
			delay = 0
		}
		if delay > maxBackoff {
			delay = maxBackoff
		}
		if policy.MaxElapsed <= 0 || elapsed >= policy.MaxElapsed || delay > policy.MaxElapsed-elapsed {
			if metrics != nil {
				metrics.BusyExhausted.Add(1)
			}
			return err
		}
		if metrics != nil {
			metrics.BusyRetries.Add(1)
			metrics.recordOperation(policy.Operation)
		}
		if err := sleep(ctx, delay); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		elapsed = now().Sub(start)
		if elapsed < 0 {
			elapsed = 0
		}
		if policy.MaxElapsed <= 0 || elapsed >= policy.MaxElapsed {
			return err
		}
		if backoff < maxBackoff {
			if backoff > maxBackoff-backoff {
				backoff = maxBackoff
			} else {
				backoff *= 2
			}
		}
	}
}

func withBusyRetry(ctx context.Context, metrics *SQLiteMetrics, sleep func(context.Context, time.Duration) error, jitter func(time.Duration) time.Duration, op func() error) error {
	backoffs := [...]time.Duration{25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
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
