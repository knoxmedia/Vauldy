package scancoord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/scanner"
	"knox-media/internal/store"
)

type blockingScanner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingScanner() *blockingScanner {
	return &blockingScanner{started: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingScanner) ScanLibraryFoldersWithContextAndCallbacks(ctx context.Context, _ int64, _ []string, _ scanner.ScanCallbacks) (int, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func openCoordinatorTestDB(t *testing.T, libraries int) (*sql.DB, []int64) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "coordinator.sqlite"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ids := make([]int64, 0, libraries)
	for i := 0; i < libraries; i++ {
		result, err := db.Exec(`INSERT INTO library (name, type, path) VALUES (?, 'video', ?)`, "library", filepath.Join(t.TempDir(), "root"))
		if err != nil {
			t.Fatalf("insert library: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("library id: %v", err)
		}
		ids = append(ids, id)
	}
	return db, ids
}

func newTestCoordinator(t *testing.T, db *sql.DB, owner string, scanner Scanner) *Coordinator {
	t.Helper()
	coordinator, err := New(db, Options{
		LeaseDuration:     time.Minute,
		HeartbeatInterval: 20 * time.Second,
		OwnerInstanceID:   owner,
		Scanner:           scanner,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return coordinator
}

func TestCoordinator_CompetesAcrossInstances(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	const submitters = 20
	coordinators := make([]*Coordinator, 0, submitters)
	for i := 0; i < submitters; i++ {
		scanner := newBlockingScanner()
		defer close(scanner.release)
		coordinators = append(coordinators, newTestCoordinator(t, db, fmt.Sprintf("instance-%d", i), scanner))
	}
	start := make(chan struct{})
	results := make(chan SubmitResult, submitters)
	errs := make(chan error, submitters)
	var ready sync.WaitGroup
	ready.Add(submitters)
	for _, coordinator := range coordinators {
		go func(c *Coordinator) {
			ready.Done()
			<-start
			result, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/root"}})
			results <- result
			errs <- err
		}(coordinator)
	}
	ready.Wait()
	close(start)

	started, winnerID := 0, int64(0)
	all := make([]SubmitResult, 0, submitters)
	for i := 0; i < submitters; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("Submit: %v", err)
		}
		result := <-results
		all = append(all, result)
		if result.Started {
			started++
			winnerID = result.TaskID
		}
	}
	if started != 1 || winnerID == 0 {
		t.Fatalf("started=%d winner=%d results=%+v", started, winnerID, all)
	}
	for _, result := range all {
		if !result.Started && (result.TaskID != 0 || result.ExistingTaskID != winnerID) {
			t.Fatalf("loser=%+v want no new task and existing=%d", result, winnerID)
		}
	}
	var taskCount, cancelledCount int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status='cancelled' THEN 1 ELSE 0 END),0) FROM scan_task WHERE library_id=?`, libraries[0]).Scan(&taskCount, &cancelledCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 || cancelledCount != 0 {
		t.Fatalf("tasks=%d cancelled=%d want 1,0", taskCount, cancelledCount)
	}
}

func TestCoordinatorActiveLeaseReturnsExistingWithoutCancelledTaskRow(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	old, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,started_at) VALUES(?,'running',?,CURRENT_TIMESTAMP)`, libraries[0], SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	oldTaskID, _ := old.LastInsertId()
	if _, err := db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraries[0], oldTaskID, "other/active"); err != nil {
		t.Fatal(err)
	}
	coordinator := newTestCoordinator(t, db, "new-instance", &countingScanner{})
	got, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceScheduled, Roots: []string{"/ignored"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Started || got.TaskID != 0 || got.ExistingTaskID != oldTaskID {
		t.Fatalf("Submit=%+v want existing task %d without insertion", got, oldTaskID)
	}
	var count, cancelled int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(cancelled),0) FROM scan_task WHERE library_id=?`, libraries[0]).Scan(&count, &cancelled); err != nil {
		t.Fatal(err)
	}
	if count != 1 || cancelled != 0 {
		t.Fatalf("count=%d cancelled=%d want 1,0", count, cancelled)
	}
}

func TestCoordinatorUnexpiredStaleLeaseStartsNewTask(t *testing.T) {
	for _, status := range []string{"done", "failed", "cancelled", "missing"} {
		t.Run(status, func(t *testing.T) {
			db, libraries := openCoordinatorTestDB(t, 1)
			staleTaskID := int64(999999)
			if status != "missing" {
				cancelled := 0
				if status == "cancelled" {
					cancelled = 1
				}
				res, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,cancelled) VALUES(?,?,?,?)`, libraries[0], status, SourceManual, cancelled)
				if err != nil {
					t.Fatal(err)
				}
				staleTaskID, _ = res.LastInsertId()
			}
			if status == "missing" {
				if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraries[0], staleTaskID, "stale/owner"); err != nil {
				t.Fatal(err)
			}
			coordinator := newTestCoordinator(t, db, "replacement", &countingScanner{})
			got, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceScheduled, Roots: []string{"/new"}})
			if err != nil || !got.Started || got.TaskID == staleTaskID {
				t.Fatalf("Submit=%+v err=%v stale=%d", got, err, staleTaskID)
			}
			var leaseTask int64
			if err := db.QueryRow(`SELECT scan_task_id FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&leaseTask); err != nil {
				t.Fatal(err)
			}
			if leaseTask != got.TaskID {
				t.Fatalf("lease task=%d want %d", leaseTask, got.TaskID)
			}
		})
	}
}

func TestCoordinatorCommitErrorAfterCommitConfirmsAndStartsScanner(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := newBlockingScanner()
	defer close(scanner.release)
	coordinator := newTestCoordinator(t, db, "commit-confirm", scanner)
	coordinator.withImmediateTx = func(ctx context.Context, db *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		outcome, err := store.WithImmediateConnTx(ctx, db, fn)
		if err != nil {
			return outcome, err
		}
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("injected error after actual commit")}
	}
	got, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/root"}})
	if err != nil || !got.Started || got.TaskID == 0 {
		t.Fatalf("Submit=%+v err=%v", got, err)
	}
	select {
	case <-scanner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("confirmed committed scan did not start")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_task WHERE library_id=?`, libraries[0]).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("tasks=%d want 1", count)
	}
}

func TestCoordinatorAmbiguousCommitConfirmationDoesNotRetryInsert(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	coordinator := newTestCoordinator(t, db, "commit-ambiguous", &countingScanner{})
	commitCalls := 0
	coordinator.withImmediateTx = func(ctx context.Context, db *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		commitCalls++
		conn, err := db.Conn(ctx)
		if err != nil {
			return store.ImmediateOutcome{}, err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			return store.ImmediateOutcome{}, err
		}
		if err := fn(conn); err != nil {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
			return store.ImmediateOutcome{}, err
		}
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("commit outcome unknown")}
	}
	coordinator.confirmSubmit = func(int64, int64, string) (time.Time, bool, error) {
		return time.Time{}, false, errors.New("database is locked (5)")
	}
	_, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/root"}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err=%v want ambiguous commit", err)
	}
	if commitCalls != 1 {
		t.Fatalf("commit calls=%d want no blind retry", commitCalls)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_task WHERE library_id=?`, libraries[0]).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("tasks=%d want rolled back candidate", count)
	}
}

func TestCoordinator_AllowsDifferentLibraries(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 2)
	scannerA, scannerB := newBlockingScanner(), newBlockingScanner()
	defer close(scannerA.release)
	defer close(scannerB.release)
	coordinatorA := newTestCoordinator(t, db, "instance-a", scannerA)
	coordinatorB := newTestCoordinator(t, db, "instance-b", scannerB)

	resultA, err := coordinatorA.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceScheduled, Roots: []string{"/a"}})
	if err != nil {
		t.Fatalf("Submit A: %v", err)
	}
	resultB, err := coordinatorB.Submit(context.Background(), ScanRequest{LibraryID: libraries[1], Source: SourceMonitor, Roots: []string{"/b"}})
	if err != nil {
		t.Fatalf("Submit B: %v", err)
	}
	if !resultA.Started || !resultB.Started {
		t.Fatalf("results = %+v, %+v; want both started", resultA, resultB)
	}
}

func TestCoordinator_TakesOverExpiredLease(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scannerA, scannerB := newBlockingScanner(), newBlockingScanner()
	defer close(scannerA.release)
	defer close(scannerB.release)
	coordinatorA := newTestCoordinator(t, db, "instance-a", scannerA)
	coordinatorB := newTestCoordinator(t, db, "instance-b", scannerB)

	resultA, err := coordinatorA.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil || !resultA.Started {
		t.Fatalf("Submit A = %+v, %v", resultA, err)
	}
	var ownerA string
	if err := db.QueryRow(`SELECT owner_id FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&ownerA); err != nil {
		t.Fatalf("read owner A: %v", err)
	}
	if _, err := db.Exec(`UPDATE scan_lease SET lease_until=datetime(CURRENT_TIMESTAMP, '-1 second') WHERE library_id=?`, libraries[0]); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	resultB, err := coordinatorB.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceScheduled, Roots: []string{"/b"}})
	if err != nil || !resultB.Started {
		t.Fatalf("Submit B = %+v, %v", resultB, err)
	}
	var ownerB string
	if err := db.QueryRow(`SELECT owner_id FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&ownerB); err != nil {
		t.Fatalf("read owner B: %v", err)
	}
	if ownerA == ownerB {
		t.Fatalf("takeover retained owner %q", ownerA)
	}

	renewed, err := coordinatorA.renewLease(context.Background(), libraries[0], resultA.TaskID, ownerA)
	if err != nil || renewed {
		t.Fatalf("old owner renew = %v, %v; want false, nil", renewed, err)
	}
	released, err := coordinatorA.releaseLease(context.Background(), libraries[0], resultA.TaskID, ownerA)
	if err != nil || released {
		t.Fatalf("old owner release = %v, %v; want false, nil", released, err)
	}

	var taskID int64
	var retainedOwner string
	if err := db.QueryRow(`SELECT scan_task_id, owner_id FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&taskID, &retainedOwner); err != nil {
		t.Fatalf("read retained lease: %v", err)
	}
	if taskID != resultB.TaskID || retainedOwner != ownerB {
		t.Fatalf("lease = task %d owner %q, want task %d owner %q", taskID, retainedOwner, resultB.TaskID, ownerB)
	}
}

type returningScanner struct {
	started   chan struct{}
	returnNow chan struct{}
}

func newReturningScanner() *returningScanner {
	return &returningScanner{started: make(chan struct{}), returnNow: make(chan struct{})}
}

func (s *returningScanner) ScanLibraryFoldersWithContextAndCallbacks(ctx context.Context, _ int64, _ []string, _ scanner.ScanCallbacks) (int, error) {
	close(s.started)
	select {
	case <-s.returnNow:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func waitForTaskStatus(t *testing.T, db *sql.DB, taskID int64, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var status string
		if err := db.QueryRow(`SELECT status FROM scan_task WHERE id=?`, taskID).Scan(&status); err != nil {
			t.Fatalf("read task status: %v", err)
		}
		if status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task status=%q, want %q", status, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNewRejectsSubsecondAndFractionalDurations(t *testing.T) {
	db, _ := openCoordinatorTestDB(t, 0)
	scanner := newBlockingScanner()
	defer close(scanner.release)
	tests := []struct {
		name  string
		opts  Options
		field string
	}{
		{name: "subsecond lease", opts: Options{LeaseDuration: 500 * time.Millisecond, HeartbeatInterval: time.Second}, field: "LeaseDuration"},
		{name: "fractional lease", opts: Options{LeaseDuration: 1500 * time.Millisecond, HeartbeatInterval: time.Second}, field: "LeaseDuration"},
		{name: "subsecond heartbeat", opts: Options{LeaseDuration: 2 * time.Second, HeartbeatInterval: 500 * time.Millisecond}, field: "HeartbeatInterval"},
		{name: "fractional heartbeat", opts: Options{LeaseDuration: 3 * time.Second, HeartbeatInterval: 1500 * time.Millisecond}, field: "HeartbeatInterval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.OwnerInstanceID = "instance"
			tt.opts.Scanner = scanner
			_, err := New(db, tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("New error=%v, want field %s", err, tt.field)
			}
		})
	}
}

func TestCoordinator_FinalizeFailureKeepsLease(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := newBlockingScanner()
	defer close(scanner.release)
	coordinator := newTestCoordinator(t, db, "instance-a", scanner)
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil || !result.Started {
		t.Fatalf("Submit = %+v, %v", result, err)
	}
	var owner string
	if err := db.QueryRow(`SELECT owner_id FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&owner); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_scan_finalize BEFORE UPDATE ON scan_task WHEN OLD.id=` + fmt.Sprint(result.TaskID) + ` BEGIN SELECT RAISE(ABORT, 'reject finalize'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	err = coordinator.finalizeAndRelease(context.Background(), result.TaskID, libraries[0], owner, "done", nil)
	if err == nil || !strings.Contains(err.Error(), "reject finalize") {
		t.Fatalf("finalize error=%v, want reject finalize", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_lease WHERE library_id=? AND scan_task_id=? AND owner_id=?`, libraries[0], result.TaskID, owner).Scan(&count); err != nil {
		t.Fatalf("count lease: %v", err)
	}
	if count != 1 {
		t.Fatalf("lease count=%d, want retained lease", count)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_scan_finalize`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
}

func TestCoordinator_CancelWinsScannerReturn(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := newReturningScanner()
	coordinator := newTestCoordinator(t, db, "instance-a", scanner)
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil || !result.Started {
		t.Fatalf("Submit = %+v, %v", result, err)
	}
	<-scanner.started
	if _, err := coordinator.Cancel(context.Background(), result.TaskID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	close(scanner.returnNow)
	waitForTaskStatus(t, db, result.TaskID, "cancelled")
	var cancelled int
	if err := db.QueryRow(`SELECT cancelled FROM scan_task WHERE id=?`, result.TaskID).Scan(&cancelled); err != nil {
		t.Fatalf("read cancelled: %v", err)
	}
	if cancelled != 1 {
		t.Fatalf("cancelled=%d, want 1", cancelled)
	}
}

func TestCoordinator_BackgroundFinalizeReportsOwnershipLost(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scannerA, scannerB := newBlockingScanner(), newBlockingScanner()
	defer close(scannerB.release)
	errorsSeen := make(chan error, 1)
	coordinatorA, err := New(db, Options{LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second, OwnerInstanceID: "instance-a", Scanner: scannerA, OnError: func(err error) { errorsSeen <- err }})
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	coordinatorB := newTestCoordinator(t, db, "instance-b", scannerB)
	resultA, err := coordinatorA.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil || !resultA.Started {
		t.Fatalf("Submit A = %+v, %v", resultA, err)
	}
	if _, err := db.Exec(`UPDATE scan_lease SET lease_until=datetime(CURRENT_TIMESTAMP, '-1 second') WHERE library_id=?`, libraries[0]); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	resultB, err := coordinatorB.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceScheduled, Roots: []string{"/b"}})
	if err != nil || !resultB.Started {
		t.Fatalf("Submit B = %+v, %v", resultB, err)
	}
	close(scannerA.release)
	select {
	case reported := <-errorsSeen:
		if !errors.Is(reported, ErrScanLeaseLost) {
			t.Fatalf("reported error=%v, want ErrScanLeaseLost", reported)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background finalize error")
	}
	var retainedTask int64
	if err := db.QueryRow(`SELECT scan_task_id FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&retainedTask); err != nil {
		t.Fatalf("read retained lease: %v", err)
	}
	if retainedTask != resultB.TaskID {
		t.Fatalf("retained task=%d, want %d", retainedTask, resultB.TaskID)
	}
}

type countingScanner struct {
	mu    sync.Mutex
	calls int
}

func (s *countingScanner) ScanLibraryFoldersWithContextAndCallbacks(context.Context, int64, []string, scanner.ScanCallbacks) (int, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return 0, nil
}

func (s *countingScanner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestCoordinator_RequestCancellationAfterCommitDoesNotStrandLease(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := newBlockingScanner()
	coordinator := newTestCoordinator(t, db, "instance-a", scanner)
	ctx, cancel := context.WithCancel(context.Background())
	coordinator.afterSubmitCommit = cancel

	result, err := coordinator.Submit(ctx, ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil || !result.Started {
		t.Fatalf("Submit = %+v, %v", result, err)
	}
	select {
	case <-scanner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("scanner did not start after request context cancellation")
	}
	close(scanner.release)
	waitForTaskStatus(t, db, result.TaskID, "done")
	var leases int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_lease WHERE scan_task_id=?`, result.TaskID).Scan(&leases); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if leases != 0 {
		t.Fatalf("lease count=%d, want 0 after scan completion", leases)
	}
}

func TestCoordinator_CancellationReadFailureFailsClosed(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := &countingScanner{}
	errorsSeen := make(chan error, 2)
	coordinator, err := New(db, Options{
		OwnerInstanceID: "instance-a",
		Scanner:         scanner,
		OnError:         func(err error) { errorsSeen <- err },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	coordinator.readCancelled = func(context.Context, int64) (int, error) {
		return 0, errors.New("injected cancellation read failure")
	}

	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil || !result.Started {
		t.Fatalf("Submit = %+v, %v", result, err)
	}
	waitForTaskStatus(t, db, result.TaskID, "failed")
	if calls := scanner.callCount(); calls != 0 {
		t.Fatalf("scanner calls=%d, want 0 on cancellation read failure", calls)
	}
	select {
	case reported := <-errorsSeen:
		if !strings.Contains(reported.Error(), "injected cancellation read failure") {
			t.Fatalf("reported error=%v", reported)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation read failure was not reported")
	}
}

func TestCoordinator_CancelCompletedTaskDoesNotMutateIt(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := &countingScanner{}
	coordinator := newTestCoordinator(t, db, "instance-a", scanner)
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil || !result.Started {
		t.Fatalf("Submit = %+v, %v", result, err)
	}
	waitForTaskStatus(t, db, result.TaskID, "done")

	if _, err := coordinator.Cancel(context.Background(), result.TaskID); err != nil {
		t.Fatalf("Cancel completed task: %v", err)
	}
	var status string
	var cancelled int
	if err := db.QueryRow(`SELECT status, cancelled FROM scan_task WHERE id=?`, result.TaskID).Scan(&status, &cancelled); err != nil {
		t.Fatalf("read completed task: %v", err)
	}
	if status != "done" || cancelled != 0 {
		t.Fatalf("task status=%q cancelled=%d, want done,0", status, cancelled)
	}
}

func TestCoordinator_CancelMissingTaskReturnsNotFound(t *testing.T) {
	db, _ := openCoordinatorTestDB(t, 0)
	scanner := &countingScanner{}
	coordinator := newTestCoordinator(t, db, "instance-a", scanner)
	if _, err := coordinator.Cancel(context.Background(), 999999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Cancel missing task error=%v, want sql.ErrNoRows", err)
	}
}

type errorScanner struct{ err error }

func (s errorScanner) ScanLibraryFoldersWithContextAndCallbacks(context.Context, int64, []string, scanner.ScanCallbacks) (int, error) {
	return 0, s.err
}

func waitForNoLease(t *testing.T, db *sql.DB, taskID int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM scan_lease WHERE scan_task_id=?`, taskID).Scan(&count); err != nil {
			t.Fatalf("count lease: %v", err)
		}
		if count == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease for task %d was not released", taskID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCoordinator_HeartbeatLossCancels(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := newBlockingScanner()
	errorsSeen := make(chan error, 4)
	coordinator, err := New(db, Options{
		LeaseDuration:     3 * time.Second,
		HeartbeatInterval: time.Second,
		OwnerInstanceID:   "instance-a",
		Scanner:           scanner,
		OnError:           func(err error) { errorsSeen <- err },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil || !result.Started {
		t.Fatalf("Submit = %+v, %v", result, err)
	}
	<-scanner.started

	var initialUpdated string
	if err := db.QueryRow(`SELECT updated_at FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&initialUpdated); err != nil {
		t.Fatalf("read initial lease: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var updated string
		if err := db.QueryRow(`SELECT updated_at FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&updated); err != nil {
			t.Fatalf("read renewed lease: %v", err)
		}
		if updated != initialUpdated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("heartbeat did not renew lease")
		}
		time.Sleep(20 * time.Millisecond)
	}

	insert, err := db.Exec(`INSERT INTO scan_task (library_id, status, source, started_at, updated_at) VALUES (?, 'running', 'scheduled', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, libraries[0])
	if err != nil {
		t.Fatalf("insert B task: %v", err)
	}
	taskB, err := insert.LastInsertId()
	if err != nil {
		t.Fatalf("B task id: %v", err)
	}
	const ownerB = "instance-b/new-task"
	if _, err := db.Exec(`UPDATE scan_lease SET scan_task_id=?, owner_id=?, lease_until=datetime(CURRENT_TIMESTAMP, '+30 seconds'), updated_at=CURRENT_TIMESTAMP WHERE library_id=?`, taskB, ownerB, libraries[0]); err != nil {
		t.Fatalf("replace lease: %v", err)
	}

	deadline = time.Now().Add(3 * time.Second)
	for {
		select {
		case reported := <-errorsSeen:
			if errors.Is(reported, ErrScanLeaseLost) {
				goto leaseLost
			}
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("lease loss was not reported")
		}
	}
leaseLost:
	var oldStatus string
	if err := db.QueryRow(`SELECT status FROM scan_task WHERE id=?`, result.TaskID).Scan(&oldStatus); err != nil || oldStatus != "running" {
		t.Fatalf("old task status=%q err=%v, want running after ownership loss", oldStatus, err)
	}
	var retainedTask int64
	var retainedOwner string
	if err := db.QueryRow(`SELECT scan_task_id, owner_id FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&retainedTask, &retainedOwner); err != nil {
		t.Fatalf("read B lease: %v", err)
	}
	if retainedTask != taskB || retainedOwner != ownerB {
		t.Fatalf("lease = task %d owner %q, want task %d owner %q", retainedTask, retainedOwner, taskB, ownerB)
	}
	seenLeaseLost := false
	deadline = time.Now().Add(2 * time.Second)
	for !seenLeaseLost && time.Now().Before(deadline) {
		select {
		case reported := <-errorsSeen:
			seenLeaseLost = strings.Contains(reported.Error(), "scan lease lost")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !seenLeaseLost {
		t.Fatal("OnError did not report scan lease lost")
	}
}

func TestCoordinator_FinalizesAndReleases(t *testing.T) {
	tests := []struct {
		name       string
		scanner    Scanner
		cancel     bool
		wantStatus string
	}{
		{name: "normal", scanner: &countingScanner{}, wantStatus: "done"},
		{name: "scanner error", scanner: errorScanner{err: errors.New("scan failed")}, wantStatus: "failed"},
		{name: "active cancel", scanner: newBlockingScanner(), cancel: true, wantStatus: "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, libraries := openCoordinatorTestDB(t, 1)
			coordinator := newTestCoordinator(t, db, "instance-a", tt.scanner)
			result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
			if err != nil || !result.Started {
				t.Fatalf("Submit = %+v, %v", result, err)
			}
			if tt.cancel {
				blocking := tt.scanner.(*blockingScanner)
				<-blocking.started
				if _, err := coordinator.Cancel(context.Background(), result.TaskID); err != nil {
					t.Fatalf("Cancel: %v", err)
				}
			}
			waitForTaskStatus(t, db, result.TaskID, tt.wantStatus)
			waitForNoLease(t, db, result.TaskID)
			if tt.cancel {
				var cancelled int
				if err := db.QueryRow(`SELECT cancelled FROM scan_task WHERE id=?`, result.TaskID).Scan(&cancelled); err != nil {
					t.Fatalf("read cancelled: %v", err)
				}
				if cancelled != 1 {
					t.Fatalf("cancelled=%d, want 1", cancelled)
				}
			}
		})
	}
}

func TestCoordinator_PersistsSources(t *testing.T) {
	for _, source := range []Source{SourceManual, SourceScheduled, SourceMonitor} {
		t.Run(string(source), func(t *testing.T) {
			db, libraries := openCoordinatorTestDB(t, 1)
			coordinator := newTestCoordinator(t, db, "instance-"+string(source), &countingScanner{})
			result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: source, Roots: []string{"/root"}})
			if err != nil || !result.Started {
				t.Fatalf("Submit = %+v, %v", result, err)
			}
			waitForTaskStatus(t, db, result.TaskID, "done")
			var persisted Source
			if err := db.QueryRow(`SELECT source FROM scan_task WHERE id=?`, result.TaskID).Scan(&persisted); err != nil {
				t.Fatalf("read source: %v", err)
			}
			if persisted != source {
				t.Fatalf("source=%q, want %q", persisted, source)
			}
		})
	}
}

type callbackScanner struct {
	callback func(context.Context, int64, string, string) error
}

func (s *callbackScanner) ScanLibraryFoldersWithContextAndCallbacks(ctx context.Context, _ int64, _ []string, callbacks scanner.ScanCallbacks) (int, error) {
	s.callback = callbacks.OnMediaAdded
	return 1, callbacks.OnMediaAdded(ctx, 99, "movie", "video")
}

func TestCoordinatorBuildsPerScanCallbackWithTaskID(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := &callbackScanner{}
	seen := make(chan [2]int64, 1)
	coordinator, err := New(db, Options{
		LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second,
		OwnerInstanceID: "callback-test", Scanner: scanner,
		OnMediaAdded: func(_ context.Context, taskID, mediaID int64, _, _ string) error {
			seen <- [2]int64{taskID, mediaID}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case got := <-seen:
		if got != [2]int64{result.TaskID, 99} {
			t.Fatalf("callback IDs=%v want [%d 99]", got, result.TaskID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for media callback")
	}
	if scanner.callback == nil {
		t.Fatal("scanner did not receive per-call callback")
	}
}

type insertingCallbackScanner struct{ db *sql.DB }

func (s *insertingCallbackScanner) ScanLibraryFoldersWithContextAndCallbacks(ctx context.Context, libraryID int64, _ []string, callbacks scanner.ScanCallbacks) (int, error) {
	res, err := s.db.Exec(`INSERT INTO media (library_id,file_id,file_type,duration) VALUES (?,'coordinator-media','video',120)`, libraryID)
	if err != nil {
		return 0, err
	}
	mediaID, _ := res.LastInsertId()
	return 1, callbacks.OnMediaAdded(ctx, mediaID, "movie", "video")
}

func TestCoordinatorCallbackEnqueuesRealPostIngestRowsWithTaskOwnership(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	enqueuer := postingest.NewEnqueuer(db, &config.Config{}, nil)
	coordinator, err := New(db, Options{
		LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second,
		OwnerInstanceID: "real-enqueue", Scanner: &insertingCallbackScanner{db: db},
		OnMediaAdded: func(ctx context.Context, taskID, mediaID int64, _ string, fileType string) error {
			_, err := enqueuer.EnqueueMedia(ctx, mediaID, &taskID, fileType)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count, owned int
		err = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN scan_task_id=? THEN 1 ELSE 0 END),0) FROM post_ingest_task`, result.TaskID).Scan(&count, &owned)
		if err == nil && count > 0 {
			if count != owned {
				t.Fatalf("rows=%d owned=%d", count, owned)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue rows not created: count=%d err=%v", count, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCoordinator_ShutdownStopsRunningScans(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	sc := newBlockingScanner()
	coordinator := newTestCoordinator(t, db, "shutdown-owner", sc)
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceMonitor, Roots: []string{"root"}})
	if err != nil || !result.Started {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	select {
	case <-sc.started:
	case <-time.After(time.Second):
		t.Fatal("scan did not start")
	}
	coordinator.Shutdown()
	waitForTaskStatus(t, db, result.TaskID, "failed")
}

func TestCoordinator_ShutdownContextWaitsForFinalization(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	sc := newBlockingScanner()
	coordinator := newTestCoordinator(t, db, "shutdown-wait-owner", sc)
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceMonitor, Roots: []string{"root"}})
	if err != nil || !result.Started {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	select {
	case <-sc.started:
	case <-time.After(time.Second):
		t.Fatal("scan did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.ShutdownContext(ctx); err != nil {
		t.Fatal(err)
	}
	var leases int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_lease WHERE scan_task_id=?`, result.TaskID).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("leases=%d", leases)
	}
}

func TestCoordinator_ShutdownContextHonorsTimeout(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	sc := &ignoringCancellationScanner{started: make(chan struct{}), release: make(chan struct{})}
	coordinator := newTestCoordinator(t, db, "shutdown-timeout-owner", sc)
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceMonitor, Roots: []string{"root"}})
	if err != nil || !result.Started {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	select {
	case <-sc.started:
	case <-time.After(time.Second):
		t.Fatal("scan did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := coordinator.ShutdownContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	close(sc.release)
}

type ignoringCancellationScanner struct {
	started chan struct{}
	release chan struct{}
}

func (s *ignoringCancellationScanner) ScanLibraryFoldersWithContextAndCallbacks(context.Context, int64, []string, scanner.ScanCallbacks) (int, error) {
	close(s.started)
	<-s.release
	return 0, nil
}

type cancelTargetSpy struct {
	mu    sync.Mutex
	calls []int64
	ctxs  []error
}

func (s *cancelTargetSpy) cancel(ctx context.Context, taskID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, taskID)
	s.ctxs = append(s.ctxs, ctx.Err())
	return nil
}

func insertScanPostTask(t *testing.T, db *sql.DB, libraryID, scanTaskID int64, status postingest.Status, suffix string) int64 {
	t.Helper()
	mediaResult, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,title,file_type) VALUES(?,?,?,?,?)`, libraryID, "cancel-"+suffix, "/cancel/"+suffix, suffix, "video")
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := mediaResult.LastInsertId()
	result, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status) VALUES(?,?,?,?)`, mediaID, scanTaskID, postingest.TaskPoster, status)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func TestCoordinator_CancelScanAtomicallyCancelsWaitingPostTasksAfterCommit(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := newBlockingScanner()
	target := &cancelTargetSpy{}
	coordinator, err := New(db, Options{LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second, OwnerInstanceID: "cancel-owner", Scanner: scanner, OnScanCancelled: target.cancel})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil {
		t.Fatal(err)
	}
	<-scanner.started
	waitingID := insertScanPostTask(t, db, libraries[0], result.TaskID, postingest.StatusWaiting, "waiting")
	runningID := insertScanPostTask(t, db, libraries[0], result.TaskID, postingest.StatusRunning, "running")
	doneID := insertScanPostTask(t, db, libraries[0], result.TaskID, postingest.StatusDone, "done")

	if _, err := coordinator.Cancel(context.Background(), result.TaskID); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, db, result.TaskID, "cancelled")
	for id, want := range map[int64]postingest.Status{waitingID: postingest.StatusCancelled, runningID: postingest.StatusRunning, doneID: postingest.StatusDone} {
		var got postingest.Status
		var finished sql.NullTime
		if err := db.QueryRow(`SELECT status,finished_at FROM post_ingest_task WHERE id=?`, id).Scan(&got, &finished); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("post task %d status=%s want %s", id, got, want)
		}
		if id == waitingID && !finished.Valid {
			t.Fatalf("waiting task %d has no finished_at", id)
		}
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if len(target.calls) != 1 || target.calls[0] != result.TaskID {
		t.Fatalf("cancel target calls=%v", target.calls)
	}
}

func TestCoordinator_CancelScanRollbackDoesNotNotifyLocalTargets(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	target := &cancelTargetSpy{}
	coordinator, err := New(db, Options{LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second, OwnerInstanceID: "rollback-owner", Scanner: &countingScanner{}, OnScanCancelled: target.cancel})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, db, result.TaskID, "done")
	if _, err := db.Exec(`UPDATE scan_task SET status='running',cancelled=0,finished_at=NULL WHERE id=?`, result.TaskID); err != nil {
		t.Fatal(err)
	}
	insertScanPostTask(t, db, libraries[0], result.TaskID, postingest.StatusWaiting, "rollback")
	if _, err := db.Exec(`CREATE TRIGGER reject_cancel_posts BEFORE UPDATE ON post_ingest_task WHEN NEW.status='cancelled' BEGIN SELECT RAISE(ABORT,'injected cancel failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Cancel(context.Background(), result.TaskID); err == nil {
		t.Fatal("Cancel succeeded, want trigger failure")
	}
	var cancelled int
	var status postingest.Status
	if err := db.QueryRow(`SELECT cancelled FROM scan_task WHERE id=?`, result.TaskID).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE scan_task_id=?`, result.TaskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if cancelled != 0 || status != postingest.StatusWaiting {
		t.Fatalf("rollback state cancelled=%d post=%s", cancelled, status)
	}
	if len(target.calls) != 0 {
		t.Fatalf("local target called before commit: %v", target.calls)
	}
}

func TestCoordinator_CancelScanUsesCleanupContextAfterCommit(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	target := &cancelTargetSpy{}
	coordinator, err := New(db, Options{LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second, OwnerInstanceID: "cleanup-owner", Scanner: &countingScanner{}, OnScanCancelled: target.cancel})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/a"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, db, result.TaskID, "done")
	if _, err := db.Exec(`UPDATE scan_task SET status='waiting',cancelled=0,finished_at=NULL WHERE id=?`, result.TaskID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator.afterCancelCommit = cancel
	if _, err := coordinator.Cancel(ctx, result.TaskID); err != nil {
		t.Fatal(err)
	}
	if len(target.calls) != 1 || target.ctxs[0] != nil {
		t.Fatalf("target calls=%v context errors=%v", target.calls, target.ctxs)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM scan_task WHERE id=?`, result.TaskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("waiting scan status=%s want cancelled", status)
	}
}

func TestCoordinator_CancelResultReportsTransitionAndTerminalNoOp(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := newBlockingScanner()
	coordinator := newTestCoordinator(t, db, "cancel-result-owner", scanner)
	running, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/running"}})
	if err != nil {
		t.Fatal(err)
	}
	<-scanner.started
	first, err := coordinator.Cancel(context.Background(), running.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Cancelled || first.Status != "cancelling" {
		t.Fatalf("first=%+v want cancelled=true status=cancelling", first)
	}
	second, err := coordinator.Cancel(context.Background(), running.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Cancelled || (second.Status != "cancelling" && second.Status != "cancelled") {
		t.Fatalf("second=%+v want no-op cancellation state", second)
	}
	waitForTaskStatus(t, db, running.TaskID, "cancelled")

	for _, status := range []string{"done", "failed", "cancelled"} {
		result, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,cancelled,finished_at) VALUES(?,?,?,?,CURRENT_TIMESTAMP)`, libraries[0], status, SourceManual, boolToInt(status == "cancelled"))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		got, err := coordinator.Cancel(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Cancelled || got.Status != status {
			t.Fatalf("terminal %s result=%+v", status, got)
		}
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestCoordinator_ConcurrentCancelOnlyOneReportsTransition(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	coordinator := newTestCoordinator(t, db, "concurrent-cancel-result", newBlockingScanner())
	result, err := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'waiting',?)`, libraries[0], SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	start := make(chan struct{})
	results := make(chan CancelResult, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; got, err := coordinator.Cancel(context.Background(), id); results <- got; errs <- err }()
	}
	close(start)
	transitions := 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if (<-results).Cancelled {
			transitions++
		}
	}
	if transitions != 1 {
		t.Fatalf("transitions=%d want 1", transitions)
	}
}

type contextObservingScanner struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (s *contextObservingScanner) ScanLibraryFoldersWithContextAndCallbacks(ctx context.Context, _ int64, _ []string, _ scanner.ScanCallbacks) (int, error) {
	close(s.started)
	<-ctx.Done()
	close(s.cancelled)
	return 0, ctx.Err()
}

func TestCoordinator_HeartbeatObservesRemotePersistentCancellation(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scannerA := &contextObservingScanner{started: make(chan struct{}), cancelled: make(chan struct{})}
	coordinatorA, err := New(db, Options{LeaseDuration: 3 * time.Second, HeartbeatInterval: time.Second, OwnerInstanceID: "remote-owner-a", Scanner: scannerA})
	if err != nil {
		t.Fatal(err)
	}
	coordinatorB, err := New(db, Options{LeaseDuration: 3 * time.Second, HeartbeatInterval: time.Second, OwnerInstanceID: "remote-owner-b", Scanner: &countingScanner{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinatorA.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/remote"}})
	if err != nil || !result.Started {
		t.Fatalf("Submit=%+v,%v", result, err)
	}
	<-scannerA.started
	mediaID := insertScanPostTask(t, db, libraries[0], result.TaskID, postingest.StatusWaiting, "remote-heartbeat")
	cancelResult, err := coordinatorB.Cancel(context.Background(), result.TaskID)
	if err != nil || !cancelResult.Cancelled || cancelResult.Status != "cancelling" {
		t.Fatalf("Cancel=%+v,%v", cancelResult, err)
	}
	select {
	case <-scannerA.cancelled:
	case <-time.After(2200 * time.Millisecond):
		t.Fatal("remote persistent cancellation was not observed by heartbeat")
	}
	waitForTaskStatus(t, db, result.TaskID, "cancelled")
	waitForNoLease(t, db, result.TaskID)
	var postStatus postingest.Status
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, mediaID).Scan(&postStatus); err != nil {
		t.Fatal(err)
	}
	if postStatus != postingest.StatusCancelled {
		t.Fatalf("post status=%s want cancelled", postStatus)
	}
}

func TestCoordinator_HeartbeatCancellationReadFailureFailsScan(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := &contextObservingScanner{started: make(chan struct{}), cancelled: make(chan struct{})}
	errorsSeen := make(chan error, 2)
	coordinator, err := New(db, Options{LeaseDuration: 3 * time.Second, HeartbeatInterval: time.Second, OwnerInstanceID: "heartbeat-read-error", Scanner: scanner, OnError: func(err error) { errorsSeen <- err }})
	if err != nil {
		t.Fatal(err)
	}
	var reads int
	coordinator.readCancelled = func(ctx context.Context, taskID int64) (int, error) {
		reads++
		if reads == 2 {
			return 0, errors.New("injected heartbeat cancellation read failure")
		}
		var cancelled int
		return cancelled, db.QueryRowContext(ctx, `SELECT cancelled FROM scan_task WHERE id=?`, taskID).Scan(&cancelled)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/read-error"}})
	if err != nil {
		t.Fatal(err)
	}
	<-scanner.started
	select {
	case <-scanner.cancelled:
	case <-time.After(2200 * time.Millisecond):
		t.Fatal("heartbeat read failure did not cancel scanner")
	}
	waitForTaskStatus(t, db, result.TaskID, "failed")
	select {
	case reported := <-errorsSeen:
		if !strings.Contains(reported.Error(), "scan cancellation heartbeat") {
			t.Fatalf("reported=%v", reported)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat read failure was not reported")
	}
}

func TestCoordinator_ProgressFlushFailureMarksSuccessfulScanFailed(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	if _, err := db.Exec(`CREATE TRIGGER reject_progress BEFORE UPDATE OF processed_count ON scan_task WHEN NEW.processed_count <> OLD.processed_count BEGIN SELECT RAISE(ABORT,'reject progress'); END`); err != nil {
		t.Fatal(err)
	}
	scanner := scannerFunc(func(_ context.Context, _ int64, _ []string, callbacks scanner.ScanCallbacks) (int, error) {
		callbacks.OnFile("retained.mp4", nil)
		return 0, nil
	})
	coordinator, err := New(db, Options{LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second, OwnerInstanceID: "progress-failure", Scanner: scanner})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"root"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, db, result.TaskID, "failed")
	var message string
	if err := db.QueryRow(`SELECT error_message FROM scan_task WHERE id=?`, result.TaskID).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "progress flush") || !strings.Contains(message, "reject progress") {
		t.Fatalf("error_message=%q", message)
	}
}

func TestCoordinator_FinalizationTimeoutBoundsShutdown(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	coordinator, err := New(db, Options{LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second, FinalizeTimeout: 50 * time.Millisecond, OwnerInstanceID: "bounded-finalize", Scanner: &countingScanner{}})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.readCancelled = func(ctx context.Context, _ int64) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"root"}})
	if err != nil || !result.Started {
		t.Fatalf("Submit=%+v,%v", result, err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if err := coordinator.ShutdownContext(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown took %v; finalization was not bounded", elapsed)
	}
	var leases int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_lease WHERE scan_task_id=?`, result.TaskID).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 1 {
		t.Fatalf("lease count=%d want retained for expiry takeover", leases)
	}
}

type scannerFunc func(context.Context, int64, []string, scanner.ScanCallbacks) (int, error)

func (f scannerFunc) ScanLibraryFoldersWithContextAndCallbacks(ctx context.Context, id int64, roots []string, callbacks scanner.ScanCallbacks) (int, error) {
	return f(ctx, id, roots, callbacks)
}

func TestRestartRecoveryCoordinatorTakesOverExpiredLeaseAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinator-restart.sqlite")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('restart','video','/restart')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := result.LastInsertId()
	oldTaskResult, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,started_at) VALUES(?,'running',?,CURRENT_TIMESTAMP)`, libraryID, SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	oldTaskID, _ := oldTaskResult.LastInsertId()
	const oldOwner = "old-process/lease"
	if _, err := db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'-1 second'))`, libraryID, oldTaskID, oldOwner); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	scanner := newBlockingScanner()
	defer close(scanner.release)
	coordinator := newTestCoordinator(t, reopened, "new-process", scanner)
	got, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraryID, Source: SourceScheduled, Roots: []string{"/restart"}})
	if err != nil || !got.Started || got.TaskID == oldTaskID {
		t.Fatalf("Submit after restart=%+v,%v old=%d", got, err, oldTaskID)
	}
	var leaseTask int64
	var leaseOwner string
	if err := reopened.QueryRow(`SELECT scan_task_id,owner_id FROM scan_lease WHERE library_id=?`, libraryID).Scan(&leaseTask, &leaseOwner); err != nil {
		t.Fatal(err)
	}
	if leaseTask != got.TaskID || !strings.HasPrefix(leaseOwner, "new-process/") {
		t.Fatalf("lease task=%d owner=%q", leaseTask, leaseOwner)
	}
	if renewed, err := coordinator.renewLease(context.Background(), libraryID, oldTaskID, oldOwner); err != nil || renewed {
		t.Fatalf("old renew=(%v,%v)", renewed, err)
	}
	if released, err := coordinator.releaseLease(context.Background(), libraryID, oldTaskID, oldOwner); err != nil || released {
		t.Fatalf("old release=(%v,%v)", released, err)
	}
}

func TestRestartRecoveryCoordinatorTakeoverFinalizesOldScan(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cancelled  int
		wantStatus string
	}{
		{name: "interrupted", wantStatus: "failed"},
		{name: "persistently cancelled", cancelled: 1, wantStatus: "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "takeover-finalize.sqlite")
			db, err := store.OpenSQLite(path)
			if err != nil {
				t.Fatal(err)
			}
			libraryResult, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('takeover','video','/takeover')`)
			if err != nil {
				t.Fatal(err)
			}
			libraryID, _ := libraryResult.LastInsertId()
			oldResult, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,cancelled,started_at) VALUES(?,'running',?,?,CURRENT_TIMESTAMP)`, libraryID, SourceManual, tc.cancelled)
			if err != nil {
				t.Fatal(err)
			}
			oldTaskID, _ := oldResult.LastInsertId()
			if _, err := db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'-1 second'))`, libraryID, oldTaskID, "dead-process/lease"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := store.OpenSQLite(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			scanner := newBlockingScanner()
			defer close(scanner.release)
			coordinator := newTestCoordinator(t, reopened, "takeover-process", scanner)
			got, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraryID, Source: SourceScheduled, Roots: []string{"/takeover"}})
			if err != nil || !got.Started {
				t.Fatalf("Submit=%+v,%v", got, err)
			}
			var oldStatus, oldMessage string
			var oldFinished sql.NullTime
			if err := reopened.QueryRow(`SELECT status,COALESCE(error_message,''),finished_at FROM scan_task WHERE id=?`, oldTaskID).Scan(&oldStatus, &oldMessage, &oldFinished); err != nil {
				t.Fatal(err)
			}
			if oldStatus != tc.wantStatus || !oldFinished.Valid || oldMessage != "scan lease expired and was taken over" {
				t.Fatalf("old status=%s message=%q finished=%v want=%s", oldStatus, oldMessage, oldFinished, tc.wantStatus)
			}
			var newStatus string
			if err := reopened.QueryRow(`SELECT status FROM scan_task WHERE id=?`, got.TaskID).Scan(&newStatus); err != nil {
				t.Fatal(err)
			}
			if newStatus != "running" {
				t.Fatalf("new status=%s want running", newStatus)
			}
		})
	}
}

func TestRestartRecoveryCoordinatorTakeoverRollsBackWhenOldFinalizationFails(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	oldResult, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,started_at) VALUES(?,'running',?,CURRENT_TIMESTAMP)`, libraries[0], SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	oldTaskID, _ := oldResult.LastInsertId()
	if _, err := db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'-1 second'))`, libraries[0], oldTaskID, "old/lease"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_expired_finalize BEFORE UPDATE ON scan_task WHEN OLD.id=%d BEGIN SELECT RAISE(ABORT,'reject expired finalize'); END`, oldTaskID)); err != nil {
		t.Fatal(err)
	}
	coordinator := newTestCoordinator(t, db, "rollback-process", &countingScanner{})
	if _, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceScheduled, Roots: []string{"/rollback"}}); err == nil || !strings.Contains(err.Error(), "reject expired finalize") {
		t.Fatalf("Submit error=%v", err)
	}
	var taskCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scan_task WHERE library_id=?`, libraries[0]).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("scan tasks=%d want only rolled-back old task", taskCount)
	}
	var leaseTask int64
	if err := db.QueryRow(`SELECT scan_task_id FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&leaseTask); err != nil {
		t.Fatal(err)
	}
	if leaseTask != oldTaskID {
		t.Fatalf("lease task=%d want old %d", leaseTask, oldTaskID)
	}
}

func TestRestartRecoveryCoordinatorConcurrentTakeoverFinalizesOldOnce(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	oldResult, err := db.Exec(`INSERT INTO scan_task(library_id,status,source,started_at) VALUES(?,'running',?,CURRENT_TIMESTAMP)`, libraries[0], SourceManual)
	if err != nil {
		t.Fatal(err)
	}
	oldTaskID, _ := oldResult.LastInsertId()
	if _, err := db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'-1 second'))`, libraries[0], oldTaskID, "expired/lease"); err != nil {
		t.Fatal(err)
	}
	coordinators := []*Coordinator{newTestCoordinator(t, db, "takeover-a", newBlockingScanner()), newTestCoordinator(t, db, "takeover-b", newBlockingScanner())}
	for _, c := range coordinators {
		defer func(c *Coordinator) { c.Shutdown() }(c)
	}
	start := make(chan struct{})
	results := make(chan SubmitResult, 2)
	errs := make(chan error, 2)
	for _, c := range coordinators {
		go func(c *Coordinator) {
			<-start
			got, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceScheduled, Roots: []string{"/concurrent"}})
			results <- got
			errs <- err
		}(c)
	}
	close(start)
	started := 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if (<-results).Started {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("started=%d want 1", started)
	}
	var status, message string
	var finished sql.NullTime
	if err := db.QueryRow(`SELECT status,COALESCE(error_message,''),finished_at FROM scan_task WHERE id=?`, oldTaskID).Scan(&status, &message, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || message != "scan lease expired and was taken over" || !finished.Valid {
		t.Fatalf("old=%s,%q,%v", status, message, finished)
	}
}

type manyMediaScanner struct {
	db    *sql.DB
	count int
}

func (s *manyMediaScanner) ScanLibraryFoldersWithContextAndCallbacks(ctx context.Context, libraryID int64, _ []string, callbacks scanner.ScanCallbacks) (int, error) {
	for i := 0; i < s.count; i++ {
		res, err := s.db.Exec(`INSERT INTO media(library_id,file_id,file_type,duration) VALUES (?,?, 'video',120)`, libraryID, fmt.Sprintf("enqueue-failure-%d", i))
		if err != nil {
			return i, err
		}
		mediaID, _ := res.LastInsertId()
		if err := callbacks.OnMediaAdded(ctx, mediaID, fmt.Sprintf("media-%d", i), "video"); err != nil {
			return i + 1, err
		}
	}
	return s.count, nil
}

func TestCoordinatorEnqueueFailureContinuesScanAndFailsTask(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	scanner := &manyMediaScanner{db: db, count: 100}
	coordinator, err := New(db, Options{
		LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second,
		OwnerInstanceID: "enqueue-failure", Scanner: scanner,
		OnMediaAdded: func(_ context.Context, _ int64, mediaID int64, _, _ string) error {
			if mediaID == 50 {
				return errors.New("enqueue sentinel")
			}
			_, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type) VALUES(?,'poster')`, mediaID)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, db, result.TaskID, "failed")
	var mediaCount, taskCount, failedMediaTasks int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media WHERE library_id=?`, libraries[0]).Scan(&mediaCount)
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task`).Scan(&taskCount)
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=50`).Scan(&failedMediaTasks)
	var message string
	_ = db.QueryRow(`SELECT COALESCE(error_message,'') FROM scan_task WHERE id=?`, result.TaskID).Scan(&message)
	if mediaCount != 100 || taskCount != 99 || failedMediaTasks != 0 || !strings.Contains(message, "enqueue sentinel") {
		t.Fatalf("media=%d tasks=%d failedTasks=%d message=%q", mediaCount, taskCount, failedMediaTasks, message)
	}
}

func TestFinalizeAndReleaseDoesNotMutateAfterTakeover(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	oldResult, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source,error_message) VALUES(?,'failed','manual','taken over')`, libraries[0])
	oldTaskID, _ := oldResult.LastInsertId()
	newResult, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraries[0])
	newTaskID, _ := newResult.LastInsertId()
	_, _ = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraries[0], newTaskID, "new-owner")
	coordinator := newTestCoordinator(t, db, "old-owner", &countingScanner{})
	if err := coordinator.finalizeAndRelease(context.Background(), oldTaskID, libraries[0], "old-owner", "done", nil); !errors.Is(err, ErrScanLeaseLost) {
		t.Fatalf("finalize error=%v want ErrScanLeaseLost", err)
	}
	var status, message string
	_ = db.QueryRow(`SELECT status,COALESCE(error_message,'') FROM scan_task WHERE id=?`, oldTaskID).Scan(&status, &message)
	var leaseTask int64
	var leaseOwner string
	_ = db.QueryRow(`SELECT scan_task_id,owner_id FROM scan_lease WHERE library_id=?`, libraries[0]).Scan(&leaseTask, &leaseOwner)
	if status != "failed" || message != "taken over" || leaseTask != newTaskID || leaseOwner != "new-owner" {
		t.Fatalf("old=%s/%q lease=%d/%q", status, message, leaseTask, leaseOwner)
	}
}

type discoveryCallbackScanner struct {
	discovery scanner.ScanDiscovery
}

func (s *discoveryCallbackScanner) ScanLibraryFoldersWithContextAndCallbacks(ctx context.Context, _ int64, _ []string, callbacks scanner.ScanCallbacks) (int, error) {
	return 1, callbacks.OnMediaDiscoveredTx(ctx, nil, s.discovery)
}

func TestCoordinatorForwardsDiscoveryDiagnosticsWithTaskID(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	discovery := scanner.ScanDiscovery{MediaID: 99, FileType: "video", MetadataAttempt: scanner.MetadataAttempt{Attempted: true, Fields: []string{"duration"}, Errors: []scanner.MetadataDiagnostic{{Source: "probe", Message: "partial"}}}}
	seen := make(chan struct {
		taskID int64
		value  scanner.ScanDiscovery
	}, 1)
	coordinator, err := New(db, Options{
		LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second,
		OwnerInstanceID: "discovery-callback-test", Scanner: &discoveryCallbackScanner{discovery: discovery},
		OnMediaDiscoveredTx: func(_ context.Context, _ *sql.Tx, taskID int64, got scanner.ScanDiscovery) error {
			seen <- struct {
				taskID int64
				value  scanner.ScanDiscovery
			}{taskID: taskID, value: got}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seen:
		if got.taskID != result.TaskID || got.value.MediaID != 99 || !got.value.MetadataAttempt.Attempted || len(got.value.MetadataAttempt.Errors) != 1 {
			t.Fatalf("callback task=%d discovery=%+v", got.taskID, got.value)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for discovery callback")
	}
}
