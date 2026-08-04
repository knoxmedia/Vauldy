package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"knox-media/internal/sqliteretry"
)

// SQLExecutor is the subset of database/sql execution methods available inside
// an immediate transaction.
type SQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ImmediateConnTx exposes SQL execution without transaction lifecycle methods.
type ImmediateConnTx interface {
	SQLExecutor
}

type ImmediateOutcome struct {
	CommitAttempted bool
	CommitConfirmed bool
}

type ImmediateBeginRetryError struct {
	Cause error
}

func (e *ImmediateBeginRetryError) Error() string {
	return fmt.Sprintf("store: immediate transaction begin timed out: %v", e.Cause)
}

func (e *ImmediateBeginRetryError) Unwrap() error { return e.Cause }

// RetryableSQLiteOperation marks only begin acquisition timeout as retryable.
func (e *ImmediateBeginRetryError) RetryableSQLiteOperation() bool { return true }

func IsImmediateBeginRetry(err error) bool {
	var retryErr *ImmediateBeginRetryError
	return errors.As(err, &retryErr)
}

type ImmediateCommitError struct {
	Cause error
}

func (e *ImmediateCommitError) Error() string {
	return fmt.Sprintf("store: immediate transaction commit outcome uncertain: %v", e.Cause)
}

func (e *ImmediateCommitError) Unwrap() error { return e.Cause }

var immediateBegin = func(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	return err
}

const immediateCleanupTimeout = time.Second

var immediateCommit = func(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `COMMIT`)
	return err
}

func WithImmediateConnTx(ctx context.Context, db *sql.DB, fn func(ImmediateConnTx) error) (outcome ImmediateOutcome, err error) {
	return withImmediateConnTx(ctx, db, 0, fn)
}

// WithImmediateConnTxBeginTimeout bounds only BEGIN IMMEDIATE acquisition. The
// caller context remains in force for the transaction body and commit.
func WithImmediateConnTxBeginTimeout(ctx context.Context, db *sql.DB, beginTimeout time.Duration, fn func(ImmediateConnTx) error) (outcome ImmediateOutcome, err error) {
	return withImmediateConnTx(ctx, db, beginTimeout, fn)
}

func withImmediateConnTx(ctx context.Context, db *sql.DB, beginTimeout time.Duration, fn func(ImmediateConnTx) error) (outcome ImmediateOutcome, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return outcome, err
	}
	discarded := false
	defer conn.Close()

	beginCtx := ctx
	cancelBegin := func() {}
	if beginTimeout > 0 {
		beginCtx, cancelBegin = context.WithTimeout(ctx, beginTimeout)
	}
	defer cancelBegin()

	var restoreBusyTimeout func() error
	busyTimeoutRestored := false
	if deadline, ok := beginCtx.Deadline(); ok {
		var previous int64
		if err := conn.QueryRowContext(beginCtx, `PRAGMA busy_timeout`).Scan(&previous); err != nil {
			return outcome, err
		}
		remaining := time.Until(deadline)
		bounded := remaining.Milliseconds()
		if bounded < 1 {
			bounded = 1
		}
		if previous <= 0 || bounded < previous {
			if _, err := conn.ExecContext(beginCtx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, bounded)); err != nil {
				return outcome, err
			}
			restoreBusyTimeout = func() error {
				if busyTimeoutRestored {
					return nil
				}
				_, restoreErr := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA busy_timeout=%d`, previous))
				if restoreErr == nil {
					busyTimeoutRestored = true
				}
				return restoreErr
			}
			defer func() {
				if discarded || busyTimeoutRestored {
					return
				}
				if restoreErr := restoreBusyTimeout(); restoreErr != nil {
					err = errors.Join(err, fmt.Errorf("store: restore immediate transaction busy timeout: %w", restoreErr))
					discarded = true
					if discardErr := discardSQLConn(conn); discardErr != nil {
						err = errors.Join(err, fmt.Errorf("store: discard connection after busy timeout restore failure: %w", discardErr))
					}
				}
			}()
		}
	}

	beginErr := immediateBegin(beginCtx, conn)
	beginCtxErr := beginCtx.Err()
	cancelBegin()
	if beginErr != nil {
		err = beginErr
		if beginTimeout > 0 && errors.Is(beginCtxErr, context.DeadlineExceeded) && ctx.Err() == nil {
			err = &ImmediateBeginRetryError{Cause: beginErr}
		}
		if !ambiguousImmediateBegin(beginCtxErr, beginErr) {
			return outcome, err
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), immediateCleanupTimeout)
		_, rollbackErr := conn.ExecContext(cleanupCtx, `ROLLBACK`)
		cancel()
		clean := isNoActiveTransactionError(rollbackErr)
		if rollbackErr != nil && !clean {
			err = errors.Join(err, fmt.Errorf("store: rollback after failed immediate transaction begin: %w", rollbackErr))
		}
		if !clean {
			discarded = true
			if discardErr := discardSQLConn(conn); discardErr != nil {
				err = errors.Join(err, fmt.Errorf("store: discard connection after ambiguous immediate transaction begin: %w", discardErr))
			}
		}
		return outcome, err
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), immediateCleanupTimeout)
		_, rollbackErr := conn.ExecContext(cleanupCtx, `ROLLBACK`)
		cancel()
		if rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("store: rollback immediate transaction: %w", rollbackErr))
		}
		if outcome.CommitAttempted || rollbackErr != nil {
			discarded = true
			if discardErr := discardSQLConn(conn); discardErr != nil {
				err = errors.Join(err, fmt.Errorf("store: discard uncertain immediate transaction connection: %w", discardErr))
			}
		}
	}()

	if err := fn(conn); err != nil {
		return outcome, err
	}
	outcome.CommitAttempted = true
	if err := immediateCommit(ctx, conn); err != nil {
		return outcome, &ImmediateCommitError{Cause: err}
	}
	outcome.CommitConfirmed = true
	finished = true
	return outcome, nil
}

func discardSQLConn(conn *sql.Conn) error {
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}

func ambiguousImmediateBegin(ctxErr, beginErr error) bool {
	if ctxErr != nil || errors.Is(beginErr, context.Canceled) || errors.Is(beginErr, context.DeadlineExceeded) {
		return true
	}
	_, _, ok := sqliteretry.ErrorCodes(beginErr)
	return !ok
}

func isNoActiveTransactionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return message == "cannot rollback - no transaction is active" || message == "sql logic error: cannot rollback - no transaction is active (1)"
}
