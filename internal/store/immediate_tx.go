package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
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
	conn, err := db.Conn(ctx)
	if err != nil {
		return outcome, err
	}
	discarded := false
	defer conn.Close()

	var restoreBusyTimeout func() error
	if deadline, ok := ctx.Deadline(); ok {
		var previous int64
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&previous); err != nil {
			return outcome, err
		}
		remaining := time.Until(deadline)
		bounded := remaining.Milliseconds()
		if bounded < 1 {
			bounded = 1
		}
		if previous <= 0 || bounded < previous {
			if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, bounded)); err != nil {
				return outcome, err
			}
			restoreBusyTimeout = func() error {
				_, restoreErr := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA busy_timeout=%d`, previous))
				return restoreErr
			}
			defer func() {
				if discarded {
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

	if beginErr := immediateBegin(ctx, conn); beginErr != nil {
		err = beginErr
		cleanupCtx, cancel := context.WithTimeout(context.Background(), immediateCleanupTimeout)
		_, rollbackErr := conn.ExecContext(cleanupCtx, `ROLLBACK`)
		cancel()
		if rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("store: rollback after failed immediate transaction begin: %w", rollbackErr))
		}
		if errors.Is(beginErr, context.Canceled) || errors.Is(beginErr, context.DeadlineExceeded) || rollbackErr != nil {
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
