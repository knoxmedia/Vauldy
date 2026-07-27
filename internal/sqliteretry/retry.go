package sqliteretry

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const defaultBackoff = 25 * time.Millisecond

// RetryPolicy bounds retries for one named SQLite operation. Non-positive
// backoffs normalize to 25ms; a non-positive MaxElapsed allows only the first
// attempt.
type RetryPolicy struct {
	Operation   string
	MaxElapsed  time.Duration
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

func ErrorCodes(err error) (primary, extended int, ok bool) {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return 0, 0, false
	}
	extended = sqliteErr.Code()
	return extended & 0xff, extended, true
}
func IsBusy(err error) bool {
	primary, _, ok := ErrorCodes(err)
	return ok && primary == sqlite3.SQLITE_BUSY
}

// WithBusyRetryPolicy is the compatibility API for operations that cannot
// accept a context. Its retry budget is enforced between calls, but an op that
// blocks cannot be interrupted. Budget-sensitive callers (including Task 11)
// must use WithBusyRetryPolicyContext.
func WithBusyRetryPolicy(ctx context.Context, policy RetryPolicy, op func() error) error {
	return WithBusyRetryPolicyContext(ctx, policy, func(context.Context) error { return op() })
}

// WithBusyRetryPolicyContext retries typed SQLITE_BUSY failures and gives each
// attempt a context bounded by the policy's remaining elapsed budget and the
// parent context deadline. A non-positive budget still permits one compatibility
// attempt using the live parent context; that first attempt cannot be time-bound
// by the zero budget, and a BUSY result is returned without retrying.
func WithBusyRetryPolicyContext(ctx context.Context, policy RetryPolicy, op func(context.Context) error) error {
	return withBusyRetryPolicyContext(ctx, policy, time.Now, sleepContext, randomJitter, op)
}

func withBusyRetryPolicyContext(ctx context.Context, policy RetryPolicy, now func() time.Time, sleep func(context.Context, time.Duration) error, jitter func(time.Duration, time.Duration) time.Duration, op func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	policy = normalizePolicy(policy)
	start := now()
	backoff := policy.BaseBackoff
	attempt := 0
	var lastBusyErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		elapsed := nonNegativeElapsed(start, now())
		if attempt > 0 && (policy.MaxElapsed <= 0 || elapsed >= policy.MaxElapsed) {
			return lastBusyErr
		}
		remaining := policy.MaxElapsed - elapsed
		attemptCtx := ctx
		cancel := func() {}
		if policy.MaxElapsed > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, remaining)
		}
		err := op(attemptCtx)
		cancel()
		attempt++
		retryable := isRetryableOperation(err)
		if err == nil || (!IsBusy(err) && !retryable) || (!retryable && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))) {
			return err
		}
		lastBusyErr = err

		elapsed = nonNegativeElapsed(start, now())
		if policy.MaxElapsed <= 0 || elapsed >= policy.MaxElapsed {
			return err
		}
		remaining = policy.MaxElapsed - elapsed
		delay := clampDuration(jitter(backoff, policy.MaxBackoff), 0, minDuration(policy.MaxBackoff, remaining))
		if delay <= 0 {
			return err
		}
		if sleepErr := sleep(ctx, delay); sleepErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return sleepErr
		}
		elapsed = nonNegativeElapsed(start, now())
		if policy.MaxElapsed <= 0 || elapsed >= policy.MaxElapsed {
			return err
		}
		backoff = saturatingDouble(backoff, policy.MaxBackoff)
	}
}

func isRetryableOperation(err error) bool {
	var retryable interface{ RetryableSQLiteOperation() bool }
	return errors.As(err, &retryable) && retryable.RetryableSQLiteOperation()
}

func normalizePolicy(policy RetryPolicy) RetryPolicy {
	if policy.BaseBackoff <= 0 {
		policy.BaseBackoff = defaultBackoff
	}
	if policy.MaxBackoff <= 0 || policy.MaxBackoff < policy.BaseBackoff {
		policy.MaxBackoff = policy.BaseBackoff
	}
	return policy
}
func nonNegativeElapsed(start, current time.Time) time.Duration {
	elapsed := current.Sub(start)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
func clampDuration(value, low, high time.Duration) time.Duration {
	if high < low {
		return low
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
func saturatingDouble(value, limit time.Duration) time.Duration {
	if value >= limit || value > limit-value {
		return limit
	}
	return value * 2
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
func randomJitter(base, max time.Duration) time.Duration {
	if base >= max {
		return max
	}
	room := max - base
	jitterMax := base / 4
	if jitterMax > room {
		jitterMax = room
	}
	if jitterMax <= 0 {
		return base
	}
	addition := time.Duration(rand.Int63n(int64(jitterMax) + 1))
	if addition > max-base {
		return max
	}
	return base + addition
}

// WithBusyRetry preserves the legacy five-attempt short retry contract.
func WithBusyRetry(ctx context.Context, op func() error) error {
	backoffs := [...]time.Duration{25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op()
		if err == nil || !IsBusy(err) || attempt == len(backoffs) {
			return err
		}
		if err := sleepContext(ctx, randomJitter(backoffs[attempt], backoffs[attempt]+backoffs[attempt]/4)); err != nil {
			return err
		}
	}
}
