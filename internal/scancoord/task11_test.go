package scancoord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"knox-media/internal/store"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func task11SQLiteError(code int) error {
	err := &sqlite.Error{}
	field := reflect.ValueOf(err).Elem().FieldByName("code")
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().SetInt(int64(code))
	return err
}

func TestCoordinatorBusyHeartbeatRetriesUntilLeaseBudget(t *testing.T) {
	db, _ := openCoordinatorTestDB(t, 0)
	c := newTestCoordinator(t, db, "heartbeat-budget", &countingScanner{})
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.heartbeatSafety = 100 * time.Millisecond
	var attempts atomic.Int32
	c.renewLeaseAttempt = func(context.Context, int64, int64, string) (bool, time.Time, error) {
		if attempts.Add(1) < 3 {
			return false, time.Time{}, task11SQLiteError(sqlite3.SQLITE_BUSY)
		}
		return true, now.Add(time.Minute), nil
	}
	deadline, err := c.heartbeat(context.Background(), 1, 2, "owner", now.Add(900*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts=%d want 3", attempts.Load())
	}
	if !deadline.Equal(now.Add(time.Minute)) {
		t.Fatalf("deadline=%v", deadline)
	}
}

func TestCoordinatorBusyHeartbeatDoesNotKillScanBeforeDeadline(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	sc := newBlockingScanner()
	errs := make(chan error, 4)
	c, err := New(db, Options{LeaseDuration: 5 * time.Second, HeartbeatInterval: time.Second, OwnerInstanceID: "busy-live", Scanner: sc, OnError: func(err error) { errs <- err }})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	c.renewLeaseAttempt = func(context.Context, int64, int64, string) (bool, time.Time, error) {
		if calls.Add(1) < 3 {
			return false, time.Time{}, task11SQLiteError(sqlite3.SQLITE_BUSY)
		}
		return true, time.Now().UTC().Add(5 * time.Second), nil
	}
	result, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/busy"}})
	if err != nil {
		t.Fatal(err)
	}
	<-sc.started
	deadline := time.After(1800 * time.Millisecond)
	for calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("heartbeat did not retry; calls=%d errors=%d", calls.Load(), len(errs))
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-sc.release:
		t.Fatal("scanner release channel unexpectedly closed")
	default:
	}
	c.mu.Lock()
	_, active := c.cancels[result.TaskID]
	c.mu.Unlock()
	if !active {
		t.Fatal("scan was cancelled before confirmed lease deadline")
	}
	close(sc.release)
	waitForTaskStatus(t, db, result.TaskID, "done")
}

func TestReadCancellationReturnsErrScanTaskMissing(t *testing.T) {
	db, _ := openCoordinatorTestDB(t, 0)
	c := newTestCoordinator(t, db, "missing", &countingScanner{})
	_, err := c.readCancellation(context.Background(), 4242)
	var missing ErrScanTaskMissing
	if !errors.As(err, &missing) || missing.TaskID != 4242 {
		t.Fatalf("error=%v missing=%+v", err, missing)
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("raw sql.ErrNoRows leaked: %v", err)
	}
}

func TestFinalizeFailurePersistsRecovery(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	sc := newReturningScanner()
	reported := make(chan error, 4)
	c, err := New(db, Options{LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second, OwnerInstanceID: "persist-recovery", Scanner: sc, OnError: func(err error) { reported <- err }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/recover"}})
	if err != nil {
		t.Fatal(err)
	}
	<-sc.started
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_task11_finalize BEFORE UPDATE ON scan_task WHEN OLD.id=%d BEGIN SELECT RAISE(ABORT,'task11 finalize failure'); END`, result.TaskID)); err != nil {
		t.Fatal(err)
	}
	close(sc.returnNow)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int
		err = db.QueryRow(`SELECT COUNT(*) FROM scan_finalize_recovery WHERE task_id=?`, result.TaskID).Scan(&count)
		if err == nil && count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovery count=%d err=%v", count, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRecoverPendingFinalizationsCompletesAndDeletesRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "finalize-recover.sqlite")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, _ := db.Exec(`INSERT INTO library(name,type,path) VALUES('recover','video','/recover')`)
	libraryID, _ := lib.LastInsertId()
	task, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraryID)
	taskID, _ := task.LastInsertId()
	const owner = "dead-process/scan"
	_, _ = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraryID, taskID, owner)
	_, err = db.Exec(`INSERT INTO scan_finalize_recovery(task_id,library_id,owner_id,desired_status,error_message,cancelled,next_available_at) VALUES(?,?,?,'done',NULL,0,CURRENT_TIMESTAMP)`, taskID, libraryID, owner)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverPendingFinalizations(context.Background(), db, 10)
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	var status string
	var rows, leases int
	_ = db.QueryRow(`SELECT status FROM scan_task WHERE id=?`, taskID).Scan(&status)
	_ = db.QueryRow(`SELECT COUNT(*) FROM scan_finalize_recovery WHERE task_id=?`, taskID).Scan(&rows)
	_ = db.QueryRow(`SELECT COUNT(*) FROM scan_lease WHERE scan_task_id=?`, taskID).Scan(&leases)
	if status != "done" || rows != 0 || leases != 0 {
		t.Fatalf("status=%s recovery=%d leases=%d", status, rows, leases)
	}
}

func requireScanDiagnostic(t *testing.T, err error, operation, owner string, taskID, libraryID int64, wantBudget bool) store.SQLiteDiagnostic {
	t.Helper()
	var diagnostic store.SQLiteDiagnostic
	if !errors.As(err, &diagnostic) {
		t.Fatalf("missing SQLiteDiagnostic in %v", err)
	}
	if diagnostic.Operation != operation || diagnostic.Owner != owner || diagnostic.TaskID != taskID || diagnostic.LibraryID != libraryID {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
	if wantBudget && diagnostic.RemainingLeaseBudget <= 0 {
		t.Fatalf("remaining budget=%v", diagnostic.RemainingLeaseBudget)
	}
	return diagnostic
}

func TestReadCancellationMissingIncludesScanDiagnostic(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	c := newTestCoordinator(t, db, "missing-owner", &countingScanner{})
	deadline := c.now().Add(time.Second)
	_, err := c.readCancellationOwned(context.Background(), 919, libraries[0], "missing-owner/task", deadline)
	var missing ErrScanTaskMissing
	if !errors.As(err, &missing) || missing.TaskID != 919 {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	requireScanDiagnostic(t, err, "scan_read_cancellation", "missing-owner/task", 919, libraries[0], true)
}

func TestHeartbeatFailureIncludesCompleteScanDiagnostic(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	c := newTestCoordinator(t, db, "heartbeat-owner", &countingScanner{})
	c.renewLeaseAttempt = func(context.Context, int64, int64, string) (bool, time.Time, error) {
		return false, time.Time{}, task11SQLiteError(sqlite3.SQLITE_BUSY_SNAPSHOT)
	}
	deadline := c.now().Add(400 * time.Millisecond)
	_, err := c.heartbeat(context.Background(), libraries[0], 71, "heartbeat-owner/71", deadline)
	d := requireScanDiagnostic(t, err, "scan_heartbeat", "heartbeat-owner/71", 71, libraries[0], false)
	if d.PrimaryCode != sqlite3.SQLITE_BUSY || d.ExtendedCode != sqlite3.SQLITE_BUSY_SNAPSHOT {
		t.Fatalf("codes=%d/%d", d.PrimaryCode, d.ExtendedCode)
	}
}

func TestFinalizeFailureIncludesCompleteScanDiagnostic(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	c := newTestCoordinator(t, db, "final-owner", &countingScanner{})
	result, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraries[0])
	taskID, _ := result.LastInsertId()
	_, _ = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraries[0], taskID, "final-owner/task")
	_, _ = db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_diag_finalize BEFORE UPDATE ON scan_task WHEN OLD.id=%d BEGIN SELECT RAISE(ABORT,'diag finalize'); END`, taskID))
	err := c.finalizeAndRelease(context.Background(), taskID, libraries[0], "final-owner/task", "done", nil)
	requireScanDiagnostic(t, err, "scan_finalize", "final-owner/task", taskID, libraries[0], false)
}

func TestSubmitFailureIncludesLibraryDiagnostic(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	c := newTestCoordinator(t, db, "submit-owner", &countingScanner{})
	_, _ = db.Exec(`CREATE TRIGGER reject_diag_submit BEFORE INSERT ON scan_task BEGIN SELECT RAISE(ABORT,'diag submit'); END`)
	_, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/diag"}})
	requireScanDiagnostic(t, err, "scan_submit", "submit-owner", 0, libraries[0], false)
}

func TestFinalCancellationUsesLastConfirmedLeaseDeadline(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	base := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	sc := newReturningScanner()
	reported := make(chan error, 8)
	c, err := New(db, Options{LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second, OwnerInstanceID: "final-deadline", Scanner: sc, OnError: func(err error) { reported <- err }})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Int64
	current.Store(base.UnixNano())
	c.now = func() time.Time { return time.Unix(0, current.Load()).UTC() }
	c.heartbeatSafety = 100 * time.Millisecond
	var reads atomic.Int32
	c.readCancelled = func(context.Context, int64) (int, error) {
		if reads.Add(1) == 1 {
			return 0, nil
		}
		return 0, task11SQLiteError(sqlite3.SQLITE_BUSY)
	}
	result, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/final-deadline"}})
	if err != nil {
		t.Fatal(err)
	}
	<-sc.started
	var deadline time.Time
	if err := db.QueryRow(`SELECT lease_until FROM scan_lease WHERE scan_task_id=?`, result.TaskID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	current.Store(deadline.Add(-50 * time.Millisecond).UnixNano())
	close(sc.returnNow)
	var diagnostic store.SQLiteDiagnostic
	timeout := time.After(2 * time.Second)
	for {
		select {
		case got := <-reported:
			if errors.As(got, &diagnostic) && diagnostic.Operation == "scan_read_cancellation" {
				goto found
			}
		case <-timeout:
			t.Fatal("missing final cancellation diagnostic")
		}
	}
found:
	if !diagnostic.HasRemainingLeaseBudget || diagnostic.RemainingLeaseBudget != 0 {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
}

func TestFinalizeRecoveryPersistenceRetriesUntilDurableAndDeduplicates(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	c := newTestCoordinator(t, db, "durable-owner", &countingScanner{})
	var attempts atomic.Int32
	c.persistRecoveryAttempt = func(ctx context.Context, r finalizeRecovery) error {
		if attempts.Add(1) <= 3 {
			return task11SQLiteError(sqlite3.SQLITE_BUSY)
		}
		return c.persistFinalizeRecovery(ctx, r)
	}
	task, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraries[0])
	taskID, _ := task.LastInsertId()
	owner := "durable-owner/701"
	_, _ = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraries[0], taskID, owner)
	r := finalizeRecovery{TaskID: taskID, LibraryID: libraries[0], Owner: owner, Status: "failed"}
	c.enqueueFinalizeRecovery(r)
	c.enqueueFinalizeRecovery(r)
	deadline := time.After(2 * time.Second)
	for c.pendingFinalizeRecoveryCount() != 0 {
		select {
		case <-deadline:
			t.Fatal("recovery never became durable")
		case <-time.After(time.Millisecond):
		}
	}
	if attempts.Load() != 4 {
		t.Fatalf("attempts=%d want 4", attempts.Load())
	}
	if err := c.ShutdownContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeRecoveryPersistenceShutdownHonorsContext(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	c := newTestCoordinator(t, db, "shutdown-persist", &countingScanner{})
	c.persistRecoveryAttempt = func(context.Context, finalizeRecovery) error { return task11SQLiteError(sqlite3.SQLITE_BUSY) }
	task, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraries[0])
	taskID, _ := task.LastInsertId()
	owner := "shutdown-persist/702"
	_, _ = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraries[0], taskID, owner)
	c.enqueueFinalizeRecovery(finalizeRecovery{TaskID: taskID, LibraryID: libraries[0], Owner: owner, Status: "failed"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := c.ShutdownContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestRecoverPendingFinalizationsClaimsOnlyProcessedLimit(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	for i := 0; i < 3; i++ {
		task, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraries[0])
		id, _ := task.LastInsertId()
		owner := fmt.Sprintf("limit/%d", id)
		_, _ = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraries[0], id, owner)
		_, _ = db.Exec(`INSERT INTO scan_finalize_recovery(task_id,library_id,owner_id,desired_status) VALUES(?,?,?,'done')`, id, libraries[0], owner)
	}
	recovered, err := RecoverPendingFinalizations(context.Background(), db, 1)
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	var claimed int
	_ = db.QueryRow(`SELECT COUNT(*) FROM scan_finalize_recovery WHERE claim_owner IS NOT NULL`).Scan(&claimed)
	if claimed != 0 {
		t.Fatalf("unprocessed claimed=%d", claimed)
	}
}

func TestRecoveryClaimLostBeforeFinalizeIsTypedAndDoesNotMutateTask(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	task, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraries[0])
	taskID, _ := task.LastInsertId()
	owner := "claimlost/task"
	_, _ = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraries[0], taskID, owner)
	row, _ := db.Exec(`INSERT INTO scan_finalize_recovery(task_id,library_id,owner_id,desired_status,claim_owner,claim_until) VALUES(?,?,?,'done','old',datetime(CURRENT_TIMESTAMP,'+1 minute'))`, taskID, libraries[0], owner)
	id, _ := row.LastInsertId()
	err := finalizeClaimedRecovery(context.Background(), db, pendingFinalize{id: id, taskID: taskID, libraryID: libraries[0], owner: owner, status: "done"}, "new-token")
	var lost ErrFinalizeRecoveryClaimLost
	if !errors.As(err, &lost) {
		t.Fatalf("err=%v", err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM scan_task WHERE id=?`, taskID).Scan(&status)
	if status != "running" {
		t.Fatalf("status=%s", status)
	}
}

func TestShutdownWaitsForScanToEnqueueRecoveryBeforeStoppingWorker(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	sc := newBlockingScanner()
	persisted := make(chan struct{})
	releasePersist := make(chan struct{})
	c := newTestCoordinator(t, db, "shutdown-race", sc)
	c.persistRecoveryAttempt = func(ctx context.Context, r finalizeRecovery) error {
		close(persisted)
		select {
		case <-releasePersist:
			return c.persistFinalizeRecovery(ctx, r)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	result, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/race"}})
	if err != nil {
		t.Fatal(err)
	}
	<-sc.started
	_, _ = db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_shutdown_finalize BEFORE UPDATE ON scan_task WHEN OLD.id=%d BEGIN SELECT RAISE(ABORT,'shutdown finalize'); END`, result.TaskID))
	done := make(chan error, 1)
	go func() { done <- c.ShutdownContext(context.Background()) }()
	select {
	case <-persisted:
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not enqueue recovery")
	}
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before recovery persisted: %v", err)
	default:
	}
	_, _ = db.Exec(`DROP TRIGGER reject_shutdown_finalize`)
	close(releasePersist)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM scan_finalize_recovery WHERE task_id=?`, result.TaskID).Scan(&count)
	if count != 1 {
		t.Fatalf("recovery rows=%d", count)
	}
}

func TestSubmitAfterShutdownIsRejected(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	c := newTestCoordinator(t, db, "closed-submit", &countingScanner{})
	if err := c.ShutdownContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/closed"}})
	var closed ErrCoordinatorShuttingDown
	if !errors.As(err, &closed) {
		t.Fatalf("err=%v", err)
	}
}

func TestShutdownWaitsForInflightSubmitThenCancelsRegisteredScan(t *testing.T) {
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('gate','video','/gate')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := lib.LastInsertId()
	libraries := []int64{libraryID}
	sc := &contextObservingScanner{started: make(chan struct{}), cancelled: make(chan struct{})}
	c := newTestCoordinator(t, db, "submit-gate", sc)
	c.finalizeAttempt = func(context.Context, int64, int64, string, string, any) error { return nil }
	registered := make(chan struct{}, 1)
	c.afterScanRegistered = func() { registered <- struct{}{} }
	entered := make(chan struct{})
	release := make(chan struct{})
	c.afterSubmitEntry = func() { close(entered); <-release }
	submitDone := make(chan error, 1)
	var result SubmitResult
	var submitErr error
	go func() {
		result, submitErr = c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/gate"}})
		submitDone <- submitErr
	}()
	<-entered
	shutdownDone := make(chan error, 1)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	go func() { shutdownDone <- c.ShutdownContext(shutdownCtx) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned during submit: %v", err)
	default:
	}
	close(release)
	if err := <-submitDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-registered:
	default:
		t.Fatal("scan was not registered")
	}
	if result.TaskID == 0 {
		t.Fatal("submit did not commit task")
	}
}

func TestShutdownWaitsForInflightSubmitFailure(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	c := newTestCoordinator(t, db, "submit-fail-gate", &countingScanner{})
	entered := make(chan struct{})
	release := make(chan struct{})
	c.afterSubmitEntry = func() { close(entered); <-release }
	_, _ = db.Exec(`CREATE TRIGGER reject_inflight_submit BEFORE INSERT ON scan_task BEGIN SELECT RAISE(ABORT,'submit failed'); END`)
	submitDone := make(chan error, 1)
	go func() {
		_, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/fail"}})
		submitDone <- err
	}()
	<-entered
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- c.ShutdownContext(context.Background()) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned during failed transaction: %v", err)
	default:
	}
	close(release)
	if err := <-submitDone; err == nil {
		t.Fatal("submit unexpectedly succeeded")
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownTimeoutDuringSubmitDoesNotCancelLaterRecovery(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	sc := newBlockingScanner()
	c := newTestCoordinator(t, db, "retry-submit-shutdown", sc)
	entered := make(chan struct{})
	releaseSubmit := make(chan struct{})
	c.afterSubmitEntry = func() { close(entered); <-releaseSubmit }
	persisted := make(chan struct{}, 1)
	c.persistRecoveryAttempt = func(ctx context.Context, r finalizeRecovery) error {
		err := c.persistFinalizeRecovery(ctx, r)
		if err == nil {
			select {
			case persisted <- struct{}{}:
			default:
			}
		}
		return err
	}
	resultCh := make(chan SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := c.Submit(context.Background(), ScanRequest{LibraryID: libraries[0], Source: SourceManual, Roots: []string{"/retry"}})
		resultCh <- r
		errCh <- err
	}()
	<-entered
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.ShutdownContext(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first shutdown err=%v", err)
	}
	close(releaseSubmit)
	result := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	<-sc.started
	_, _ = db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_retry_submit_finalize BEFORE UPDATE ON scan_task WHEN OLD.id=%d BEGIN SELECT RAISE(ABORT,'retry finalize'); END`, result.TaskID))
	close(sc.release)
	select {
	case <-persisted:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery did not persist after timed-out shutdown")
	}
	long, longCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer longCancel()
	if err := c.ShutdownContext(long); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	if c.pendingFinalizeRecoveryCount() != 0 {
		t.Fatalf("pending=%d", c.pendingFinalizeRecoveryCount())
	}
}

func TestShutdownTimeoutDuringRecoveryKeepsWorkerRetrying(t *testing.T) {
	db, libraries := openCoordinatorTestDB(t, 1)
	c := newTestCoordinator(t, db, "retry-recovery-shutdown", &countingScanner{})
	task, _ := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','manual')`, libraries[0])
	taskID, _ := task.LastInsertId()
	owner := "retry-recovery-shutdown/task"
	_, _ = db.Exec(`INSERT INTO scan_lease(library_id,scan_task_id,owner_id,lease_until) VALUES(?,?,?,datetime(CURRENT_TIMESTAMP,'+1 minute'))`, libraries[0], taskID, owner)
	release := make(chan struct{})
	attempted := make(chan struct{}, 1)
	c.persistRecoveryAttempt = func(ctx context.Context, r finalizeRecovery) error {
		select {
		case attempted <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return c.persistFinalizeRecovery(ctx, r)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.enqueueFinalizeRecovery(finalizeRecovery{TaskID: taskID, LibraryID: libraries[0], Owner: owner, Status: "failed"})
	<-attempted
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.ShutdownContext(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first shutdown err=%v", err)
	}
	close(release)
	long, longCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer longCancel()
	if err := c.ShutdownContext(long); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	if c.pendingFinalizeRecoveryCount() != 0 {
		t.Fatalf("pending=%d", c.pendingFinalizeRecoveryCount())
	}
}
