package ingest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"knox-media/internal/store"
)

const DefaultLeaseDuration = 5 * time.Minute

var ErrLeaseLost = errors.New("ingest lease lost")
var ErrAmbiguousNotConfirmed = errors.New("ingest ambiguous commit not confirmed")
var ErrIdempotencyConflict = errors.New("ingest idempotency conflict")

type LeaseLostError struct{ ItemID int64 }

func (e *LeaseLostError) Error() string { return fmt.Sprintf("%v: item %d", ErrLeaseLost, e.ItemID) }
func (e *LeaseLostError) Unwrap() error { return ErrLeaseLost }
func IsLeaseLost(err error) bool        { return errors.Is(err, ErrLeaseLost) }

type AmbiguousNotConfirmedError struct{ Cause error }

func (e *AmbiguousNotConfirmedError) Error() string {
	return fmt.Sprintf("%v: %v", ErrAmbiguousNotConfirmed, e.Cause)
}
func (e *AmbiguousNotConfirmedError) Unwrap() error { return e.Cause }
func IsAmbiguousNotConfirmed(err error) bool {
	var target *AmbiguousNotConfirmedError
	return errors.As(err, &target)
}

type EnqueueInput struct {
	SubmissionKey string
	Candidate     Candidate
	Fingerprint   Fingerprint
}
type EnqueueResult struct {
	ItemID           int64
	Duplicate        bool
	SupersededItemID int64
}
type Lease struct {
	ID          int64
	Owner       string
	LeaseUntil  time.Time
	PathKey     string
	Fingerprint Fingerprint
	RetryRound  int
	Attempts    int
}

type repositoryOption func(*Repository)
type immediateRunner func(context.Context, *sql.DB, time.Duration, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error)
type Repository struct {
	db                          *sql.DB
	now                         func() time.Time
	leaseDuration, beginTimeout time.Duration
	retryBackoff                func(int) time.Duration
	maxAttempts                 int
	confirmTimeout              time.Duration
	confirmHook                 func(context.Context)
	immediate                   immediateRunner
}

func NewRepository(db *sql.DB, options ...repositoryOption) *Repository {
	r := &Repository{db: db, now: func() time.Time { return time.Now().UTC() }, leaseDuration: DefaultLeaseDuration, beginTimeout: 750 * time.Millisecond, retryBackoff: func(attempt int) time.Duration {
		if attempt < 1 {
			attempt = 1
		}
		return time.Second * time.Duration(1<<min(attempt-1, 8))
	}, maxAttempts: 3, confirmTimeout: time.Second, immediate: store.WithImmediateConnTxBeginTimeout}
	for _, o := range options {
		o(r)
	}
	return r
}
func WithLeaseDuration(d time.Duration) repositoryOption {
	return func(r *Repository) { r.leaseDuration = d }
}
func WithClock(f func() time.Time) repositoryOption { return func(r *Repository) { r.now = f } }
func WithRetryBackoff(f func(int) time.Duration) repositoryOption {
	return func(r *Repository) { r.retryBackoff = f }
}
func WithMaxAttempts(n int) repositoryOption {
	return func(r *Repository) {
		if n > 0 {
			r.maxAttempts = n
		}
	}
}

type IdempotencyConflictError struct{ SubmissionKey string }

func (e *IdempotencyConflictError) Error() string {
	return fmt.Sprintf("%v: submission key %q has different immutable identity", ErrIdempotencyConflict, e.SubmissionKey)
}
func (e *IdempotencyConflictError) Unwrap() error { return ErrIdempotencyConflict }
func IsIdempotencyConflict(err error) bool        { return errors.Is(err, ErrIdempotencyConflict) }
func WithConfirmTimeout(d time.Duration) repositoryOption {
	return func(r *Repository) {
		if d > 0 {
			r.confirmTimeout = d
		}
	}
}
func withImmediateRunner(f immediateRunner) repositoryOption {
	return func(r *Repository) { r.immediate = f }
}

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (r *Repository) transact(ctx context.Context, body func(store.ImmediateConnTx) error, confirm func(context.Context) (bool, error)) error {
	deadline := time.Now().Add(2 * time.Second)
	delay := 10 * time.Millisecond
	for {
		out, err := r.immediate(ctx, r.db, r.beginTimeout, body)
		if err == nil {
			return nil
		}
		var uncertain *store.ImmediateCommitError
		if errors.As(err, &uncertain) || out.CommitAttempted {
			if confirm == nil {
				return &AmbiguousNotConfirmedError{Cause: err}
			}
			confirmCtx, cancelConfirm := context.WithTimeout(context.WithoutCancel(ctx), r.confirmTimeout)
			if r.confirmHook != nil {
				r.confirmHook(confirmCtx)
			}
			if confirmErr := confirmCtx.Err(); confirmErr != nil {
				cancelConfirm()
				return errors.Join(&AmbiguousNotConfirmedError{Cause: err}, confirmErr)
			}
			ok, qerr := confirm(confirmCtx)
			cancelConfirm()
			if qerr != nil {
				return errors.Join(&AmbiguousNotConfirmedError{Cause: err}, qerr)
			}
			if ok {
				return nil
			}
			return &AmbiguousNotConfirmedError{Cause: err}
		}
		if out.BodyStarted || !store.IsImmediateBeginRetry(err) {
			return err
		}
		if !time.Now().Before(deadline) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 100*time.Millisecond {
			delay *= 2
		}
	}
}

func (r *Repository) Enqueue(ctx context.Context, in EnqueueInput) (result EnqueueResult, err error) {
	if err = validateEnqueue(in); err != nil {
		return result, err
	}
	token, err := randomToken()
	if err != nil {
		return result, err
	}
	now := r.now()
	var superseded int64
	wrote := false
	bodyStarted := false
	body := func(tx store.ImmediateConnTx) error {
		bodyStarted = true
		result = EnqueueResult{}
		superseded = 0
		wrote = false
		var id int64
		var existingSource, existingPath, existingPathKey string
		var existingLibrary int64
		var existingUpload, existingSHA sql.NullString
		var existingSize, existingMTime sql.NullInt64
		e := tx.QueryRowContext(ctx, `SELECT id,source,library_id,canonical_path,path_key,upload_id,sha256,size_bytes,mtime_ns FROM ingest_item WHERE submission_key=?`, in.SubmissionKey).Scan(&id, &existingSource, &existingLibrary, &existingPath, &existingPathKey, &existingUpload, &existingSHA, &existingSize, &existingMTime)
		if e == nil {
			exact := existingSource == string(in.Candidate.Source) && existingLibrary == in.Candidate.LibraryID && existingPath == in.Candidate.Path && existingPathKey == in.Candidate.PathKey && existingUpload.String == in.Candidate.UploadID && existingUpload.Valid == (in.Candidate.UploadID != "") && existingSHA.Valid && existingSHA.String == in.Fingerprint.SHA256 && existingSize.Valid && existingSize.Int64 == in.Fingerprint.Size && existingMTime.Valid && existingMTime.Int64 == in.Fingerprint.ModTimeNS
			if !exact {
				return &IdempotencyConflictError{SubmissionKey: in.SubmissionKey}
			}
			result = EnqueueResult{ItemID: id, Duplicate: true}
			return nil
		} else if e != sql.ErrNoRows {
			return e
		}
		var state string
		var sha sql.NullString
		var size, mtime sql.NullInt64
		e = tx.QueryRowContext(ctx, `SELECT id,state,sha256,size_bytes,mtime_ns FROM ingest_item WHERE library_id=? AND path_key=? AND state IN ('waiting','running')`, in.Candidate.LibraryID, in.Candidate.PathKey).Scan(&id, &state, &sha, &size, &mtime)
		if e == nil {
			if sha.Valid && size.Valid && mtime.Valid && sha.String == in.Fingerprint.SHA256 && size.Int64 == in.Fingerprint.Size && mtime.Int64 == in.Fingerprint.ModTimeNS {
				result = EnqueueResult{ItemID: id, Duplicate: true}
				return nil
			}
			res, e := tx.ExecContext(ctx, `UPDATE ingest_item SET state='superseded',superseded_owner=lease_owner,superseded_lease_until=lease_until,lease_owner=NULL,lease_until=NULL,finished_at=?,updated_at=?,transition_token=? WHERE id=? AND state=?`, now, now, token, id, state)
			if e != nil {
				return e
			}
			n, rowsErr := res.RowsAffected()
			if rowsErr != nil {
				return rowsErr
			}
			if n != 1 {
				return fmt.Errorf("ingest supersession race")
			}
			superseded = id
		} else if e != sql.ErrNoRows {
			return e
		}
		upload := sql.NullString{String: in.Candidate.UploadID, Valid: in.Candidate.UploadID != ""}
		res, e := tx.ExecContext(ctx, `INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,upload_id,size_bytes,mtime_ns,sha256,state,available_at,created_at,updated_at,transition_token) VALUES(?,?,?,?,?,?,?,?,?,'waiting',?,?,?,?)`, in.SubmissionKey, string(in.Candidate.Source), in.Candidate.LibraryID, in.Candidate.Path, in.Candidate.PathKey, upload, in.Fingerprint.Size, in.Fingerprint.ModTimeNS, in.Fingerprint.SHA256, now, now, now, token)
		if e != nil {
			return e
		}
		id, e = res.LastInsertId()
		if e != nil {
			return e
		}
		wrote = true
		result = EnqueueResult{ItemID: id, SupersededItemID: superseded}
		return nil
	}
	confirm := func(ctx context.Context) (bool, error) {
		if !bodyStarted {
			return false, nil
		}
		if !wrote {
			return true, nil
		}
		var id int64
		e := r.db.QueryRowContext(ctx, `SELECT id FROM ingest_item WHERE submission_key=? AND transition_token=? AND state='waiting' AND path_key=? AND sha256=? AND size_bytes=? AND mtime_ns=?`, in.SubmissionKey, token, in.Candidate.PathKey, in.Fingerprint.SHA256, in.Fingerprint.Size, in.Fingerprint.ModTimeNS).Scan(&id)
		if e == sql.ErrNoRows {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		result = EnqueueResult{ItemID: id, SupersededItemID: superseded}
		return true, nil
	}
	err = r.transact(ctx, body, confirm)
	return result, err
}
func validateEnqueue(in EnqueueInput) error {
	if strings.TrimSpace(in.SubmissionKey) == "" || in.Candidate.LibraryID <= 0 || strings.TrimSpace(in.Candidate.Path) == "" || strings.TrimSpace(in.Candidate.PathKey) == "" {
		return fmt.Errorf("invalid ingest enqueue identity")
	}
	if in.Candidate.Source != SourceUpload && in.Candidate.Source != SourceFilesystemEvent {
		return fmt.Errorf("unsupported ingest source %q", in.Candidate.Source)
	}
	if len(in.Fingerprint.SHA256) != 64 || in.Fingerprint.Size < 0 {
		return fmt.Errorf("invalid ingest fingerprint")
	}
	for _, c := range in.Fingerprint.SHA256 {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("invalid ingest fingerprint")
		}
	}
	return nil
}

func (r *Repository) Claim(ctx context.Context) (lease *Lease, err error) {
	owner, err := randomToken()
	if err != nil {
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	now := r.now()
	until := now.Add(r.leaseDuration)
	body := func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `UPDATE ingest_item SET state='failed',last_error='ingest attempts exhausted before claim',finished_at=?,updated_at=?,transition_token=? WHERE state='waiting' AND available_at<=? AND attempts>=?`, now, now, token, now, r.maxAttempts)
		if e != nil {
			return e
		}
		if _, e = res.RowsAffected(); e != nil {
			return e
		}
		var l Lease
		e = tx.QueryRowContext(ctx, `SELECT id,path_key,sha256,size_bytes,mtime_ns,retry_round,attempts FROM ingest_item WHERE state='waiting' AND available_at<=? AND attempts<? ORDER BY available_at,id LIMIT 1`, now, r.maxAttempts).Scan(&l.ID, &l.PathKey, &l.Fingerprint.SHA256, &l.Fingerprint.Size, &l.Fingerprint.ModTimeNS, &l.RetryRound, &l.Attempts)
		if e == sql.ErrNoRows {
			lease = nil
			return nil
		}
		if e != nil {
			return e
		}
		res, e = tx.ExecContext(ctx, `UPDATE ingest_item SET state='running',attempts=attempts+1,lease_owner=?,lease_until=?,transition_token=?,started_at=COALESCE(started_at,?),updated_at=? WHERE id=? AND state='waiting' AND available_at<=? AND path_key=? AND sha256=? AND size_bytes=? AND mtime_ns=? AND retry_round=? AND attempts<?`, owner, until, token, now, now, l.ID, now, l.PathKey, l.Fingerprint.SHA256, l.Fingerprint.Size, l.Fingerprint.ModTimeNS, l.RetryRound, r.maxAttempts)
		if e != nil {
			return e
		}
		n, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if n != 1 {
			lease = nil
			return nil
		}
		l.Attempts++
		l.Owner = owner
		l.LeaseUntil = until
		lease = &l
		return nil
	}
	confirm := func(ctx context.Context) (bool, error) {
		var l Lease
		e := r.db.QueryRowContext(ctx, `SELECT id,path_key,sha256,size_bytes,mtime_ns,retry_round,attempts,lease_until FROM ingest_item WHERE state='running' AND lease_owner=? AND transition_token=?`, owner, token).Scan(&l.ID, &l.PathKey, &l.Fingerprint.SHA256, &l.Fingerprint.Size, &l.Fingerprint.ModTimeNS, &l.RetryRound, &l.Attempts, &l.LeaseUntil)
		if e == sql.ErrNoRows {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		l.Owner = owner
		lease = &l
		return true, nil
	}
	if err = r.transact(ctx, body, confirm); err != nil {
		return nil, err
	}
	return lease, nil
}

func (r *Repository) Renew(ctx context.Context, l Lease) (time.Time, error) {
	token, err := randomToken()
	if err != nil {
		return time.Time{}, err
	}
	now := r.now()
	until := now.Add(r.leaseDuration)
	query := `UPDATE ingest_item SET lease_until=?,updated_at=?,transition_token=? WHERE id=? AND state='running' AND lease_owner=? AND lease_until>? AND path_key=? AND sha256=? AND size_bytes=? AND mtime_ns=? AND retry_round=?`
	args := []any{until, now, token, l.ID, l.Owner, now, l.PathKey, l.Fingerprint.SHA256, l.Fingerprint.Size, l.Fingerprint.ModTimeNS, l.RetryRound}
	confirm := tokenConfirmation(r.db, l.ID, token, `state='running' AND lease_owner=? AND lease_until=? AND path_key=? AND sha256=? AND size_bytes=? AND mtime_ns=? AND retry_round=?`, l.Owner, until, l.PathKey, l.Fingerprint.SHA256, l.Fingerprint.Size, l.Fingerprint.ModTimeNS, l.RetryRound)
	return until, r.fencedTransition(ctx, l, query, args, confirm)
}

// PersistPlan is a DB-only persistence callback. It must use only the supplied
// transaction executor and perform deterministic transactional SQL. Callers must
// finish hashing, filesystem/network I/O, probing, stability waits, and external
// processes before Finalize. Re-entering the parent *sql.DB can deadlock and is
// outside this contract. Once invoked, PersistPlan is never retried.
type PersistPlan func(store.ImmediateConnTx) error

func (r *Repository) Complete(ctx context.Context, l Lease) error { return r.Finalize(ctx, l, nil) }
func (r *Repository) Finalize(ctx context.Context, l Lease, persist PersistPlan) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	now := r.now()
	body := func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, `UPDATE ingest_item SET updated_at=? WHERE id=? AND state='running' AND lease_owner=? AND lease_until>? AND path_key=? AND sha256=? AND size_bytes=? AND mtime_ns=? AND retry_round=?`, now, l.ID, l.Owner, now, l.PathKey, l.Fingerprint.SHA256, l.Fingerprint.Size, l.Fingerprint.ModTimeNS, l.RetryRound)
		if e != nil {
			return e
		}
		n, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if n != 1 {
			return &LeaseLostError{ItemID: l.ID}
		}
		if persist != nil {
			if e = persist(tx); e != nil {
				return e
			}
		}
		res, e = tx.ExecContext(ctx, `UPDATE ingest_item SET state='done',lease_owner=NULL,lease_until=NULL,finished_at=?,updated_at=?,transition_token=? WHERE id=? AND state='running' AND lease_owner=? AND lease_until>? AND path_key=? AND sha256=? AND size_bytes=? AND mtime_ns=? AND retry_round=?`, now, now, token, l.ID, l.Owner, now, l.PathKey, l.Fingerprint.SHA256, l.Fingerprint.Size, l.Fingerprint.ModTimeNS, l.RetryRound)
		if e != nil {
			return e
		}
		n, rowsErr = res.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if n != 1 {
			return &LeaseLostError{ItemID: l.ID}
		}
		return nil
	}
	return r.transact(ctx, body, tokenConfirmation(r.db, l.ID, token, `state='done' AND path_key=? AND sha256=? AND size_bytes=? AND mtime_ns=? AND retry_round=?`, l.PathKey, l.Fingerprint.SHA256, l.Fingerprint.Size, l.Fingerprint.ModTimeNS, l.RetryRound))
}
func (r *Repository) Fail(ctx context.Context, l Lease, cause error, retryable bool) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	now := r.now()
	state := "failed"
	available := now
	if retryable && l.Attempts < r.maxAttempts {
		state = "waiting"
		available = now.Add(r.retryBackoff(l.Attempts))
	}
	query := `UPDATE ingest_item SET state=?,available_at=?,lease_owner=NULL,lease_until=NULL,last_error=?,finished_at=CASE WHEN ?='failed' THEN ? ELSE NULL END,updated_at=?,transition_token=? WHERE id=? AND state='running' AND lease_owner=? AND lease_until>? AND path_key=? AND sha256=? AND size_bytes=? AND mtime_ns=? AND retry_round=?`
	args := []any{state, available, cause.Error(), state, now, now, token, l.ID, l.Owner, now, l.PathKey, l.Fingerprint.SHA256, l.Fingerprint.Size, l.Fingerprint.ModTimeNS, l.RetryRound}
	confirm := tokenConfirmation(r.db, l.ID, token, `state=? AND available_at=? AND attempts=? AND retry_round=? AND last_error=? AND path_key=? AND sha256=? AND size_bytes=? AND mtime_ns=?`, state, available, l.Attempts, l.RetryRound, cause.Error(), l.PathKey, l.Fingerprint.SHA256, l.Fingerprint.Size, l.Fingerprint.ModTimeNS)
	return r.fencedTransition(ctx, l, query, args, confirm)
}
func (r *Repository) fencedTransition(ctx context.Context, l Lease, query string, args []any, confirm func(context.Context) (bool, error)) error {
	body := func(tx store.ImmediateConnTx) error {
		res, e := tx.ExecContext(ctx, query, args...)
		if e != nil {
			return e
		}
		n, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if n != 1 {
			return &LeaseLostError{ItemID: l.ID}
		}
		return nil
	}
	return r.transact(ctx, body, confirm)
}
func tokenConfirmation(db *sql.DB, id int64, token, predicate string, args ...any) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		query := `SELECT 1 FROM ingest_item WHERE id=? AND transition_token=? AND ` + predicate
		all := append([]any{id, token}, args...)
		var one int
		e := db.QueryRowContext(ctx, query, all...).Scan(&one)
		if e == sql.ErrNoRows {
			return false, nil
		}
		return e == nil, e
	}
}

// RecoverExpired returns the total rows recovered, including both rows requeued
// for another automatic attempt and rows terminalized at the attempt ceiling.
func (r *Repository) RecoverExpired(ctx context.Context) (count int64, err error) {
	token, err := randomToken()
	if err != nil {
		return 0, err
	}
	now := r.now()
	waitingCount, failedCount := int64(0), int64(0)
	bodyRan := false
	body := func(tx store.ImmediateConnTx) error {
		bodyRan = true
		waitingCount, failedCount, count = 0, 0, 0
		rows, e := tx.QueryContext(ctx, `SELECT id,attempts FROM ingest_item WHERE state='running' AND lease_until<=? ORDER BY id`, now)
		if e != nil {
			return e
		}
		type expired struct {
			id       int64
			attempts int
		}
		var items []expired
		for rows.Next() {
			var item expired
			if e = rows.Scan(&item.id, &item.attempts); e != nil {
				rows.Close()
				return e
			}
			items = append(items, item)
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		if e = rows.Close(); e != nil {
			return e
		}
		for _, item := range items {
			var res sql.Result
			if item.attempts < r.maxAttempts {
				res, e = tx.ExecContext(ctx, `UPDATE ingest_item SET state='waiting',lease_owner=NULL,lease_until=NULL,available_at=?,last_error='ingest lease expired; retry scheduled',updated_at=?,transition_token=? WHERE id=? AND state='running' AND lease_until<=? AND attempts=?`, now.Add(r.retryBackoff(item.attempts)), now, token, item.id, now, item.attempts)
				waitingCount++
			} else {
				res, e = tx.ExecContext(ctx, `UPDATE ingest_item SET state='failed',lease_owner=NULL,lease_until=NULL,last_error='ingest lease expired; maximum attempts exhausted',finished_at=?,updated_at=?,transition_token=? WHERE id=? AND state='running' AND lease_until<=? AND attempts=?`, now, now, token, item.id, now, item.attempts)
				failedCount++
			}
			if e != nil {
				return e
			}
			affected, rowsErr := res.RowsAffected()
			if rowsErr != nil {
				return rowsErr
			}
			if affected != 1 {
				return &LeaseLostError{ItemID: item.id}
			}
		}
		count = waitingCount + failedCount
		return nil
	}
	confirm := func(ctx context.Context) (bool, error) {
		if !bodyRan {
			return false, nil
		}
		var waiting, failed int64
		e := r.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(state='waiting' AND last_error='ingest lease expired; retry scheduled'),0),COALESCE(SUM(state='failed' AND last_error='ingest lease expired; maximum attempts exhausted'),0) FROM ingest_item WHERE transition_token=? AND lease_owner IS NULL AND lease_until IS NULL`, token).Scan(&waiting, &failed)
		return e == nil && waiting == waitingCount && failed == failedCount && waiting+failed == count, e
	}
	err = r.transact(ctx, body, confirm)
	return
}
