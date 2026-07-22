package sqliteretry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func typedSQLiteError(t *testing.T, code int) error {
	t.Helper()
	err := &sqlite.Error{}
	field := reflect.ValueOf(err).Elem().FieldByName("code")
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().SetInt(int64(code))
	return err
}

func TestWithBusyRetryPolicyContextBoundsBlockingOperation(t *testing.T) {
	busy := typedSQLiteError(t, sqlite3.SQLITE_BUSY)
	started := time.Now()
	calls := 0
	err := WithBusyRetryPolicyContext(context.Background(), RetryPolicy{MaxElapsed: 40 * time.Millisecond, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, func(ctx context.Context) error {
		calls++
		if calls == 1 {
			return busy
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("elapsed=%v", elapsed)
	}
}

func TestWithBusyRetryPolicyDoesNotCallAtBudgetBoundary(t *testing.T) {
	busy := typedSQLiteError(t, sqlite3.SQLITE_BUSY_SNAPSHOT)
	now := time.Unix(100, 0)
	calls := 0
	err := withBusyRetryPolicyContext(context.Background(), RetryPolicy{MaxElapsed: 25 * time.Millisecond, BaseBackoff: 25 * time.Millisecond, MaxBackoff: 25 * time.Millisecond},
		func() time.Time { return now },
		func(context.Context, time.Duration) error { now = now.Add(25 * time.Millisecond); return nil },
		func(base, _ time.Duration) time.Duration { return base },
		func(context.Context) error { calls++; return busy },
	)
	if err != busy || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestWithBusyRetryPolicySaturatesBackoffAndHandlesClockRollback(t *testing.T) {
	busy := typedSQLiteError(t, sqlite3.SQLITE_BUSY_TIMEOUT)
	now := time.Unix(100, 0)
	var sleeps []time.Duration
	calls := 0
	max := time.Duration(1<<63 - 1)
	err := withBusyRetryPolicyContext(context.Background(), RetryPolicy{MaxElapsed: max, BaseBackoff: max/2 + 1, MaxBackoff: max},
		func() time.Time { return now },
		func(context.Context, time.Duration) error {
			if len(sleeps) == 0 {
				now = now.Add(-time.Second)
			}
			return nil
		},
		func(base, limit time.Duration) time.Duration { sleeps = append(sleeps, base); return limit },
		func(context.Context) error {
			calls++
			if calls == 3 {
				return nil
			}
			return busy
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(sleeps) != 2 {
		t.Fatalf("calls=%d sleeps=%v", calls, sleeps)
	}
	for _, delay := range sleeps {
		if delay <= 0 || delay > max {
			t.Fatalf("invalid delay %v", delay)
		}
	}
	if sleeps[1] != max {
		t.Fatalf("second backoff=%v want %v", sleeps[1], max)
	}
}

func TestRetryPolicyNormalizesInvalidValuesAndWrappedBusy(t *testing.T) {
	busy := fmt.Errorf("wrapped: %w", typedSQLiteError(t, sqlite3.SQLITE_BUSY_SNAPSHOT))
	now := time.Unix(100, 0)
	calls := 0
	err := withBusyRetryPolicyContext(context.Background(), RetryPolicy{MaxElapsed: time.Second, BaseBackoff: -1, MaxBackoff: -1}, func() time.Time { return now }, func(context.Context, time.Duration) error { now = now.Add(25 * time.Millisecond); return nil }, func(base, max time.Duration) time.Duration {
		if base != 25*time.Millisecond || max != 25*time.Millisecond {
			t.Fatalf("normalized=%v/%v", base, max)
		}
		return base
	}, func(context.Context) error {
		calls++
		if calls == 2 {
			return nil
		}
		return busy
	})
	if err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestWithBusyRetryPolicyReturnsLastBusyWhenBudgetCrossesBeforeNextAttempt(t *testing.T) {
	busy := typedSQLiteError(t, sqlite3.SQLITE_BUSY_SNAPSHOT)
	start := time.Unix(100, 0)
	times := []time.Time{start, start, start, start.Add(50 * time.Millisecond), start.Add(101 * time.Millisecond)}
	index := 0
	now := func() time.Time { value := times[index]; index++; return value }
	calls := 0
	err := withBusyRetryPolicyContext(context.Background(), RetryPolicy{MaxElapsed: 100 * time.Millisecond, BaseBackoff: 25 * time.Millisecond, MaxBackoff: 25 * time.Millisecond}, now, func(context.Context, time.Duration) error { return nil }, func(base, _ time.Duration) time.Duration { return base }, func(context.Context) error { calls++; return busy })
	if err != busy || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestWithBusyRetryPolicyContextNonPositiveBudgetUsesLiveParentForFirstAttempt(t *testing.T) {
	busy := typedSQLiteError(t, sqlite3.SQLITE_BUSY)
	for _, maxElapsed := range []time.Duration{0, -time.Second} {
		t.Run(maxElapsed.String(), func(t *testing.T) {
			calls := 0
			err := WithBusyRetryPolicyContext(context.Background(), RetryPolicy{MaxElapsed: maxElapsed}, func(ctx context.Context) error {
				calls++
				if err := ctx.Err(); err != nil {
					t.Fatalf("first context already done: %v", err)
				}
				return busy
			})
			if err != busy || calls != 1 {
				t.Fatalf("busy err=%v calls=%d", err, calls)
			}

			calls = 0
			err = WithBusyRetryPolicyContext(context.Background(), RetryPolicy{MaxElapsed: maxElapsed}, func(ctx context.Context) error {
				calls++
				if err := ctx.Err(); err != nil {
					t.Fatalf("success context already done: %v", err)
				}
				return nil
			})
			if err != nil || calls != 1 {
				t.Fatalf("success err=%v calls=%d", err, calls)
			}
		})
	}
}
