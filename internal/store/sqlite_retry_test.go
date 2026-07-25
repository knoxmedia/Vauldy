package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func sqliteTestError(t *testing.T, code int) error {
	t.Helper()
	err := &sqlite.Error{}
	field := reflect.ValueOf(err).Elem().FieldByName("code")
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().SetInt(int64(code))
	return err
}

func TestIsSQLiteBusy(t *testing.T) {
	busy := sqliteTestError(t, sqlite3.SQLITE_BUSY)
	extendedBusy := sqliteTestError(t, sqlite3.SQLITE_BUSY_SNAPSHOT)

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "base busy", err: busy, want: true},
		{name: "extended busy", err: extendedBusy, want: true},
		{name: "wrapped busy", err: fmt.Errorf("wrapped: %w", busy), want: true},
		{name: "locked", err: sqliteTestError(t, sqlite3.SQLITE_LOCKED)},
		{name: "constraint", err: sqliteTestError(t, sqlite3.SQLITE_CONSTRAINT)},
		{name: "io error", err: sqliteTestError(t, sqlite3.SQLITE_IOERR)},
		{name: "ordinary", err: errors.New("ordinary failure")},
		{name: "busy text only", err: errors.New("database is busy (SQLITE_BUSY)")},
		{name: "context canceled", err: context.Canceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSQLiteBusy(tt.err); got != tt.want {
				t.Fatalf("IsSQLiteBusy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSQLiteConstraint(t *testing.T) {
	constraint := sqliteTestError(t, sqlite3.SQLITE_CONSTRAINT)
	extended := sqliteTestError(t, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"base", constraint, true},
		{"extended", extended, true},
		{"wrapped", fmt.Errorf("wrapped: %w", extended), true},
		{"busy", sqliteTestError(t, sqlite3.SQLITE_BUSY), false},
		{"ordinary", errors.New("constraint text"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSQLiteConstraint(tc.err); got != tc.want {
				t.Fatalf("IsSQLiteConstraint()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestWithBusyRetrySuccessFirstAttempt(t *testing.T) {
	var calls int
	var sleeps []time.Duration
	metrics := &SQLiteMetrics{}

	err := withBusyRetry(context.Background(), metrics,
		func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		func(delay time.Duration) time.Duration { return delay },
		func() error {
			calls++
			return nil
		},
	)

	if err != nil || calls != 1 || len(sleeps) != 0 {
		t.Fatalf("err=%v calls=%d sleeps=%v", err, calls, sleeps)
	}
	if got := metrics.BusyRetries.Load(); got != 0 {
		t.Fatalf("BusyRetries=%d, want 0", got)
	}
	if got := metrics.BusyExhausted.Load(); got != 0 {
		t.Fatalf("BusyExhausted=%d, want 0", got)
	}
}

func TestWithBusyRetrySucceedsAfterRetries(t *testing.T) {
	busy := sqliteTestError(t, sqlite3.SQLITE_BUSY)
	var calls int
	var sleeps []time.Duration
	metrics := &SQLiteMetrics{}

	err := withBusyRetry(context.Background(), metrics,
		func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		func(delay time.Duration) time.Duration { return delay },
		func() error {
			calls++
			if calls <= 3 {
				return busy
			}
			return nil
		},
	)

	if err != nil {
		t.Fatalf("withBusyRetry() error = %v", err)
	}
	if calls != 4 {
		t.Fatalf("calls=%d, want 4", calls)
	}
	wantSleeps := []time.Duration{25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}
	if !reflect.DeepEqual(sleeps, wantSleeps) {
		t.Fatalf("sleeps=%v, want %v", sleeps, wantSleeps)
	}
	if got := metrics.BusyRetries.Load(); got != 3 {
		t.Fatalf("BusyRetries=%d, want 3", got)
	}
	if got := metrics.BusyExhausted.Load(); got != 0 {
		t.Fatalf("BusyExhausted=%d, want 0", got)
	}
}

func TestWithBusyRetryExhaustsFiveAttempts(t *testing.T) {
	busy := sqliteTestError(t, sqlite3.SQLITE_BUSY_TIMEOUT)
	var calls int
	var sleeps []time.Duration
	metrics := &SQLiteMetrics{}

	err := withBusyRetry(context.Background(), metrics,
		func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		func(delay time.Duration) time.Duration { return delay },
		func() error { calls++; return busy },
	)

	if err != busy {
		t.Fatalf("error=%v, want original busy error", err)
	}
	if calls != 5 {
		t.Fatalf("calls=%d, want 5", calls)
	}
	wantSleeps := []time.Duration{25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	if !reflect.DeepEqual(sleeps, wantSleeps) {
		t.Fatalf("sleeps=%v, want %v", sleeps, wantSleeps)
	}
	if got := metrics.BusyRetries.Load(); got != 4 {
		t.Fatalf("BusyRetries=%d, want 4", got)
	}
	if got := metrics.BusyExhausted.Load(); got != 1 {
		t.Fatalf("BusyExhausted=%d, want 1", got)
	}
}

func TestWithBusyRetryDoesNotRetryOtherErrors(t *testing.T) {
	tests := []struct {
		name string
		err  func(*testing.T) error
	}{
		{name: "locked", err: func(t *testing.T) error { return sqliteTestError(t, sqlite3.SQLITE_LOCKED) }},
		{name: "constraint", err: func(t *testing.T) error { return sqliteTestError(t, sqlite3.SQLITE_CONSTRAINT) }},
		{name: "io error", err: func(t *testing.T) error { return sqliteTestError(t, sqlite3.SQLITE_IOERR) }},
		{name: "ordinary", err: func(*testing.T) error { return errors.New("ordinary") }},
		{name: "busy text only", err: func(*testing.T) error { return errors.New("busy SQLITE_BUSY") }},
		{name: "wrapped canceled", err: func(*testing.T) error { return fmt.Errorf("op: %w", context.Canceled) }},
		{name: "wrapped deadline", err: func(*testing.T) error { return fmt.Errorf("op: %w", context.DeadlineExceeded) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantErr := tt.err(t)
			calls := 0
			sleeps := 0
			err := withBusyRetry(context.Background(), nil,
				func(context.Context, time.Duration) error { sleeps++; return nil },
				func(delay time.Duration) time.Duration { return delay },
				func() error { calls++; return wantErr },
			)
			if err != wantErr {
				t.Fatalf("error=%v, want original %v", err, wantErr)
			}
			if calls != 1 || sleeps != 0 {
				t.Fatalf("calls=%d sleeps=%d, want 1 and 0", calls, sleeps)
			}
		})
	}
}

func TestWithBusyRetryChecksCanceledContextBeforeOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0

	err := WithBusyRetry(ctx, nil, func() error { calls++; return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("calls=%d, want 0", calls)
	}
}

func TestWithBusyRetryStopsWhenWaitIsCanceled(t *testing.T) {
	busy := sqliteTestError(t, sqlite3.SQLITE_BUSY)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	metrics := &SQLiteMetrics{}

	err := withBusyRetry(ctx, metrics,
		func(ctx context.Context, _ time.Duration) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
		func(delay time.Duration) time.Duration { return delay },
		func() error { calls++; return busy },
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
	if got := metrics.BusyRetries.Load(); got != 1 {
		t.Fatalf("BusyRetries=%d, want 1", got)
	}
	if got := metrics.BusyExhausted.Load(); got != 0 {
		t.Fatalf("BusyExhausted=%d, want 0", got)
	}
}

func TestJitterDelayStaysWithinConfiguredRange(t *testing.T) {
	bases := []time.Duration{
		time.Nanosecond,
		25 * time.Millisecond,
		200 * time.Millisecond,
	}

	for _, base := range bases {
		for i := 0; i < 10_000; i++ {
			got := jitterDelay(base)
			max := base + base/4
			if got < base || got > max {
				t.Fatalf("jitterDelay(%v)=%v, want in [%v, %v]", base, got, base, max)
			}
		}
	}
}

func TestWithBusyRetryNilMetricsExhaustsSafely(t *testing.T) {
	busy := sqliteTestError(t, sqlite3.SQLITE_BUSY)
	calls := 0
	sleeps := 0

	err := withBusyRetry(context.Background(), nil,
		func(context.Context, time.Duration) error {
			sleeps++
			return nil
		},
		func(delay time.Duration) time.Duration { return delay },
		func() error {
			calls++
			return busy
		},
	)

	if err != busy {
		t.Fatalf("error=%v, want original busy error", err)
	}
	if calls != 5 {
		t.Fatalf("calls=%d, want 5", calls)
	}
	if sleeps != 4 {
		t.Fatalf("sleeps=%d, want 4", sleeps)
	}
}

func TestSQLiteBusyIntegrationRetriesRealDriverBusyAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.sqlite")
	bootstrap, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.Exec(`CREATE TABLE busy_probe (id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}

	dsn := path + "?_pragma=busy_timeout(1)&_pragma=journal_mode(WAL)"
	locker, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	conn, err := locker.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	released := make(chan error, 1)
	go func() {
		<-release
		_, err := conn.ExecContext(ctx, `COMMIT`)
		released <- err
	}()
	metrics := &SQLiteMetrics{}
	var delays []time.Duration
	err = withBusyRetry(ctx, metrics, func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		if len(delays) == 4 {
			close(release)
			return <-released
		}
		return nil
	}, func(delay time.Duration) time.Duration { return delay }, func() error {
		_, err := writer.ExecContext(ctx, `INSERT INTO busy_probe(value) VALUES ('ok')`)
		return err
	})
	if err != nil {
		t.Fatalf("real busy retry: %v", err)
	}
	want := []time.Duration{25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	if !reflect.DeepEqual(delays, want) {
		t.Fatalf("delays=%v want=%v", delays, want)
	}
	if metrics.BusyRetries.Load() != 4 || metrics.BusyExhausted.Load() != 0 {
		t.Fatalf("metrics retries=%d exhausted=%d", metrics.BusyRetries.Load(), metrics.BusyExhausted.Load())
	}
}

func TestSQLiteBusyIntegrationDoesNotRetryConstraintContextLockedOrIO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonbusy.sqlite")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE unique_probe (value TEXT UNIQUE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO unique_probe(value) VALUES ('duplicate')`); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		ctx  context.Context
		op   func() error
	}{
		{name: "constraint", ctx: context.Background(), op: func() error { _, err := db.Exec(`INSERT INTO unique_probe(value) VALUES ('duplicate')`); return err }},
		{name: "locked", ctx: context.Background(), op: func() error { return sqliteTestError(t, sqlite3.SQLITE_LOCKED) }},
		{name: "io", ctx: context.Background(), op: func() error { return sqliteTestError(t, sqlite3.SQLITE_IOERR) }},
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests = append(tests, struct {
		name string
		ctx  context.Context
		op   func() error
	}{name: "context", ctx: cancelled, op: func() error { return nil }})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, sleeps := 0, 0
			metrics := &SQLiteMetrics{}
			err := withBusyRetry(tt.ctx, metrics, func(context.Context, time.Duration) error { sleeps++; return nil }, func(d time.Duration) time.Duration { return d }, func() error { calls++; return tt.op() })
			if err == nil {
				t.Fatal("expected error")
			}
			wantCalls := 1
			if tt.name == "context" {
				wantCalls = 0
			}
			if calls != wantCalls || sleeps != 0 || metrics.BusyRetries.Load() != 0 || metrics.BusyExhausted.Load() != 0 {
				t.Fatalf("calls=%d sleeps=%d retries=%d exhausted=%d", calls, sleeps, metrics.BusyRetries.Load(), metrics.BusyExhausted.Load())
			}
		})
	}
}

func TestSQLiteBusyIntegrationDoesNotRetryRealLockedOpenStatement(t *testing.T) {
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "locked.sqlite"))
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE locked_probe (id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO locked_probe(value) VALUES ('held')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, `SELECT value FROM locked_probe`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("reader did not keep a statement open")
	}

	calls, sleeps := 0, 0
	metrics := &SQLiteMetrics{}
	err = withBusyRetry(ctx, metrics, func(context.Context, time.Duration) error { sleeps++; return nil }, func(d time.Duration) time.Duration { return d }, func() error {
		calls++
		_, err := conn.ExecContext(ctx, `DROP TABLE locked_probe`)
		return err
	})
	if err == nil {
		t.Fatal("DROP with an open statement succeeded")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("error=%T %v want modernc sqlite error", err, err)
	}
	if sqliteErr.Code()&0xff != sqlite3.SQLITE_LOCKED {
		t.Fatalf("error=%v code=%d want real SQLITE_LOCKED", err, sqliteErr.Code())
	}
	if IsSQLiteBusy(err) {
		t.Fatalf("real SQLITE_LOCKED classified busy: %v", err)
	}
	if calls != 1 || sleeps != 0 || metrics.BusyRetries.Load() != 0 || metrics.BusyExhausted.Load() != 0 {
		t.Fatalf("calls=%d sleeps=%d retries=%d exhausted=%d", calls, sleeps, metrics.BusyRetries.Load(), metrics.BusyExhausted.Load())
	}
}
func TestSQLiteBusyIntegrationDoesNotRetryRealReadonlyStorageError(t *testing.T) {
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "readonly.sqlite"))
	writable, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.Exec(`CREATE TABLE readonly_probe(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	readonly, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if err := readonly.Ping(); err != nil {
		t.Fatal(err)
	}
	calls, sleeps := 0, 0
	metrics := &SQLiteMetrics{}
	err = withBusyRetry(context.Background(), metrics, func(context.Context, time.Duration) error { sleeps++; return nil }, func(d time.Duration) time.Duration { return d }, func() error {
		calls++
		_, err := readonly.Exec(`INSERT INTO readonly_probe(value) VALUES ('denied')`)
		return err
	})
	if err == nil {
		t.Fatal("write through mode=ro succeeded")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_READONLY {
		t.Fatalf("error=%T %v code=%d want real SQLITE_READONLY", err, err, sqliteErr.Code())
	}
	if IsSQLiteBusy(err) {
		t.Fatalf("real SQLITE_READONLY classified busy: %v", err)
	}
	if calls != 1 || sleeps != 0 || metrics.BusyRetries.Load() != 0 || metrics.BusyExhausted.Load() != 0 {
		t.Fatalf("calls=%d sleeps=%d retries=%d exhausted=%d", calls, sleeps, metrics.BusyRetries.Load(), metrics.BusyExhausted.Load())
	}
}

func TestWithBusyRetryPolicyStopsAtBudget(t *testing.T) {
	busy := sqliteTestError(t, sqlite3.SQLITE_BUSY_SNAPSHOT)
	now := time.Unix(100, 0)
	calls := 0
	var sleeps []time.Duration
	policy := RetryPolicy{Operation: "claim", MaxElapsed: 70 * time.Millisecond, BaseBackoff: 25 * time.Millisecond, MaxBackoff: 100 * time.Millisecond}

	err := withBusyRetryPolicy(context.Background(), nil, policy,
		func() time.Time { return now },
		func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			now = now.Add(delay)
			return nil
		},
		func(base, _ time.Duration) time.Duration { return base },
		func() error { calls++; return busy },
	)
	if err != busy {
		t.Fatalf("error=%v, want original busy error", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
	if want := []time.Duration{25 * time.Millisecond}; !reflect.DeepEqual(sleeps, want) {
		t.Fatalf("sleeps=%v, want %v", sleeps, want)
	}
}

func TestWithBusyRetryPolicyUsesHeartbeatLeaseBudget(t *testing.T) {
	policy := HeartbeatLeaseRetryPolicy("heartbeat", 800*time.Millisecond, 150*time.Millisecond)
	if policy.Operation != "heartbeat" {
		t.Fatalf("operation=%q", policy.Operation)
	}
	if policy.MaxElapsed != 650*time.Millisecond {
		t.Fatalf("MaxElapsed=%v, want 650ms", policy.MaxElapsed)
	}
	if policy.BaseBackoff <= 0 || policy.MaxBackoff < policy.BaseBackoff {
		t.Fatalf("invalid backoff bounds: base=%v max=%v", policy.BaseBackoff, policy.MaxBackoff)
	}

	zero := HeartbeatLeaseRetryPolicy("heartbeat", 100*time.Millisecond, 150*time.Millisecond)
	if zero.MaxElapsed != 0 {
		t.Fatalf("exhausted lease MaxElapsed=%v, want 0", zero.MaxElapsed)
	}
}

func TestWithBusyRetryPolicyContextStoreWrapperAndOperationMetrics(t *testing.T) {
	busy := sqliteTestError(t, sqlite3.SQLITE_BUSY_SNAPSHOT)
	metrics := &SQLiteMetrics{}
	calls := 0
	err := WithBusyRetryPolicyContext(context.Background(), metrics, RetryPolicy{Operation: "heartbeat", MaxElapsed: time.Second, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, func(context.Context) error {
		calls++
		if calls == 2 {
			return nil
		}
		return fmt.Errorf("wrapped: %w", busy)
	})
	if err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if metrics.BusyRetries.Load() != 1 || metrics.OperationRetries("heartbeat") != 1 {
		t.Fatalf("retries=%d operation=%d", metrics.BusyRetries.Load(), metrics.OperationRetries("heartbeat"))
	}
}

func TestWithBusyRetryPolicyLegacyWrapperDoesNotCallAtBudgetBoundary(t *testing.T) {
	busy := sqliteTestError(t, sqlite3.SQLITE_BUSY)
	now := time.Unix(100, 0)
	calls := 0
	err := withBusyRetryPolicy(context.Background(), nil, RetryPolicy{MaxElapsed: 25 * time.Millisecond, BaseBackoff: 25 * time.Millisecond, MaxBackoff: 25 * time.Millisecond}, func() time.Time { return now }, func(context.Context, time.Duration) error { now = now.Add(25 * time.Millisecond); return nil }, func(base, _ time.Duration) time.Duration { return base }, func() error { calls++; return busy })
	if err != busy || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestWithBusyRetryPolicyContextZeroBudgetCountsExhaustedWithoutRetry(t *testing.T) {
	busy := sqliteTestError(t, sqlite3.SQLITE_BUSY)
	metrics := &SQLiteMetrics{}
	calls := 0
	err := WithBusyRetryPolicyContext(context.Background(), metrics, RetryPolicy{Operation: "heartbeat", MaxElapsed: 0}, func(ctx context.Context) error {
		calls++
		if ctx.Err() != nil {
			t.Fatalf("first context done: %v", ctx.Err())
		}
		return busy
	})
	if err != busy || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if metrics.BusyRetries.Load() != 0 || metrics.OperationRetries("heartbeat") != 0 || metrics.BusyExhausted.Load() != 1 {
		t.Fatalf("retries=%d op=%d exhausted=%d", metrics.BusyRetries.Load(), metrics.OperationRetries("heartbeat"), metrics.BusyExhausted.Load())
	}
}
