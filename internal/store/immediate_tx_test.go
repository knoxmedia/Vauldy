package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func openImmediateTxTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "immediate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE immediate_test (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestWithImmediateConnTxCommits(t *testing.T) {
	db := openImmediateTxTestDB(t)
	outcome, err := WithImmediateConnTx(context.Background(), db, func(tx ImmediateConnTx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO immediate_test(value) VALUES ('committed')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.CommitAttempted || !outcome.CommitConfirmed {
		t.Fatalf("outcome=%+v want attempted and confirmed", outcome)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM immediate_test WHERE value='committed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d want 1", count)
	}
}

func TestWithImmediateConnTxRollsBackCallbackError(t *testing.T) {
	db := openImmediateTxTestDB(t)
	callbackErr := errors.New("callback failed")
	outcome, err := WithImmediateConnTx(context.Background(), db, func(tx ImmediateConnTx) error {
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO immediate_test(value) VALUES ('rolled-back')`); err != nil {
			return err
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("error=%v want callback error", err)
	}
	if outcome.CommitAttempted || outcome.CommitConfirmed {
		t.Fatalf("outcome=%+v want no commit", outcome)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM immediate_test`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count=%d want rollback", count)
	}
}

func TestWithImmediateConnTxReportsCommitUncertain(t *testing.T) {
	db := openImmediateTxTestDB(t)
	original := immediateCommit
	commitErr := errors.New("commit response lost")
	immediateCommit = func(context.Context, *sql.Conn) error { return commitErr }
	t.Cleanup(func() { immediateCommit = original })

	outcome, err := WithImmediateConnTx(context.Background(), db, func(tx ImmediateConnTx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO immediate_test(value) VALUES ('uncertain')`)
		return err
	})
	var uncertain *ImmediateCommitError
	if !errors.As(err, &uncertain) || !errors.Is(err, commitErr) {
		t.Fatalf("error=%T %v want ImmediateCommitError wrapping commit error", err, err)
	}
	if !outcome.CommitAttempted || outcome.CommitConfirmed {
		t.Fatalf("outcome=%+v want attempted but unconfirmed", outcome)
	}
}

func TestWithImmediateConnTxSerializesWriters(t *testing.T) {
	db := openImmediateTxTestDB(t)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)

	go func() {
		ready.Done()
		_, err := WithImmediateConnTx(context.Background(), db, func(tx ImmediateConnTx) error {
			close(firstEntered)
			<-releaseFirst
			_, err := tx.ExecContext(context.Background(), `INSERT INTO immediate_test(value) VALUES ('first')`)
			return err
		})
		errs <- err
	}()
	<-firstEntered
	go func() {
		ready.Done()
		_, err := WithImmediateConnTx(context.Background(), db, func(tx ImmediateConnTx) error {
			close(secondEntered)
			_, err := tx.ExecContext(context.Background(), `INSERT INTO immediate_test(value) VALUES ('second')`)
			return err
		})
		errs <- err
	}()
	ready.Wait()

	select {
	case <-secondEntered:
		t.Fatal("second writer entered before first transaction completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("second writer did not enter after first transaction completed")
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

type immediateRollbackTestDriver struct {
	opens       atomic.Int32
	closes      atomic.Int32
	beginErr    error
	rollbackErr error
	rollbacks   atomic.Int32
}

func (d *immediateRollbackTestDriver) Open(string) (driver.Conn, error) {
	d.opens.Add(1)
	return &immediateRollbackTestConn{driver: d}, nil
}

type immediateRollbackTestConn struct {
	driver *immediateRollbackTestDriver
}

func (c *immediateRollbackTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *immediateRollbackTestConn) Close() error {
	c.driver.closes.Add(1)
	return nil
}
func (c *immediateRollbackTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("begin unsupported")
}
func (c *immediateRollbackTestConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	switch query {
	case "BEGIN IMMEDIATE":
		return driver.RowsAffected(0), c.driver.beginErr
	case "ROLLBACK":
		c.driver.rollbacks.Add(1)
		return driver.RowsAffected(0), c.driver.rollbackErr
	default:
		return driver.RowsAffected(0), nil
	}
}

var immediateRollbackDriverSequence atomic.Int64

func openImmediateRollbackTestDB(t *testing.T, testDriver *immediateRollbackTestDriver) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("immediate-rollback-%d", immediateRollbackDriverSequence.Add(1))
	sql.Register(name, testDriver)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type immediateTestRollbackError struct{ message string }

func (e *immediateTestRollbackError) Error() string { return e.message }

func TestWithImmediateConnTxRollbackFailurePreservesErrorsAndDiscardsConnection(t *testing.T) {
	rollbackErr := &immediateTestRollbackError{message: "rollback failed"}
	testDriver := &immediateRollbackTestDriver{rollbackErr: rollbackErr}
	db := openImmediateRollbackTestDB(t, testDriver)
	callbackErr := errors.New("callback failed")

	_, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("error=%v want callback and rollback errors", err)
	}
	var typedRollback *immediateTestRollbackError
	if !errors.As(err, &typedRollback) || typedRollback != rollbackErr {
		t.Fatalf("error=%v does not preserve typed rollback error", err)
	}
	if got := testDriver.closes.Load(); got != 1 {
		t.Fatalf("closed connections=%d want failed transaction connection discarded", got)
	}
	if _, err := db.ExecContext(context.Background(), "probe"); err != nil {
		t.Fatal(err)
	}
	if got := testDriver.opens.Load(); got != 2 {
		t.Fatalf("opened connections=%d want replacement after discard", got)
	}
}

func TestWithImmediateConnTxBeginContextCancellationDiscardsConnection(t *testing.T) {
	testDriver := &immediateRollbackTestDriver{beginErr: context.Canceled}
	db := openImmediateRollbackTestDB(t, testDriver)

	_, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error {
		t.Fatal("callback called after failed begin")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context cancellation", err)
	}
	if got := testDriver.closes.Load(); got != 1 {
		t.Fatalf("closed connections=%d want ambiguous begin connection discarded", got)
	}
	testDriver.beginErr = nil
	if _, err := db.ExecContext(context.Background(), "probe"); err != nil {
		t.Fatal(err)
	}
	if got := testDriver.opens.Load(); got != 2 {
		t.Fatalf("opened connections=%d want replacement after ambiguous begin", got)
	}
}

func TestWithImmediateConnTxCallbackContextCancellationRetainsConnectionAfterRollback(t *testing.T) {
	testDriver := &immediateRollbackTestDriver{}
	db := openImmediateRollbackTestDB(t, testDriver)

	_, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error {
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context cancellation", err)
	}
	if got := testDriver.closes.Load(); got != 0 {
		t.Fatalf("closed connections=%d want successfully rolled back connection retained", got)
	}
	if _, err := db.ExecContext(context.Background(), "probe"); err != nil {
		t.Fatal(err)
	}
	if got := testDriver.opens.Load(); got != 1 {
		t.Fatalf("opened connections=%d want original connection reused", got)
	}
}

func TestWithImmediateConnTxCommitAndRollbackFailurePreservesBothAndDiscardsConnection(t *testing.T) {
	rollbackErr := &immediateTestRollbackError{message: "rollback after commit failed"}
	testDriver := &immediateRollbackTestDriver{rollbackErr: rollbackErr}
	db := openImmediateRollbackTestDB(t, testDriver)
	original := immediateCommit
	commitErr := errors.New("commit failed")
	immediateCommit = func(context.Context, *sql.Conn) error { return commitErr }
	t.Cleanup(func() { immediateCommit = original })

	outcome, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { return nil })
	var uncertain *ImmediateCommitError
	if !errors.As(err, &uncertain) || !errors.Is(err, commitErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("error=%v want commit uncertainty and rollback errors", err)
	}
	if !outcome.CommitAttempted || outcome.CommitConfirmed {
		t.Fatalf("outcome=%+v want attempted but unconfirmed", outcome)
	}
	if got := testDriver.closes.Load(); got != 1 {
		t.Fatalf("closed connections=%d want failed transaction connection discarded", got)
	}
}

func TestWithImmediateConnTxAmbiguousBeginCancellationDoesNotContaminatePool(t *testing.T) {
	db := openImmediateTxTestDB(t)
	db.SetMaxOpenConns(1)
	original := immediateBegin
	ambiguousBegin := func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			return err
		}
		return context.Canceled
	}
	immediateBegin = ambiguousBegin
	t.Cleanup(func() { immediateBegin = original })

	for i := 0; i < 100; i++ {
		_, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error {
			t.Fatal("callback called after ambiguous begin")
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: error=%v want context cancellation", i, err)
		}
		immediateBegin = original
		if _, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { return nil }); err != nil {
			t.Fatalf("iteration %d: replacement transaction: %v", i, err)
		}
		immediateBegin = ambiguousBegin
	}
}

func TestWithImmediateConnTxBeginCancellationRollbackFailureDiscardsConnection(t *testing.T) {
	rollbackErr := errors.New("rollback failed")
	testDriver := &immediateRollbackTestDriver{beginErr: context.Canceled, rollbackErr: rollbackErr}
	db := openImmediateRollbackTestDB(t, testDriver)

	_, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { return nil })
	if !errors.Is(err, context.Canceled) || !errors.Is(err, rollbackErr) {
		t.Fatalf("error=%v want cancellation and rollback failure", err)
	}
	if got := testDriver.rollbacks.Load(); got != 1 {
		t.Fatalf("rollbacks=%d want cleanup attempted", got)
	}
	if got := testDriver.closes.Load(); got != 1 {
		t.Fatalf("closed connections=%d want ambiguous connection discarded", got)
	}
}

func TestWithImmediateConnTxCommitErrorAlwaysDiscardsConnection(t *testing.T) {
	testDriver := &immediateRollbackTestDriver{}
	db := openImmediateRollbackTestDB(t, testDriver)
	original := immediateCommit
	commitErr := errors.New("commit response lost")
	immediateCommit = func(context.Context, *sql.Conn) error { return commitErr }
	t.Cleanup(func() { immediateCommit = original })

	_, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { return nil })
	var uncertain *ImmediateCommitError
	if !errors.As(err, &uncertain) || !errors.Is(err, commitErr) {
		t.Fatalf("error=%v want uncertain commit", err)
	}
	if got := testDriver.rollbacks.Load(); got != 1 {
		t.Fatalf("rollbacks=%d want cleanup attempted", got)
	}
	if got := testDriver.closes.Load(); got != 1 {
		t.Fatalf("closed connections=%d want uncertain connection discarded", got)
	}
}

func TestWithImmediateConnTxRealBeginCancellationDoesNotRetainWriterLock(t *testing.T) {
	db := openImmediateTxTestDB(t)
	db.SetMaxOpenConns(2)

	for i := 0; i < 25; i++ {
		locker, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = locker.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
			_ = locker.Close()
			t.Fatalf("iteration %d: lock writer: %v", i, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		_, beginErr := WithImmediateConnTx(ctx, db, func(ImmediateConnTx) error {
			t.Fatal("callback entered while writer lock held")
			return nil
		})
		ctxErr := ctx.Err()
		cancel()
		if ctxErr != context.DeadlineExceeded && !IsSQLiteBusy(beginErr) {
			_ = locker.Close()
			t.Fatalf("iteration %d: begin error=%v context error=%v want deadline or busy", i, beginErr, ctxErr)
		}
		if _, err = locker.ExecContext(context.Background(), `ROLLBACK`); err != nil {
			_ = locker.Close()
			t.Fatalf("iteration %d: release writer: %v", i, err)
		}
		if err = locker.Close(); err != nil {
			t.Fatalf("iteration %d: close writer: %v", i, err)
		}
		if _, err = WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { return nil }); err != nil {
			t.Fatalf("iteration %d: transaction after cancellation: %v", i, err)
		}
	}
}

type countingSQLiteDriver struct {
	inner  driver.Driver
	opens  atomic.Int32
	closes atomic.Int32
}

func (d *countingSQLiteDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	d.opens.Add(1)
	return &countingSQLiteConn{Conn: conn, closes: &d.closes}, nil
}

type countingSQLiteConn struct {
	driver.Conn
	closes *atomic.Int32
}

func (c *countingSQLiteConn) Close() error {
	c.closes.Add(1)
	return c.Conn.Close()
}

func (c *countingSQLiteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *countingSQLiteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

var countingSQLiteDriverSequence atomic.Int64

func TestWithImmediateConnTxLiveBusyRetainsPhysicalConnection(t *testing.T) {
	driverName := fmt.Sprintf("counting-sqlite-%d", countingSQLiteDriverSequence.Add(1))
	counter := &countingSQLiteDriver{inner: &sqlite.Driver{}}
	sql.Register(driverName, counter)
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "busy-retain.sqlite"))
	db, err := sql.Open(driverName, "file:"+path+"?_pragma=busy_timeout(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`CREATE TABLE busy_retain(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	locker, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = locker.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = locker.ExecContext(context.Background(), `ROLLBACK`); _ = locker.Close() })

	for i := 0; i < 25; i++ {
		_, beginErr := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error {
			t.Fatal("callback entered while writer lock held")
			return nil
		})
		if !IsSQLiteBusy(beginErr) {
			t.Fatalf("iteration %d: error=%v want SQLite busy", i, beginErr)
		}
	}
	if got := counter.opens.Load(); got != 2 {
		t.Fatalf("opened physical connections=%d want locker plus one retained busy connection", got)
	}
	if got := counter.closes.Load(); got != 0 {
		t.Fatalf("closed physical connections=%d want no busy churn", got)
	}
}

func TestWithImmediateConnTxLiveLockedRetainsConnectionWithoutRollback(t *testing.T) {
	lockedErr := sqliteTestError(t, sqlite3.SQLITE_LOCKED)
	testDriver := &immediateRollbackTestDriver{beginErr: lockedErr, rollbackErr: errors.New("cannot rollback - no transaction is active")}
	db := openImmediateRollbackTestDB(t, testDriver)

	_, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { return nil })
	if !errors.Is(err, lockedErr) {
		t.Fatalf("error=%v want original locked error", err)
	}
	if got := testDriver.rollbacks.Load(); got != 0 {
		t.Fatalf("rollbacks=%d want definitive failed BEGIN left untouched", got)
	}
	if got := testDriver.closes.Load(); got != 0 {
		t.Fatalf("closed connections=%d want locked connection retained", got)
	}
}

func TestWithImmediateConnTxNoActiveRollbackProvesAmbiguousBeginClean(t *testing.T) {
	noActive := errors.New("SQL logic error: cannot rollback - no transaction is active (1)")
	testDriver := &immediateRollbackTestDriver{beginErr: context.Canceled, rollbackErr: noActive}
	db := openImmediateRollbackTestDB(t, testDriver)

	_, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want cancellation", err)
	}
	if errors.Is(err, noActive) {
		t.Fatalf("no-active cleanup error leaked: %v", err)
	}
	if got := testDriver.rollbacks.Load(); got != 1 {
		t.Fatalf("rollbacks=%d want ambiguity cleanup", got)
	}
	if got := testDriver.closes.Load(); got != 0 {
		t.Fatalf("closed connections=%d want proven-clean connection retained", got)
	}
}

func TestWithImmediateConnTxBeginTimeoutRetriesRealContention(t *testing.T) {
	db := openImmediateTxTestDB(t)
	db.SetMaxOpenConns(2)
	locker, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = locker.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(40 * time.Millisecond)
		_, _ = locker.ExecContext(context.Background(), `ROLLBACK`)
		_ = locker.Close()
		close(released)
	}()
	t.Cleanup(func() {
		<-released
	})

	attempts := 0
	bodyCalls := 0
	err = WithBusyRetryPolicyContext(context.Background(), nil, RetryPolicy{Operation: "immediate_begin_test", MaxElapsed: time.Second, BaseBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond}, func(attemptCtx context.Context) error {
		attempts++
		_, txErr := WithImmediateConnTxBeginTimeout(attemptCtx, db, 10*time.Millisecond, func(tx ImmediateConnTx) error {
			bodyCalls++
			_, execErr := tx.ExecContext(attemptCtx, `INSERT INTO immediate_test(value) VALUES ('after-contention')`)
			return execErr
		})
		return txErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts < 2 || bodyCalls != 1 {
		t.Fatalf("attempts=%d body calls=%d", attempts, bodyCalls)
	}
}

func TestWithImmediateConnTxBeginTimeoutOnlyBoundsBegin(t *testing.T) {
	db := openImmediateTxTestDB(t)
	original := immediateBegin
	var beginDeadline time.Time
	immediateBegin = func(ctx context.Context, conn *sql.Conn) error {
		beginDeadline, _ = ctx.Deadline()
		return original(ctx, conn)
	}
	t.Cleanup(func() { immediateBegin = original })

	started := time.Now()
	bodyCalls := 0
	outcome, err := WithImmediateConnTxBeginTimeout(context.Background(), db, 20*time.Millisecond, func(tx ImmediateConnTx) error {
		bodyCalls++
		time.Sleep(60 * time.Millisecond)
		_, err := tx.ExecContext(context.Background(), `INSERT INTO immediate_test(value) VALUES ('slow-body')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if bodyCalls != 1 || !outcome.CommitConfirmed {
		t.Fatalf("body calls=%d outcome=%+v", bodyCalls, outcome)
	}
	if beginDeadline.IsZero() || beginDeadline.Sub(started) > 50*time.Millisecond {
		t.Fatalf("begin deadline=%v started=%v", beginDeadline, started)
	}
}

func TestWithImmediateConnTxBeginTimeoutSignalsRetryableContention(t *testing.T) {
	db := openImmediateTxTestDB(t)
	original := immediateBegin
	immediateBegin = func(ctx context.Context, _ *sql.Conn) error {
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { immediateBegin = original })

	_, err := WithImmediateConnTxBeginTimeout(context.Background(), db, 10*time.Millisecond, func(ImmediateConnTx) error {
		t.Fatal("body called after timed out begin")
		return nil
	})
	if !IsImmediateBeginRetry(err) {
		t.Fatalf("error=%T %v want immediate begin retry signal", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v want preserved deadline cause", err)
	}
	if IsSQLiteBusy(err) {
		t.Fatalf("error=%v must not be misclassified as SQLite busy", err)
	}
}

func TestWithImmediateConnTxBeginTimeoutPreservesCallerCancellation(t *testing.T) {
	db := openImmediateTxTestDB(t)
	original := immediateBegin
	immediateBegin = func(ctx context.Context, _ *sql.Conn) error {
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { immediateBegin = original })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WithImmediateConnTxBeginTimeout(ctx, db, time.Second, func(ImmediateConnTx) error { return nil })
	if !errors.Is(err, context.Canceled) || IsImmediateBeginRetry(err) {
		t.Fatalf("error=%T %v want caller cancellation without retry signal", err, err)
	}
}

func TestWithImmediateConnTxBeginTimeoutPreservesCallerDeadline(t *testing.T) {
	db := openImmediateTxTestDB(t)
	original := immediateBegin
	immediateBegin = func(ctx context.Context, _ *sql.Conn) error {
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { immediateBegin = original })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := WithImmediateConnTxBeginTimeout(ctx, db, time.Second, func(ImmediateConnTx) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) || IsImmediateBeginRetry(err) {
		t.Fatalf("error=%T %v want caller deadline without retry signal", err, err)
	}
}

func TestWithImmediateConnTxBeginTimeoutPreservesAmbiguousCommit(t *testing.T) {
	db := openImmediateTxTestDB(t)
	original := immediateCommit
	commitErr := errors.New("commit response lost")
	immediateCommit = func(context.Context, *sql.Conn) error { return commitErr }
	t.Cleanup(func() { immediateCommit = original })

	outcome, err := WithImmediateConnTxBeginTimeout(context.Background(), db, 10*time.Millisecond, func(ImmediateConnTx) error { return nil })
	var uncertain *ImmediateCommitError
	if !errors.As(err, &uncertain) || !errors.Is(err, commitErr) || IsImmediateBeginRetry(err) {
		t.Fatalf("error=%T %v want ambiguous commit only", err, err)
	}
	if !outcome.CommitAttempted || outcome.CommitConfirmed {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestWithImmediateConnTxDefinitiveSQLErrorRetainsConnectionWithoutRollback(t *testing.T) {
	sqlErr := sqliteTestError(t, sqlite3.SQLITE_ERROR)
	testDriver := &immediateRollbackTestDriver{beginErr: sqlErr, rollbackErr: errors.New("cannot rollback - no transaction is active")}
	db := openImmediateRollbackTestDB(t, testDriver)

	_, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { return nil })
	if !errors.Is(err, sqlErr) {
		t.Fatalf("error=%v want original SQL error", err)
	}
	if got := testDriver.rollbacks.Load(); got != 0 {
		t.Fatalf("rollbacks=%d want definitive failed BEGIN left untouched", got)
	}
	if got := testDriver.closes.Load(); got != 0 {
		t.Fatalf("closed connections=%d want connection retained", got)
	}
}

func TestImmediateOutcomeReportsBodyStarted(t *testing.T) {
	db := openImmediateTxTestDB(t)
	want := errors.New("body")
	out, err := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { return want })
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	if !out.BodyStarted || out.CommitAttempted {
		t.Fatalf("outcome=%+v", out)
	}
}

func TestImmediateOutcomeBeginFailureBodyNotStarted(t *testing.T) {
	testDriver := &immediateRollbackTestDriver{beginErr: errors.New("begin failed")}
	db := openImmediateRollbackTestDB(t, testDriver)
	out, _ := WithImmediateConnTx(context.Background(), db, func(ImmediateConnTx) error { t.Fatal("body called"); return nil })
	if out.BodyStarted {
		t.Fatalf("outcome=%+v", out)
	}
}
