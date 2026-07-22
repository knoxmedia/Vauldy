package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
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

var immediateCommit = func(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `COMMIT`)
	return err
}

func WithImmediateConnTx(ctx context.Context, db *sql.DB, fn func(ImmediateConnTx) error) (outcome ImmediateOutcome, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return outcome, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return outcome, err
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		if _, rollbackErr := conn.ExecContext(context.Background(), `ROLLBACK`); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("store: rollback immediate transaction: %w", rollbackErr))
			if discardErr := conn.Raw(func(any) error { return driver.ErrBadConn }); discardErr != nil && !errors.Is(discardErr, driver.ErrBadConn) {
				err = errors.Join(err, fmt.Errorf("store: discard connection after rollback failure: %w", discardErr))
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
