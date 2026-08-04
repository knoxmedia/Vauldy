package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"knox-media/internal/store"

	_ "modernc.org/sqlite"
)

// openReleaseTestDB opens an in-memory DB with the full scheduler schema plus
// policy_revision (needed for reservation FK).
func openReleaseTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openSchedulerTestDB(t)
	if _, err := db.Exec(
		store.SchedulerPolicyRevisionSchema + ";" +
			store.SchedulerControlSchema + ";" +
			store.SchedulerFairnessSchema + ";" +
			store.SchedulerReservationSchema + ";" +
			store.SchedulerAuditSchema + ";" +
			store.SchedulerIndexesSQL,
	); err != nil {
		t.Fatalf("create scheduler schema: %v", err)
	}
	// Insert an active policy revision so reservations have a valid FK target.
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO scheduler_policy_revision(schema_version,policy_json,author,reason,validation_hash,is_active,activated_at) VALUES(1,'{}','test','test','test',1,CURRENT_TIMESTAMP)`,
	); err != nil {
		t.Fatalf("insert active policy: %v", err)
	}
	return db
}

// seedReservation creates an active reservation and returns its execution_id.
var seedCounter int64

func seedReservation(t *testing.T, db *sql.DB) (executionID string) {
	t.Helper()
	seedCounter++
	executionID = fmt.Sprintf("test-owner/seed-%d-%d", seedCounter, time.Now().UnixNano())
	result, err := db.ExecContext(context.Background(),
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,'poster',1,1,'active',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`,
		executionID)
	if err != nil {
		t.Fatalf("insert seed reservation: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	if id <= 0 {
		t.Fatal("expected non-zero id")
	}
	return executionID
}

func assertReservationReleased(t *testing.T, db *sql.DB, executionID string) {
	t.Helper()
	var status string
	var releasedAt sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT status, CAST(released_at AS TEXT) FROM scheduler_reservation WHERE execution_id=?`, executionID,
	).Scan(&status, &releasedAt); err != nil {
		t.Fatalf("query reservation: %v", err)
	}
	if status != "released" {
		t.Fatalf("status=%q want released", status)
	}
	if !releasedAt.Valid || releasedAt.String == "" {
		t.Fatal("released_at is NULL, want non-NULL")
	}
}

func assertReservationActive(t *testing.T, db *sql.DB, executionID string) {
	t.Helper()
	var status string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM scheduler_reservation WHERE execution_id=?`, executionID,
	).Scan(&status); err != nil {
		t.Fatalf("query reservation: %v", err)
	}
	if status != "active" {
		t.Fatalf("status=%q want active", status)
	}
}

func assertExactlyOneRelease(t *testing.T, db *sql.DB, executionID string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM scheduler_reservation WHERE execution_id=? AND status='released'`, executionID,
	).Scan(&count); err != nil {
		t.Fatalf("count released: %v", err)
	}
	if count != 1 {
		t.Fatalf("released count=%d want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Basic release lifecycle
// ---------------------------------------------------------------------------

func TestReleaseReservationTxBasic(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "test_complete", "test-owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
	assertExactlyOneRelease(t, db, eid)
}

func TestReleaseReservationTxRollback(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx, eid, "test_complete", "test-owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	_ = tx.Rollback()

	assertReservationActive(t, db, eid)
}

func TestReleaseReservationTxDuplicateIsNoopAfterFirstRelease(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	// First release succeeds.
	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx1, eid, "complete", "owner"); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}

	// Second release must fail (not found or already released).
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	if err := ReleaseReservationTx(ctx, tx2, eid, "duplicate", "owner"); err == nil {
		t.Fatal("expected error on duplicate release")
	}
}

func TestReleaseReservationTxReleasedAtIsNullCAS(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "complete", "owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	var releasedAt sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT CAST(released_at AS TEXT) FROM scheduler_reservation WHERE execution_id=?`, eid,
	).Scan(&releasedAt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !releasedAt.Valid {
		t.Fatal("released_at IS NULL after CAS release")
	}
	// Second attempt with explicit released_at IS NULL check should not update.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	result, err := tx2.ExecContext(ctx,
		`UPDATE scheduler_reservation SET released_at=CURRENT_TIMESTAMP WHERE execution_id=? AND released_at IS NULL`, eid)
	if err != nil {
		t.Fatalf("CAS update: %v", err)
	}
	n, _ := result.RowsAffected()
	if n != 0 {
		t.Fatalf("CAS update affected %d rows, want 0 (released_at already set)", n)
	}
}

// ---------------------------------------------------------------------------
// Complete scenario
// ---------------------------------------------------------------------------

func TestReleaseReservationTxComplete(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "complete", "test-owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
	assertExactlyOneRelease(t, db, eid)
}

// ---------------------------------------------------------------------------
// Retryable / permanent fail scenario
// ---------------------------------------------------------------------------

func TestReleaseReservationTxRetryableFail(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "retryable_fail", "test-owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
}

func TestReleaseReservationTxPermanentFail(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "permanent_fail", "test-owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
}

// ---------------------------------------------------------------------------
// Cancellation scenario
// ---------------------------------------------------------------------------

func TestReleaseReservationTxCancellation(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "cancelled", "test-owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
}

// ---------------------------------------------------------------------------
// Shutdown uncertainty scenario
// ---------------------------------------------------------------------------

func TestReleaseReservationTxShutdown(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "shutdown", "test-owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
}

// ---------------------------------------------------------------------------
// Timeout scenario
// ---------------------------------------------------------------------------

func TestReleaseReservationTxTimeout(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "timeout", "test-owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
}

// ---------------------------------------------------------------------------
// Expired recovery
// ---------------------------------------------------------------------------

func TestReleaseReservationTxExpiredRecovery(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "expired_recovery", "recovery"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
}

// ---------------------------------------------------------------------------
// Startup interruption
// ---------------------------------------------------------------------------

func TestReleaseReservationTxStartupInterruption(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "startup_interruption", "startup"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
}

// ---------------------------------------------------------------------------
// Generation supersession
// ---------------------------------------------------------------------------

func TestReleaseReservationTxGenerationSupersession(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()

	// Create old-gen reservation.
	oldEID := seedReservation(t, db)

	// Release old-gen.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, oldEID, "generation_supersession", "new-gen"); err != nil {
		t.Fatalf("release old-gen reservation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, oldEID)

	// New generation can create its own reservation.
	newEID := seedReservation(t, db)
	assertReservationActive(t, db, newEID)
}

// ---------------------------------------------------------------------------
// GPU fallback
// ---------------------------------------------------------------------------

func TestReleaseReservationTxGPUFallback(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()

	// Create GPU reservation with gpu task type.
	gpuEID := "test-owner/" + fmt.Sprintf("gpu-%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx,
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,'thumbnail',1,1,'active',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`,
		gpuEID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, gpuEID, "gpu_fallback", "fallback"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, gpuEID)
}

// ---------------------------------------------------------------------------
// Duplicate callbacks are no-ops
// ---------------------------------------------------------------------------

func TestReleaseReservationTxDuplicateComplete(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	// First completion.
	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx1, eid, "complete", "owner"); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}

	// Duplicate completion must fail.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx2, eid, "complete_duplicate", "owner"); err == nil {
		_ = tx2.Rollback()
		t.Fatal("expected error on duplicate complete release")
	}
	_ = tx2.Rollback()
	assertExactlyOneRelease(t, db, eid)
}

func TestReleaseReservationTxDuplicateCancel(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	// Cancel.
	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx1, eid, "cancelled", "owner"); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}

	// Duplicate cancel must fail.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	if err := ReleaseReservationTx(ctx, tx2, eid, "cancelled_duplicate", "owner"); err == nil {
		t.Fatal("expected error on duplicate cancel release")
	}
}

func TestReleaseReservationTxDuplicateRecovery(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	// Recovery release.
	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx1, eid, "expired_recovery", "recovery"); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}

	// Duplicate recovery must fail gracefully.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	if err := ReleaseReservationTx(ctx, tx2, eid, "recovery_duplicate", "recovery-2"); err == nil {
		t.Fatal("expected error on duplicate recovery release")
	}
}

// ---------------------------------------------------------------------------
// Stale owner cannot free successor's reservation
// ---------------------------------------------------------------------------

func TestReleaseReservationTxStaleOwnerCannotFreeSuccessorReservation(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()

	// Original owner creates reservation, completes, releases.
	origEID := seedReservation(t, db)
	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx1, origEID, "complete", "owner-1"); err != nil {
		t.Fatalf("release original: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}

	// Successor creates new reservation.
	succEID := seedReservation(t, db)

	// Stale attempt to release original execution_id again must fail.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx2, origEID, "stale_release", "owner-stale"); err == nil {
		_ = tx2.Rollback()
		t.Fatal("expected error on stale release")
	}
	_ = tx2.Rollback()

	// Successor's reservation must still be active.
	assertReservationActive(t, db, succEID)
}

// ---------------------------------------------------------------------------
// Crash before queue transition (reservation not yet released)
// ---------------------------------------------------------------------------

func TestReleaseReservationTxCrashBeforeRelease(t *testing.T) {
	db := openReleaseTestDB(t)
	eid := seedReservation(t, db)

	// Simulate crash before release: reservation stays active, recovery
	// must release it.
	assertReservationActive(t, db, eid)

	// Recovery releases.
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "crash_recovery", "recovery"); err != nil {
		t.Fatalf("recovery release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
}

// ---------------------------------------------------------------------------
// Crash after queue transition (reservation was already released)
// ---------------------------------------------------------------------------

func TestReleaseReservationTxCrashAfterRelease(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	// Release + crash: release committed but process died.
	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx1, eid, "complete", "owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}

	assertReservationReleased(t, db, eid)

	// Recovery attempts re-release: must fail (idempotent).
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	if err := ReleaseReservationTx(ctx, tx2, eid, "crash_recovery", "recovery"); err == nil {
		t.Fatal("expected error: reservation already released after crash")
	}
}

// ---------------------------------------------------------------------------
// Crash during release CAS (race between two release attempts)
// ---------------------------------------------------------------------------

func TestReleaseReservationTxCrashDuringReleaseCAS(t *testing.T) {
	db := openReleaseTestDB(t)
	eid := seedReservation(t, db)

	// Two concurrent release attempts: only one wins.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				results <- fmt.Errorf("begin tx: %w", err)
				return
			}
			committed := false
			defer func() {
				if !committed {
					_ = tx.Rollback()
				}
			}()
			if err := ReleaseReservationTx(context.Background(), tx, eid, "cas_race", actor); err != nil {
				results <- err
				return
			}
			if err := tx.Commit(); err != nil {
				results <- err
				return
			}
			committed = true
			results <- nil
		}(fmt.Sprintf("actor-%d", i))
	}
	wg.Wait()
	close(results)

	successes := 0
	failures := 0
	for err := range results {
		if err != nil {
			failures++
		} else {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one success expected, got %d successes, %d failures", successes, failures)
	}
	if failures != 1 {
		t.Fatalf("exactly one failure expected, got %d successes, %d failures", successes, failures)
	}

	assertExactlyOneRelease(t, db, eid)
}

// ---------------------------------------------------------------------------
// Late-worker cleanup is no-op after first release
// ---------------------------------------------------------------------------

func TestReleaseReservationTxLateWorkerCleanupNoop(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	// Normal release.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx, eid, "complete", "owner"); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Late worker tries to release with shutdown kind.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx2, eid, "shutdown_cleanup", "late-worker"); err == nil {
		_ = tx2.Rollback()
		t.Fatal("expected error on late worker cleanup release")
	}
	_ = tx2.Rollback()

	assertExactlyOneRelease(t, db, eid)
}

// ---------------------------------------------------------------------------
// No negative usage: reservation count never goes below zero
// ---------------------------------------------------------------------------

func TestReleaseReservationTxNoNegativeUsage(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()

	// Create multiple reservations.
	eids := make([]string, 3)
	for i := 0; i < 3; i++ {
		eids[i] = seedReservation(t, db)
	}

	// Count active before.
	var activeBefore int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scheduler_reservation WHERE status='active'`,
	).Scan(&activeBefore); err != nil {
		t.Fatal(err)
	}
	if activeBefore != 3 {
		t.Fatalf("active count before=%d want 3", activeBefore)
	}

	// Release all.
	for _, eid := range eids {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := ReleaseReservationTx(ctx, tx, eid, "done", "owner"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("release: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	// Active must be 0, released must be 3.
	var released int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scheduler_reservation WHERE status='released'`,
	).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if released != 3 {
		t.Fatalf("released count=%d want 3", released)
	}

	// Re-releasing any must fail.
	for _, eid := range eids {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := ReleaseReservationTx(ctx, tx, eid, "negative_test", "bad"); err == nil {
			_ = tx.Rollback()
			t.Fatal("expected error: cannot re-release")
		}
		_ = tx.Rollback()
	}
}

// ---------------------------------------------------------------------------
// Unresponsive executor scenario (reservation stays until fenced)
// ---------------------------------------------------------------------------

func TestReleaseReservationTxUnresponsiveExecutorHoldsReservation(t *testing.T) {
	db := openReleaseTestDB(t)
	eid := seedReservation(t, db)

	// Reservation is active while executor is unresponsive.
	assertReservationActive(t, db, eid)

	// Fence: release reservation.
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "fenced_unresponsive", "fence"); err != nil {
		t.Fatalf("fence release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, eid)
}

// ---------------------------------------------------------------------------
// Lease-renewal loss
// ---------------------------------------------------------------------------

func TestReleaseReservationTxLeaseRenewalLoss(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()

	// Create reservation with short lease.
	shortEID := "test-owner/" + fmt.Sprintf("short-%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx,
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,'poster',1,1,'active',datetime(CURRENT_TIMESTAMP,'+1 seconds'))`,
		shortEID); err != nil {
		t.Fatal(err)
	}

	// Simulate lease loss: release.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, shortEID, "lease_renewal_loss", "recovery"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, shortEID)
}

// ---------------------------------------------------------------------------
// GetReservationTx
// ---------------------------------------------------------------------------

func TestGetReservationTx(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	r, err := GetReservationTx(ctx, tx, eid)
	if err != nil {
		t.Fatalf("get reservation: %v", err)
	}
	if r.ExecutionID != eid {
		t.Fatalf("execution_id=%q want %q", r.ExecutionID, eid)
	}
	if r.Status != "active" {
		t.Fatalf("status=%q want active", r.Status)
	}
	if r.ReleasedAt != nil {
		t.Fatal("released_at should be nil for active reservation")
	}
}

func TestGetReservationTxNotFound(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = GetReservationTx(ctx, tx, "nonexistent-eid")
	if err == nil {
		t.Fatal("expected error for nonexistent reservation")
	}
}

// ---------------------------------------------------------------------------
// Reservation audit evidence
// ---------------------------------------------------------------------------

func TestReleaseReservationTxEvidence(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, "test_evidence", "actor-test"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	var reason string
	var releasedBy string
	if err := db.QueryRowContext(ctx,
		`SELECT release_reason, released_by FROM scheduler_reservation WHERE execution_id=?`, eid,
	).Scan(&reason, &releasedBy); err != nil {
		t.Fatalf("query: %v", err)
	}
	if reason != "test_evidence" {
		t.Fatalf("release_reason=%q want test_evidence", reason)
	}
	if releasedBy != "actor-test" {
		t.Fatalf("released_by=%q want actor-test", releasedBy)
	}
}

// ---------------------------------------------------------------------------
// Type consolidation: ensure GetReservationTx works after release
// ---------------------------------------------------------------------------

func TestGetReservationTxAfterRelease(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseReservationTx(ctx, tx, eid, "done", "owner"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()

	r, err := GetReservationTx(ctx, tx2, eid)
	if err != nil {
		t.Fatalf("get after release: %v", err)
	}
	if r.Status != "released" {
		t.Fatalf("status=%q want released", r.Status)
	}
	if r.ReleasedAt == nil {
		t.Fatal("released_at is nil for released reservation")
	}
}

// ---------------------------------------------------------------------------
// ActiveReservationCountTx
// ---------------------------------------------------------------------------

func TestActiveReservationCountTx(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()

	// Seed with different task types.
	for i := 0; i < 3; i++ {
		s := fmt.Sprintf("cr-%d", i)
		_ = seedReservation(t, db) // poster type
		_ = s
	}
	// Add one thumbnail.
	tEID := "test-owner/" + fmt.Sprintf("thumb-%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx,
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,'thumbnail',1,1,'active',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`,
		tEID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	count, err := ActiveReservationCountTx(ctx, tx, "poster")
	if err != nil {
		t.Fatalf("count poster: %v", err)
	}
	if count < 3 {
		t.Fatalf("poster count=%d want at least 3", count)
	}
}

// ---------------------------------------------------------------------------
// Reservation identity JSON audit
// ---------------------------------------------------------------------------

func TestReleaseReservationTxIdentityAudit(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()

	eid := "audit-owner/" + fmt.Sprintf("audit-%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx,
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,'poster',2,1,'active',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`,
		eid); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	auditDetail := map[string]interface{}{
		"execution_id": eid,
		"reason":       "audit_test",
		"units":        2,
	}
	detailJSON, err := json.Marshal(auditDetail)
	if err != nil {
		t.Fatal(err)
	}

	// Record audit in same transaction as release.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO scheduler_audit(event_type,actor,detail_json) VALUES(?,?,?)`,
		"reservation_released", "test-audit", string(detailJSON),
	); err != nil {
		t.Fatalf("insert audit: %v", err)
	}

	if err := ReleaseReservationTx(ctx, tx, eid, "audit_test", "test-audit"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	// Verify audit entry exists.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scheduler_audit WHERE event_type='reservation_released' AND actor='test-audit'`,
	).Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit count=%d want 1", count)
	}

	// Verify reservation is released.
	assertReservationReleased(t, db, eid)
}

// ---------------------------------------------------------------------------
// Bulk recovery with mixed released/active reservations
// ---------------------------------------------------------------------------

func TestReleaseReservationTxMixedRecovery(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()

	// Active reservation.
	activeEID := seedReservation(t, db)

	// Already-released reservation.
	releasedEID := fmt.Sprintf("test-owner/released-%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx,
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until,released_at,release_reason,released_by) VALUES(?,'poster',1,1,'released',datetime(CURRENT_TIMESTAMP,'+90 seconds'),CURRENT_TIMESTAMP,'already_done','prior-release')`,
		releasedEID); err != nil {
		t.Fatal(err)
	}

	// Recover active: must succeed.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, activeEID, "recovery", "recovery"); err != nil {
		t.Fatalf("release active: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	assertReservationReleased(t, db, activeEID)

	// Recover already-released: must fail.
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	if err := ReleaseReservationTx(ctx, tx2, releasedEID, "recovery", "recovery"); err == nil {
		t.Fatal("expected error releasing already-released reservation")
	}
}

// ---------------------------------------------------------------------------
// Release reason/payload length stress
// ---------------------------------------------------------------------------

func TestReleaseReservationTxReasonLength(t *testing.T) {
	db := openReleaseTestDB(t)
	ctx := context.Background()
	eid := seedReservation(t, db)

	longReason := strings.Repeat("x", 500)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := ReleaseReservationTx(ctx, tx, eid, longReason, "long-actor"); err != nil {
		t.Fatalf("release with long reason: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true

	var reason string
	if err := db.QueryRowContext(ctx,
		`SELECT release_reason FROM scheduler_reservation WHERE execution_id=?`, eid,
	).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != longReason {
		t.Fatal("release_reason mismatch after long-string release")
	}
}
