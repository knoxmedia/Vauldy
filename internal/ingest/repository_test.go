package ingest

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"knox-media/internal/store"

	"modernc.org/sqlite"
)

func repositoryDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ingest.db")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'test','movie','/library')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}
func fp(ch byte) Fingerprint {
	return Fingerprint{SHA256: string(make([]byte, 0)) + repeatHex(ch), Size: 10, ModTimeNS: 20}
}
func repeatHex(ch byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = ch
	}
	return string(b)
}
func input(key, pathKey string, f Fingerprint) EnqueueInput {
	return EnqueueInput{SubmissionKey: key, Candidate: Candidate{Source: SourceUpload, LibraryID: 1, Path: "/library/a.mp4", PathKey: pathKey, UploadID: "u"}, Fingerprint: f}
}

func TestRepositoryEnqueueReplayConvergenceAndSupersede(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db)
	first, err := r.Enqueue(context.Background(), input("one", "path", fp('a')))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.Enqueue(context.Background(), input("one", "path", fp('a')))
	if err != nil || replay.ItemID != first.ItemID || !replay.Duplicate {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	converged, err := r.Enqueue(context.Background(), input("two", "path", fp('a')))
	if err != nil || converged.ItemID != first.ItemID || !converged.Duplicate {
		t.Fatalf("converged=%+v err=%v", converged, err)
	}
	replacement, err := r.Enqueue(context.Background(), input("three", "path", fp('b')))
	if err != nil || replacement.ItemID == first.ItemID || replacement.SupersededItemID != first.ItemID {
		t.Fatalf("replacement=%+v err=%v", replacement, err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM ingest_item WHERE id=?`, first.ItemID).Scan(&state); err != nil || state != "superseded" {
		t.Fatalf("state=%q err=%v", state, err)
	}
}

func TestRepositoryRunningSupersessionPreservesHistoricalLeaseAndFencesOwner(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db, WithLeaseDuration(time.Minute))
	old, _ := r.Enqueue(context.Background(), input("old", "path", fp('a')))
	lease, err := r.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := r.Enqueue(context.Background(), input("new", "path", fp('b')))
	if err != nil {
		t.Fatal(err)
	}
	var state string
	var owner sql.NullString
	var until sql.NullTime
	if err := db.QueryRow(`SELECT state,superseded_owner,superseded_lease_until FROM ingest_item WHERE id=?`, old.ItemID).Scan(&state, &owner, &until); err != nil {
		t.Fatal(err)
	}
	if state != "superseded" || owner.String != lease.Owner || !until.Valid {
		t.Fatalf("evidence %q %q %v", state, owner.String, until.Valid)
	}
	if _, err = r.Renew(context.Background(), *lease); !IsLeaseLost(err) {
		t.Fatalf("renew err=%v", err)
	}
	if err = r.Complete(context.Background(), *lease); !IsLeaseLost(err) {
		t.Fatalf("complete err=%v", err)
	}
	if replacement.SupersededItemID != old.ItemID {
		t.Fatalf("replacement=%+v", replacement)
	}
}

func TestRepositoryConcurrentClaimExactlyOnceAndDefaultLease(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db)
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	const n = 12
	var wg sync.WaitGroup
	got := make(chan *Lease, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, _ := r.Claim(context.Background())
			if lease != nil {
				got <- lease
			}
		}()
	}
	wg.Wait()
	close(got)
	var leases []*Lease
	for l := range got {
		leases = append(leases, l)
	}
	if len(leases) != 1 {
		t.Fatalf("claims=%d", len(leases))
	}
	if leases[0].Owner == "" || time.Until(leases[0].LeaseUntil) < DefaultLeaseDuration-time.Second {
		t.Fatalf("lease=%+v", leases[0])
	}
}

func TestRepositoryRenewAndAllFencePredicates(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db, WithLeaseDuration(2*time.Minute))
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	before := lease.LeaseUntil
	time.Sleep(time.Millisecond)
	renewed, err := r.Renew(context.Background(), *lease)
	if err != nil || !renewed.After(before) {
		t.Fatalf("renewed=%v err=%v", renewed, err)
	}
	mutations := []func(*Lease){func(l *Lease) { l.ID++ }, func(l *Lease) { l.Owner = "wrong" }, func(l *Lease) { l.PathKey = "wrong" }, func(l *Lease) { l.Fingerprint = fp('b') }, func(l *Lease) { l.RetryRound++ }}
	for i, mutate := range mutations {
		bad := *lease
		mutate(&bad)
		if err := r.Complete(context.Background(), bad); !IsLeaseLost(err) {
			t.Fatalf("predicate %d err=%v", i, err)
		}
	}
}

func TestRepositoryExpiredRecoveryRetryAndPermanentFailure(t *testing.T) {
	db, _ := repositoryDB(t)
	now := time.Now().UTC()
	r := NewRepository(db, WithClock(func() time.Time { return now }), WithLeaseDuration(time.Second), WithRetryBackoff(func(int) time.Duration { return 5 * time.Minute }))
	r.Enqueue(context.Background(), input("recover", "p1", fp('a')))
	lease, _ := r.Claim(context.Background())
	now = now.Add(2 * time.Second)
	n, err := r.RecoverExpired(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("recover=%d err=%v", n, err)
	}
	now = now.Add(5 * time.Minute)
	again, _ := r.Claim(context.Background())
	if again == nil || again.Owner == lease.Owner {
		t.Fatalf("again=%+v", again)
	}
	if err := r.Fail(context.Background(), *again, errors.New("temporary"), true); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempts int
	var available time.Time
	db.QueryRow(`SELECT state,attempts,available_at FROM ingest_item WHERE id=?`, again.ID).Scan(&state, &attempts, &available)
	if state != "waiting" || attempts != 2 || !available.After(now) {
		t.Fatalf("retry %q %d %v", state, attempts, available)
	}
	r.Enqueue(context.Background(), input("permanent", "p2", fp('b')))
	permanent, _ := r.Claim(context.Background())
	if err := r.Fail(context.Background(), *permanent, errors.New("bad"), false); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT state FROM ingest_item WHERE id=?`, permanent.ID).Scan(&state)
	if state != "failed" {
		t.Fatalf("state=%s", state)
	}
}

func TestRepositoryPersistsWaitingAcrossReopen(t *testing.T) {
	db, path := repositoryDB(t)
	r := NewRepository(db)
	item, _ := r.Enqueue(context.Background(), input("one", "path", fp('a')))
	db.Close()
	reopened, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	lease, err := NewRepository(reopened).Claim(context.Background())
	if err != nil || lease == nil || lease.ID != item.ItemID {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
}

func TestRepositoryLeaseLossPreventsPublication(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db)
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	lease.Owner = "stale"
	called := false
	err := r.Finalize(context.Background(), *lease, func(store.ImmediateConnTx) error { called = true; return nil })
	if !IsLeaseLost(err) || called {
		t.Fatalf("err=%v publication-called=%v", err, called)
	}
}

func TestRepositoryAmbiguousCommitConfirmsDurableEnqueue(t *testing.T) {
	db, _ := repositoryDB(t)
	base := store.WithImmediateConnTxBeginTimeout
	injected := false
	r := NewRepository(db, withImmediateRunner(func(ctx context.Context, db *sql.DB, timeout time.Duration, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		out, err := base(ctx, db, timeout, fn)
		if err == nil && !injected {
			injected = true
			return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("lost acknowledgement")}
		}
		return out, err
	}))
	got, err := r.Enqueue(context.Background(), input("ambiguous", "path", fp('a')))
	if err != nil || got.ItemID == 0 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ingest_item WHERE submission_key='ambiguous'`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows=%d", n)
	}
}

func TestRepositoryBusyBeginRetriesAreBounded(t *testing.T) {
	db, _ := repositoryDB(t)
	attempts := 0
	r := NewRepository(db, WithClock(func() time.Time { return time.Now() }), withImmediateRunner(func(context.Context, *sql.DB, time.Duration, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		attempts++
		return store.ImmediateOutcome{}, &store.ImmediateBeginRetryError{Cause: errors.New("busy")}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := r.Enqueue(ctx, input("busy", "path", fp('a')))
	if !errors.Is(err, context.DeadlineExceeded) || attempts < 2 || attempts > 10 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestRepositoryDoesNotRunExternalIdentityWork(t *testing.T) {
	// Repository accepts already-computed immutable identity only. Its API has no
	// hash/probe/stability callback that could accidentally run under SQL locks.
	var _ = EnqueueInput{Fingerprint: fp('a')}
}

func ambiguousAfterCommitOnce() immediateRunner {
	base := store.WithImmediateConnTxBeginTimeout
	used := false
	return func(ctx context.Context, db *sql.DB, d time.Duration, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		out, err := base(ctx, db, d, fn)
		if err == nil && !used {
			used = true
			return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("lost acknowledgement")}
		}
		return out, err
	}
}

func TestRepositoryAmbiguousClaimDoesNotDoubleAttemptsOrBackoff(t *testing.T) {
	now := time.Now().UTC()
	normalDB, _ := repositoryDB(t)
	ambiguousDB, _ := repositoryDB(t)
	backoff := func(attempt int) time.Duration { return time.Duration(attempt) * time.Minute }
	normal := NewRepository(normalDB, WithClock(func() time.Time { return now }), WithRetryBackoff(backoff))
	ambiguous := NewRepository(ambiguousDB, WithClock(func() time.Time { return now }), WithRetryBackoff(backoff))
	normal.Enqueue(context.Background(), input("normal", "path", fp('a')))
	ambiguous.Enqueue(context.Background(), input("ambiguous", "path", fp('a')))
	nl, _ := normal.Claim(context.Background())
	al, err := ambiguous.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if al.Attempts != nl.Attempts {
		t.Fatalf("ambiguous attempts=%d normal=%d", al.Attempts, nl.Attempts)
	}
	if err = normal.Fail(context.Background(), *nl, errors.New("retry"), true); err != nil {
		t.Fatal(err)
	}
	if err = ambiguous.Fail(context.Background(), *al, errors.New("retry"), true); err != nil {
		t.Fatal(err)
	}
	var na, aa time.Time
	normalDB.QueryRow(`SELECT available_at FROM ingest_item WHERE id=?`, nl.ID).Scan(&na)
	ambiguousDB.QueryRow(`SELECT available_at FROM ingest_item WHERE id=?`, al.ID).Scan(&aa)
	if !na.Equal(aa) {
		t.Fatalf("backoff normal=%v ambiguous=%v", na, aa)
	}
}

func TestRepositoryAmbiguousFinalizeConfirmsOwnTokenAndPublication(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db)
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	r.immediate = ambiguousAfterCommitOnce()
	err := r.Finalize(context.Background(), *lease, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(context.Background(), `UPDATE ingest_item SET last_error='publication-marker' WHERE id=?`, lease.ID)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	var state, marker, token string
	if err = db.QueryRow(`SELECT state,last_error,transition_token FROM ingest_item WHERE id=?`, lease.ID).Scan(&state, &marker, &token); err != nil {
		t.Fatal(err)
	}
	if state != "done" || marker != "publication-marker" || len(token) != 32 {
		t.Fatalf("state=%q marker=%q token=%q", state, marker, token)
	}
}

func TestRepositoryAmbiguousMismatchIsNotSuccess(t *testing.T) {
	db, _ := repositoryDB(t)
	now := time.Now().UTC()
	r := NewRepository(db, WithClock(func() time.Time { return now }), WithLeaseDuration(time.Second))
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	now = now.Add(2 * time.Second)
	r.immediate = func(ctx context.Context, db *sql.DB, d time.Duration, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		// Simulate an uncertain rollback followed by another operation reaching waiting.
		if _, err := db.ExecContext(ctx, `UPDATE ingest_item SET state='waiting',lease_owner=NULL,lease_until=NULL,available_at=?,transition_token='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE id=?`, now, lease.ID); err != nil {
			return store.ImmediateOutcome{}, err
		}
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("rolled back")}
	}
	err := r.Fail(context.Background(), *lease, errors.New("retry"), true)
	if err == nil || (!IsLeaseLost(err) && !IsAmbiguousNotConfirmed(err)) {
		t.Fatalf("err=%v", err)
	}
}

func TestRepositoryAmbiguousRenewConfirmsExactToken(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db, WithLeaseDuration(time.Minute))
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	r.immediate = ambiguousAfterCommitOnce()
	until, err := r.Renew(context.Background(), *lease)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	var stored time.Time
	if err = db.QueryRow(`SELECT transition_token,lease_until FROM ingest_item WHERE id=?`, lease.ID).Scan(&token, &stored); err != nil {
		t.Fatal(err)
	}
	if len(token) != 32 || !stored.Equal(until) {
		t.Fatalf("token=%q stored=%v until=%v", token, stored, until)
	}
}

func TestRepositoryAmbiguousFailConfirmsExactEffects(t *testing.T) {
	db, _ := repositoryDB(t)
	now := time.Now().UTC()
	r := NewRepository(db, WithClock(func() time.Time { return now }), WithRetryBackoff(func(int) time.Duration { return 7 * time.Minute }))
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	r.immediate = ambiguousAfterCommitOnce()
	if err := r.Fail(context.Background(), *lease, errors.New("retry-exact"), true); err != nil {
		t.Fatal(err)
	}
	var state, last, token string
	var available time.Time
	var attempts, round int
	if err := db.QueryRow(`SELECT state,last_error,transition_token,available_at,attempts,retry_round FROM ingest_item WHERE id=?`, lease.ID).Scan(&state, &last, &token, &available, &attempts, &round); err != nil {
		t.Fatal(err)
	}
	if state != "waiting" || last != "retry-exact" || len(token) != 32 || !available.Equal(now.Add(7*time.Minute)) || attempts != lease.Attempts || round != lease.RetryRound {
		t.Fatalf("effects %q %q %q %v %d %d", state, last, token, available, attempts, round)
	}
}

func TestRepositorySubmissionKeyIdentityConflict(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db)
	original := input("same-key", "path", fp('a'))
	first, err := r.Enqueue(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := r.Enqueue(context.Background(), original)
	if err != nil || exact.ItemID != first.ItemID || !exact.Duplicate {
		t.Fatalf("exact=%+v err=%v", exact, err)
	}
	cases := []EnqueueInput{input("same-key", "other", fp('a')), input("same-key", "path", fp('b'))}
	cases = append(cases, original)
	cases[2].Candidate.LibraryID = 2
	for i, in := range cases {
		_, err = r.Enqueue(context.Background(), in)
		if !IsIdempotencyConflict(err) {
			t.Fatalf("case %d err=%v", i, err)
		}
	}
}

func TestRepositoryFinalizeCallbackErrorRollsBack(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db)
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	want := errors.New("planner failed")
	err := r.Finalize(context.Background(), *lease, func(tx store.ImmediateConnTx) error {
		_, e := tx.ExecContext(context.Background(), `UPDATE ingest_item SET last_error='must-rollback' WHERE id=?`, lease.ID)
		if e != nil {
			return e
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	var state, last string
	db.QueryRow(`SELECT state,last_error FROM ingest_item WHERE id=?`, lease.ID).Scan(&state, &last)
	if state != "running" || last != "" {
		t.Fatalf("state=%q last=%q", state, last)
	}
}

func TestRepositoryBodyNeverRetriedAfterBegin(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db)
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	calls := 0
	r.immediate = func(ctx context.Context, db *sql.DB, d time.Duration, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		calls++
		out, err := store.WithImmediateConnTxBeginTimeout(ctx, db, d, fn)
		if err == nil {
			return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("uncertain")}
		}
		return out, err
	}
	if err := r.Finalize(context.Background(), *lease, func(store.ImmediateConnTx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("body attempts=%d", calls)
	}
}

func TestRepositoryMaxAttemptsBoundary(t *testing.T) {
	db, _ := repositoryDB(t)
	now := time.Now().UTC()
	r := NewRepository(db, WithClock(func() time.Time { return now }), WithMaxAttempts(2))
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	first, _ := r.Claim(context.Background())
	if err := r.Fail(context.Background(), *first, errors.New("retry1"), true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	second, _ := r.Claim(context.Background())
	if second == nil || second.Attempts != 2 {
		t.Fatalf("second=%+v", second)
	}
	if err := r.Fail(context.Background(), *second, errors.New("retry2"), true); err != nil {
		t.Fatal(err)
	}
	var state string
	var round int
	db.QueryRow(`SELECT state,retry_round FROM ingest_item WHERE id=?`, second.ID).Scan(&state, &round)
	if state != "failed" || round != 0 {
		t.Fatalf("state=%q round=%d", state, round)
	}
}

func TestRepositoryRecoverExpiredStopsAtMaxAttempts(t *testing.T) {
	db, _ := repositoryDB(t)
	now := time.Now().UTC()
	r := NewRepository(db, WithClock(func() time.Time { return now }), WithLeaseDuration(time.Second), WithMaxAttempts(2), WithRetryBackoff(func(int) time.Duration { return time.Minute }))
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	first, _ := r.Claim(context.Background())
	now = now.Add(2 * time.Second)
	n, err := r.RecoverExpired(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("first recovery=%d err=%v", n, err)
	}
	if staleErr := r.Complete(context.Background(), *first); !IsLeaseLost(staleErr) {
		t.Fatalf("stale err=%v", staleErr)
	}
	if lease, _ := r.Claim(context.Background()); lease != nil {
		t.Fatal("claimed before recovery backoff")
	}
	now = now.Add(time.Minute)
	second, _ := r.Claim(context.Background())
	if second == nil || second.Attempts != 2 {
		t.Fatalf("second=%+v", second)
	}
	now = now.Add(2 * time.Second)
	n, err = r.RecoverExpired(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("second recovery=%d err=%v", n, err)
	}
	var state, last string
	var attempts, round int
	var owner sql.NullString
	if err = db.QueryRow(`SELECT state,last_error,attempts,retry_round,lease_owner FROM ingest_item WHERE id=?`, second.ID).Scan(&state, &last, &attempts, &round, &owner); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || attempts != 2 || round != 0 || owner.Valid || last == "" {
		t.Fatalf("state=%q last=%q attempts=%d round=%d owner=%v", state, last, attempts, round, owner)
	}
	if lease, _ := r.Claim(context.Background()); lease != nil {
		t.Fatalf("claim beyond max=%+v", lease)
	}
}

func TestRepositoryRecoverExpiredMixedSubsetsAmbiguous(t *testing.T) {
	db, _ := repositoryDB(t)
	now := time.Now().UTC()
	r := NewRepository(db, WithClock(func() time.Time { return now }), WithLeaseDuration(time.Second), WithMaxAttempts(2), WithRetryBackoff(func(int) time.Duration { return 5 * time.Minute }))
	r.Enqueue(context.Background(), input("below", "p1", fp('a')))
	below, _ := r.Claim(context.Background())
	r.Enqueue(context.Background(), input("at", "p2", fp('b')))
	at, _ := r.Claim(context.Background())
	if _, err := db.Exec(`UPDATE ingest_item SET attempts=2 WHERE id=?`, at.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	r.immediate = ambiguousAfterCommitOnce()
	n, err := r.RecoverExpired(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var waiting, failed int
	var tokens int
	if err = db.QueryRow(`SELECT SUM(state='waiting'),SUM(state='failed'),COUNT(DISTINCT transition_token) FROM ingest_item WHERE id IN (?,?)`, below.ID, at.ID).Scan(&waiting, &failed, &tokens); err != nil {
		t.Fatal(err)
	}
	if waiting != 1 || failed != 1 || tokens != 1 {
		t.Fatalf("waiting=%d failed=%d tokens=%d", waiting, failed, tokens)
	}
}

func TestRepositoryRecoverExpiredAmbiguousOtherActorCannotConfirm(t *testing.T) {
	db, _ := repositoryDB(t)
	now := time.Now().UTC()
	r := NewRepository(db, WithClock(func() time.Time { return now }), WithLeaseDuration(time.Second))
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	now = now.Add(2 * time.Second)
	r.immediate = func(ctx context.Context, db *sql.DB, d time.Duration, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		_, err := db.ExecContext(ctx, `UPDATE ingest_item SET state='waiting',lease_owner=NULL,lease_until=NULL,available_at=?,transition_token='cccccccccccccccccccccccccccccccc' WHERE id=?`, now, lease.ID)
		if err != nil {
			return store.ImmediateOutcome{}, err
		}
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("rolled back")}
	}
	if _, err := r.RecoverExpired(context.Background()); !IsAmbiguousNotConfirmed(err) {
		t.Fatalf("err=%v", err)
	}
}

func TestRepositoryPersistPlanBusyInvokedOnce(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db)
	r.Enqueue(context.Background(), input("one", "path", fp('a')))
	lease, _ := r.Claim(context.Background())
	calls := 0
	busy := &sqlite.Error{}
	err := r.Finalize(context.Background(), *lease, func(store.ImmediateConnTx) error { calls++; return busy })
	if err == nil {
		t.Fatal("expected busy body failure")
	}
	if calls != 1 {
		t.Fatalf("persist calls=%d", calls)
	}
}

func TestRepositoryBeginBusyRetriesBeforeBody(t *testing.T) {
	db, _ := repositoryDB(t)
	base := store.WithImmediateConnTxBeginTimeout
	attempts, bodies := 0, 0
	r := NewRepository(db, withImmediateRunner(func(ctx context.Context, db *sql.DB, d time.Duration, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		attempts++
		if attempts < 3 {
			return store.ImmediateOutcome{}, &store.ImmediateBeginRetryError{Cause: errors.New("busy begin")}
		}
		return base(ctx, db, d, func(tx store.ImmediateConnTx) error { bodies++; return fn(tx) })
	}))
	_, err := r.Enqueue(context.Background(), input("one", "path", fp('a')))
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || bodies != 1 {
		t.Fatalf("attempts=%d bodies=%d", attempts, bodies)
	}
}

func TestRepositoryCanceledCommitConfirmsDetached(t *testing.T) {
	db, _ := repositoryDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	base := store.WithImmediateConnTxBeginTimeout
	r := NewRepository(db, withImmediateRunner(func(callCtx context.Context, db *sql.DB, d time.Duration, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		out, err := base(callCtx, db, d, fn)
		if err == nil {
			cancel()
			return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: context.Canceled}
		}
		return out, err
	}))
	got, err := r.Enqueue(ctx, input("detached", "path", fp('a')))
	if err != nil || got.ItemID == 0 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestRepositoryCanceledRollbackNotConfirmed(t *testing.T) {
	db, _ := repositoryDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	r := NewRepository(db, withImmediateRunner(func(context.Context, *sql.DB, time.Duration, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		cancel()
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: context.Canceled}
	}))
	_, err := r.Enqueue(ctx, input("rolledback", "path", fp('a')))
	if !IsAmbiguousNotConfirmed(err) {
		t.Fatalf("err=%v", err)
	}
}

func TestRepositoryConfirmationTimeoutIsJoined(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db, WithConfirmTimeout(20*time.Millisecond), withImmediateRunner(func(context.Context, *sql.DB, time.Duration, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("uncertain")}
	}))
	r.confirmHook = func(ctx context.Context) { <-ctx.Done() }
	_, err := r.Enqueue(context.Background(), input("timeout", "path", fp('a')))
	if !IsAmbiguousNotConfirmed(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestRepositoryClaimTerminalizesAnomalousExhaustedWaiting(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db, WithMaxAttempts(2))
	exhausted, _ := r.Enqueue(context.Background(), input("exhausted", "p1", fp('a')))
	eligible, _ := r.Enqueue(context.Background(), input("eligible", "p2", fp('b')))
	if _, err := db.Exec(`UPDATE ingest_item SET attempts=2 WHERE id=?`, exhausted.ItemID); err != nil {
		t.Fatal(err)
	}
	lease, err := r.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil || lease.ID != eligible.ItemID || lease.Attempts != 1 {
		t.Fatalf("lease=%+v", lease)
	}
	var state, last, token string
	var attempts, round int
	if err = db.QueryRow(`SELECT state,last_error,transition_token,attempts,retry_round FROM ingest_item WHERE id=?`, exhausted.ItemID).Scan(&state, &last, &token, &attempts, &round); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || last == "" || len(token) != 32 || attempts != 2 || round != 0 {
		t.Fatalf("state=%q last=%q token=%q attempts=%d round=%d", state, last, token, attempts, round)
	}
}

func TestRepositoryConcurrentClaimNeverExceedsCeiling(t *testing.T) {
	db, _ := repositoryDB(t)
	r := NewRepository(db, WithMaxAttempts(2))
	item, _ := r.Enqueue(context.Background(), input("one", "path", fp('a')))
	if _, err := db.Exec(`UPDATE ingest_item SET attempts=1 WHERE id=?`, item.ItemID); err != nil {
		t.Fatal(err)
	}
	const n = 12
	var wg sync.WaitGroup
	leases := make(chan *Lease, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, _ := r.Claim(context.Background())
			if l != nil {
				leases <- l
			}
		}()
	}
	wg.Wait()
	close(leases)
	count := 0
	for l := range leases {
		count++
		if l.Attempts != 2 {
			t.Fatalf("attempts=%d", l.Attempts)
		}
	}
	if count != 1 {
		t.Fatalf("claims=%d", count)
	}
	var attempts int
	db.QueryRow(`SELECT attempts FROM ingest_item WHERE id=?`, item.ItemID).Scan(&attempts)
	if attempts != 2 {
		t.Fatalf("durable attempts=%d", attempts)
	}
}
