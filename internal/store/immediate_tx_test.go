package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
