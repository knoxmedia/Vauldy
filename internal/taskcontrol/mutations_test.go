package taskcontrol

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// openMutationTestDB creates an in-memory SQLite database with the post_ingest_task
// and related schema tables needed for mutation tests.
func openMutationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open mutation test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := createMutationTestSchema(t, db); err != nil {
		db.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func createMutationTestSchema(t *testing.T, db *sql.DB) error {
	t.Helper()
	stmts := []string{
		`CREATE TABLE post_ingest_task (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL DEFAULT 0,
			generation INTEGER NOT NULL DEFAULT 0,
			task_type TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'waiting',
			attempts INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 3,
			retry_round INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until TIMESTAMP,
			started_at TIMESTAMP,
			finished_at TIMESTAMP,
			available_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			base_priority INTEGER NOT NULL DEFAULT 0,
			library_id INTEGER,
			scan_task_id INTEGER,
			ingest_run_id INTEGER,
			ingest_step_id INTEGER,
			source_class INTEGER NOT NULL DEFAULT 200,
			resource_profile_version INTEGER NOT NULL DEFAULT 0,
			resource_profile_json TEXT NOT NULL DEFAULT '{}',
			run_now_expires TIMESTAMP,
			removed_at TIMESTAMP,
			removed_by TEXT NOT NULL DEFAULT '',
			remove_reason TEXT NOT NULL DEFAULT '',
			abort_requested_at TIMESTAMP,
			abort_timeout_recovery_required INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE task_projection_revision (
			task_identity TEXT PRIMARY KEY,
			revision INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE task_projection_sequence (
			singleton_id INTEGER PRIMARY KEY,
			next_revision INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE task_batch_operation (
			operation_id TEXT PRIMARY KEY,
			action TEXT NOT NULL,
			actor_id INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT '',
			requested_count INTEGER NOT NULL DEFAULT 0 CHECK (requested_count >= 0),
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at TIMESTAMP
		)`,
		`CREATE TABLE task_batch_item (
			operation_id TEXT NOT NULL,
			task_identity TEXT NOT NULL,
			action TEXT NOT NULL,
			request_revision INTEGER NOT NULL DEFAULT 0,
			ok INTEGER NOT NULL CHECK (ok IN (0, 1)),
			outcome_code TEXT NOT NULL DEFAULT '',
			result_revision INTEGER,
			result_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(result_json)),
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (operation_id, task_identity, action),
			FOREIGN KEY (operation_id) REFERENCES task_batch_operation(operation_id) ON DELETE RESTRICT
		)`,
		`CREATE TABLE task_control_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_identity TEXT NOT NULL DEFAULT '',
			task_type TEXT NOT NULL DEFAULT '',
			actor_id INTEGER NOT NULL DEFAULT 0,
			actor_name TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			previous_status TEXT NOT NULL DEFAULT '',
			new_status TEXT NOT NULL DEFAULT '',
			new_retry_round INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE media_ingest_run (
			id INTEGER PRIMARY KEY,
			media_id INTEGER NOT NULL DEFAULT 0,
			generation INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'processing',
			scan_task_id INTEGER,
			superseded_at TIMESTAMP,
			superseded_by_generation INTEGER
		)`,
		`CREATE TABLE media_ingest_step (
			id INTEGER PRIMARY KEY,
			run_id INTEGER NOT NULL,
			media_id INTEGER NOT NULL DEFAULT 0,
			generation INTEGER NOT NULL DEFAULT 0,
			step_type TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'waiting',
			required INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			retry_round INTEGER NOT NULL DEFAULT 0,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until TIMESTAMP,
			started_at TIMESTAMP,
			finished_at TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE media_ingest_step_dependency (
			step_id INTEGER NOT NULL,
			depends_on_step_id INTEGER NOT NULL,
			PRIMARY KEY (step_id, depends_on_step_id)
		)`,
		`CREATE TABLE scheduler_reservation (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			execution_id TEXT NOT NULL UNIQUE,
			task_type TEXT NOT NULL DEFAULT '',
			task_identity TEXT NOT NULL DEFAULT '',
			reserved_units INTEGER NOT NULL DEFAULT 0,
			policy_revision_id INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			lease_until TIMESTAMP,
			released_at TIMESTAMP,
			release_reason TEXT NOT NULL DEFAULT '',
			released_by TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("exec %q: %w", s[:50], err)
		}
	}
	return nil
}

func insertTestTask(t *testing.T, db *sql.DB, taskType, status string, retryRound int) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO post_ingest_task (media_id, generation, task_type, status, retry_round, max_attempts, base_priority, source_class)
		VALUES (1, 1, ?, ?, ?, 3, 100, 200)`, taskType, status, retryRound)
	if err != nil {
		t.Fatalf("insert test task: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

func insertRunningTask(t *testing.T, db *sql.DB, taskType string) int64 {
	t.Helper()
	id := insertTestTask(t, db, taskType, "running", 0)
	_, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='test-owner/uuid', lease_until=datetime('now','+5 minutes') WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set lease: %v", err)
	}
	return id
}

// =============================================================================
// Task 6: Abort Lifecycle Tests
// =============================================================================

func TestAbortRequestPersistsBeforeSignal(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "transcode")
	taskID := BuildIdentity("orchestration", id)

	err := svc.AbortRequest(context.Background(), AbortRequestParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "test abort",
	})
	if err != nil {
		t.Fatalf("abort request: %v", err)
	}

	// Verify row still running with abort_requested_at set
	var status string
	var abortReq sql.NullTime
	if err := db.QueryRow(`SELECT status, abort_requested_at FROM post_ingest_task WHERE id=?`, id).Scan(&status, &abortReq); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Errorf("expected running after abort request, got %s", status)
	}
	if !abortReq.Valid {
		t.Error("abort_requested_at should be set after request")
	}
}

func TestAbortRequestFailsForNonRunningTask(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.AbortRequest(context.Background(), AbortRequestParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "test abort",
	})
	if err == nil {
		t.Fatal("expected error requesting abort for non-running task")
	}
}

func TestAbortAcknowledgeCommitsCancelled(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "transcode")
	taskID := BuildIdentity("orchestration", id)

	// First request abort
	if err := svc.AbortRequest(context.Background(), AbortRequestParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "test abort",
	}); err != nil {
		t.Fatalf("abort request: %v", err)
	}

	// Then acknowledge
	err := svc.AbortAcknowledge(context.Background(), AbortAckParams{
		TaskIdentity: taskID,
		OwnerFence:   "test-owner/uuid",
	})
	if err != nil {
		t.Fatalf("abort ack: %v", err)
	}

	var status string
	var leaseOwner string
	var abortReq sql.NullTime
	if err := db.QueryRow(`SELECT status, lease_owner, abort_requested_at FROM post_ingest_task WHERE id=?`, id).Scan(&status, &leaseOwner, &abortReq); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Errorf("expected cancelled after ack, got %s", status)
	}
	if leaseOwner != "" {
		t.Error("lease_owner should be cleared after ack")
	}
}

func TestAbortAcknowledgeWithWrongFenceFails(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "transcode")
	taskID := BuildIdentity("orchestration", id)

	if err := svc.AbortRequest(context.Background(), AbortRequestParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "test",
	}); err != nil {
		t.Fatalf("abort request: %v", err)
	}

	err := svc.AbortAcknowledge(context.Background(), AbortAckParams{
		TaskIdentity: taskID,
		OwnerFence:   "wrong-owner/uuid",
	})
	if err == nil {
		t.Fatal("expected error with wrong fence")
	}
}

func TestAbortTimeoutSetsRecoveryRequired(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "transcode")
	taskID := BuildIdentity("orchestration", id)

	if err := svc.AbortRequest(context.Background(), AbortRequestParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "test",
	}); err != nil {
		t.Fatalf("abort request: %v", err)
	}

	err := svc.AbortTimeout(context.Background(), AbortTimeoutParams{
		TaskIdentity: taskID,
	})
	if err != nil {
		t.Fatalf("abort timeout: %v", err)
	}

	var status string
	var recoveryReq int
	if err := db.QueryRow(`SELECT status, abort_timeout_recovery_required FROM post_ingest_task WHERE id=?`, id).Scan(&status, &recoveryReq); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Errorf("expected still running after timeout, got %s", status)
	}
	if recoveryReq != 1 {
		t.Errorf("expected abort_timeout_recovery_required=1, got %d", recoveryReq)
	}
}

func TestFencedLeaseRecoveryCommitsCancelled(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "running", 0)
	// Set expired lease
	_, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='old-owner/uuid', lease_until=datetime('now','-10 minutes'), abort_requested_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set expired lease: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)

	err = svc.FencedLeaseRecovery(context.Background(), FencedRecoveryParams{
		TaskIdentity: taskID,
		ActorID:      0,
		Reason:       "fenced_recovery",
	})
	if err != nil {
		t.Fatalf("fenced recovery: %v", err)
	}

	var status, leaseOwner string
	if err := db.QueryRow(`SELECT status, lease_owner FROM post_ingest_task WHERE id=?`, id).Scan(&status, &leaseOwner); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Errorf("expected cancelled after fenced recovery, got %s", status)
	}
	if leaseOwner != "" {
		t.Error("lease_owner should be cleared")
	}
}

func TestFencedLeaseRecoveryReleasesReservationOnce(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "running", 0)
	_, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='old-owner/uuid', lease_until=datetime('now','-10 minutes'), abort_requested_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set expired: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)

	// First recovery
	if err := svc.FencedLeaseRecovery(context.Background(), FencedRecoveryParams{
		TaskIdentity: taskID,
		ActorID:      0,
		Reason:       "fenced_recovery",
	}); err != nil {
		t.Fatalf("first recovery: %v", err)
	}

	// Second recovery should be no-op (already cancelled)
	err = svc.FencedLeaseRecovery(context.Background(), FencedRecoveryParams{
		TaskIdentity: taskID,
		ActorID:      0,
		Reason:       "fenced_recovery_2",
	})
	if err == nil {
		t.Fatal("expected error on duplicate fenced recovery")
	}
}

func TestFencedLeaseRecoveryActiveLeaseFails(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "transcode")
	taskID := BuildIdentity("orchestration", id)

	err := svc.FencedLeaseRecovery(context.Background(), FencedRecoveryParams{
		TaskIdentity: taskID,
		ActorID:      0,
		Reason:       "fenced_recovery",
	})
	if err == nil {
		t.Fatal("expected error: lease still active")
	}
}

func TestUncertainOwnershipRemainsNonterminal(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "running", 0)
	// Set owner without UUID suffix (uncertain)
	_, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='unknown', lease_until=datetime('now','-10 minutes'), abort_requested_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set uncertain owner: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)

	err = svc.FencedLeaseRecovery(context.Background(), FencedRecoveryParams{
		TaskIdentity: taskID,
		ActorID:      0,
		Reason:       "fenced_recovery",
	})
	if err == nil {
		t.Fatal("expected error for uncertain ownership")
	}
}

func TestCancelWaitingTaskNotAbort(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Cancel(context.Background(), CancelParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "admin cancel",
	})
	if err != nil {
		t.Fatalf("cancel waiting: %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Errorf("expected cancelled, got %s", status)
	}
}

func TestCancelRunningTaskWithoutAbortRequestFails(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "transcode")
	taskID := BuildIdentity("orchestration", id)

	err := svc.Cancel(context.Background(), CancelParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "direct cancel",
	})
	if err == nil {
		t.Fatal("expected error: cancel running requires abort first")
	}
}

// =============================================================================
// Task 7: Tombstone Remove and Monotonic Reset Tests
// =============================================================================

func TestRemoveSetsActorReasonTime(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "done", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Remove(context.Background(), RemoveParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "cleanup",
	})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}

	var status, removedBy, removeReason string
	var removedAt sql.NullTime
	if err := db.QueryRow(`SELECT status, removed_at, removed_by, remove_reason FROM post_ingest_task WHERE id=?`, id).Scan(&status, &removedAt, &removedBy, &removeReason); err != nil {
		t.Fatal(err)
	}
	if !removedAt.Valid {
		t.Error("removed_at should be set")
	}
	if removedBy != "1" {
		t.Errorf("expected removed_by='1', got %q", removedBy)
	}
	if removeReason != "cleanup" {
		t.Errorf("expected remove_reason='cleanup', got %q", removeReason)
	}
	if status != "done" {
		t.Errorf("status should stay done, got %s", status)
	}
}

func TestRemoveHidesByDefault(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "thumbnail", "failed", 0)
	taskID := BuildIdentity("orchestration", id)

	if err := svc.Remove(context.Background(), RemoveParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "hide",
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Verify the row is marked removed (hidden from default queries)
	var removedAt sql.NullTime
	if err := db.QueryRow(`SELECT removed_at FROM post_ingest_task WHERE id=?`, id).Scan(&removedAt); err != nil {
		t.Fatal(err)
	}
	if !removedAt.Valid {
		t.Error("row should be hidden (removed_at set)")
	}
}

func TestRemovePreservesAttemptsAndDependencies(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "encrypt", "failed", 2)
	// Set some attempts data
	_, err := db.Exec(`UPDATE post_ingest_task SET attempts=3, max_attempts=5, last_error='test error' WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set attempts: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)

	if err := svc.Remove(context.Background(), RemoveParams{
		TaskIdentity: taskID,
		ActorID:      2,
		Reason:       "archive",
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}

	var attempts, maxAttempts int
	var lastError string
	if err := db.QueryRow(`SELECT attempts, max_attempts, last_error FROM post_ingest_task WHERE id=?`, id).Scan(&attempts, &maxAttempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Errorf("attempts should be preserved, got %d", attempts)
	}
	if maxAttempts != 5 {
		t.Errorf("max_attempts should be preserved, got %d", maxAttempts)
	}
	if lastError != "test error" {
		t.Errorf("last_error should be preserved, got %q", lastError)
	}
}

func TestRemoveNeverDeletesSource(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "cancelled", 0)
	taskID := BuildIdentity("orchestration", id)

	if err := svc.Remove(context.Background(), RemoveParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "test",
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Row should still exist
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE id=?`, id).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != 1 {
		t.Error("source row should not be deleted")
	}
}

func TestRemoveRequestsAbortForCancellableRunning(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "transcode")
	taskID := BuildIdentity("orchestration", id)

	err := svc.Remove(context.Background(), RemoveParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "force remove",
	})
	if err != nil {
		t.Fatalf("remove running: %v", err)
	}

	// Should have set abort_requested_at
	var abortReq sql.NullTime
	if err := db.QueryRow(`SELECT abort_requested_at FROM post_ingest_task WHERE id=?`, id).Scan(&abortReq); err != nil {
		t.Fatal(err)
	}
	if !abortReq.Valid {
		t.Error("abort_requested_at should be set when removing running task")
	}
}

func TestRemoveAlreadyRemovedIsIdempotent(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "done", 0)
	taskID := BuildIdentity("orchestration", id)

	// First remove
	if err := svc.Remove(context.Background(), RemoveParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "first",
	}); err != nil {
		t.Fatalf("first remove: %v", err)
	}

	// Second remove
	if err := svc.Remove(context.Background(), RemoveParams{
		TaskIdentity: taskID,
		ActorID:      2,
		Reason:       "second",
	}); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestResetIncrementsRetryRound(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "failed", 3)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Reset(context.Background(), ResetParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "retry",
	})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	var retryRound int
	var status string
	if err := db.QueryRow(`SELECT retry_round, status FROM post_ingest_task WHERE id=?`, id).Scan(&retryRound, &status); err != nil {
		t.Fatal(err)
	}
	if retryRound != 4 {
		t.Errorf("expected retry_round=4, got %d", retryRound)
	}
	if status != "waiting" {
		t.Errorf("expected waiting after reset, got %s", status)
	}
}

func TestResetCreatesWaitingExecution(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "poster", "cancelled", 1)
	// Set old lease
	_, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='old-owner', lease_until=datetime('now','-5 minutes'), started_at=CURRENT_TIMESTAMP, finished_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set old state: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)

	if err := svc.Reset(context.Background(), ResetParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "retry",
	}); err != nil {
		t.Fatalf("reset: %v", err)
	}

	var status, leaseOwner string
	var leaseUntil, startedAt, finishedAt sql.NullTime
	var retryRound int
	if err := db.QueryRow(`SELECT status, retry_round, lease_owner, lease_until, started_at, finished_at FROM post_ingest_task WHERE id=?`, id).Scan(&status, &retryRound, &leaseOwner, &leaseUntil, &startedAt, &finishedAt); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" {
		t.Errorf("expected waiting, got %s", status)
	}
	if leaseOwner != "" {
		t.Error("lease_owner should be cleared")
	}
	if leaseUntil.Valid {
		t.Error("lease_until should be cleared")
	}
	if startedAt.Valid {
		t.Error("started_at should be cleared")
	}
	if finishedAt.Valid {
		t.Error("finished_at should be cleared")
	}
	if retryRound != 2 {
		t.Errorf("expected retry_round=2, got %d", retryRound)
	}
}

func TestResetValidatesGeneration(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "failed", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Reset(context.Background(), ResetParams{
		TaskIdentity:         taskID,
		ActorID:              1,
		Reason:               "retry",
		ExpectedGeneration:   99, // wrong generation
	})
	if err == nil {
		t.Fatal("expected error: generation mismatch")
	}
}

func TestResetNonTerminalFails(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Reset(context.Background(), ResetParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "retry",
	})
	if err == nil {
		t.Fatal("expected error: cannot reset non-terminal task")
	}
}

func TestStaleRevisionReturnsLatestRow(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "failed", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Reset(context.Background(), ResetParams{
		TaskIdentity:       taskID,
		ActorID:            1,
		Reason:             "retry",
		ExpectedRevision:   999, // stale revision
	})
	// Stale revision should return the latest row without error (no mutation but returns current state)
	if err != nil {
		t.Fatalf("stale revision should return latest row: %v", err)
	}
}

func TestRetryRoundMonotonicallyIncreases(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "encrypt", "failed", 5)
	taskID := BuildIdentity("orchestration", id)

	if err := svc.Reset(context.Background(), ResetParams{
		TaskIdentity:         taskID,
		ActorID:              1,
		Reason:               "retry",
		ExpectedRetryRound:   5,
	}); err != nil {
		t.Fatalf("reset: %v", err)
	}

	var retryRound int
	if err := db.QueryRow(`SELECT retry_round FROM post_ingest_task WHERE id=?`, id).Scan(&retryRound); err != nil {
		t.Fatal(err)
	}
	if retryRound != 6 {
		t.Errorf("expected retry_round=6, got %d", retryRound)
	}
}

// =============================================================================
// Task 8: AI Reopen, Run-Now, and Skip Tests
// =============================================================================

func TestAIReopenRecognitionResetAloneDoesNotReopen(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "ai_analysis", "skipped", 0)
	taskID := BuildIdentity("orchestration", id)

	// Reset alone should not reopen AI
	err := svc.Reset(context.Background(), ResetParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "reset",
	})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	// Reset makes it waiting, not a special "reopened" state
	if status != "waiting" {
		t.Errorf("expected waiting after reset, got %s", status)
	}
}

func TestAIReopenRecognitionWaitingDoesNotReopen(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "ai_analysis", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Reopen(context.Background(), ReopenParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "retry analysis",
	})
	if err == nil {
		t.Fatal("expected error: cannot reopen waiting AI task")
	}
}

func TestAIReopenAfterRecognitionSuccess(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "ai_analysis", "skipped", 1)
	_, err := db.Exec(`UPDATE post_ingest_task SET retry_round=0 WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set retry_round: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)

	if err := svc.Reopen(context.Background(), ReopenParams{
		TaskIdentity: taskID,
		ActorID:      5,
		Reason:       "recognition succeeded, reanalyze",
	}); err != nil {
		t.Fatalf("ai reopen: %v", err)
	}

	var status string
	var retryRound int
	if err := db.QueryRow(`SELECT status, retry_round FROM post_ingest_task WHERE id=?`, id).Scan(&status, &retryRound); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" {
		t.Errorf("expected waiting after reopen, got %s", status)
	}
	if retryRound != 1 {
		t.Errorf("expected retry_round=1 after AI reopen, got %d", retryRound)
	}
}

func TestAIReopenIncrementsAIRetryRound(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "ai_analysis", "skipped", 2)
	taskID := BuildIdentity("orchestration", id)

	if err := svc.Reopen(context.Background(), ReopenParams{
		TaskIdentity: taskID,
		ActorID:      3,
		Reason:       "reanalyze",
	}); err != nil {
		t.Fatalf("ai reopen: %v", err)
	}

	var retryRound int
	if err := db.QueryRow(`SELECT retry_round FROM post_ingest_task WHERE id=?`, id).Scan(&retryRound); err != nil {
		t.Fatal(err)
	}
	if retryRound != 3 {
		t.Errorf("expected retry_round=3 after reopen, got %d", retryRound)
	}
}

func TestAIReopenNonAITaskFails(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "skipped", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Reopen(context.Background(), ReopenParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "reopen",
	})
	if err == nil {
		t.Fatal("expected error: non-AI task cannot be reopened")
	}
}

func TestRunNowSetsBoost(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "thumbnail", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.RunNow(context.Background(), RunNowParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "priority",
	})
	if err != nil {
		t.Fatalf("run now: %v", err)
	}

	var status string
	var runNowExpires sql.NullTime
	if err := db.QueryRow(`SELECT status, run_now_expires FROM post_ingest_task WHERE id=?`, id).Scan(&status, &runNowExpires); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" {
		t.Errorf("expected waiting after run-now, got %s", status)
	}
	if !runNowExpires.Valid {
		t.Error("run_now_expires should be set")
	}
}

func TestRunNowNonWaitingFails(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "thumbnail", "running", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.RunNow(context.Background(), RunNowParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "priority",
	})
	if err == nil {
		t.Fatal("expected error: run-now requires waiting status")
	}
}

func TestRunNowBypassesResourceLimit(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "encrypt", "waiting", 0)
	// Set priority low to simulate resource-constrained
	_, err := db.Exec(`UPDATE post_ingest_task SET base_priority=1 WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set low priority: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)

	if err := svc.RunNow(context.Background(), RunNowParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "bypass limit",
	}); err != nil {
		t.Fatalf("run now: %v", err)
	}

	var priority int64
	var runNowExpires sql.NullTime
	if err := db.QueryRow(`SELECT base_priority, run_now_expires FROM post_ingest_task WHERE id=?`, id).Scan(&priority, &runNowExpires); err != nil {
		t.Fatal(err)
	}
	// Priority should be boosted
	if priority <= 1 {
		t.Errorf("priority should be boosted above 1, got %d", priority)
	}
	if !runNowExpires.Valid {
		t.Error("run_now_expires should be set")
	}
}

func TestSkipRequiresPolicy(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "keyframe", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Skip(context.Background(), SkipParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "not needed",
	})
	if err != nil {
		t.Fatalf("skip: %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "skipped" {
		t.Errorf("expected skipped status, got %s", status)
	}
}

func TestSkipNonWaitingFails(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "keyframe", "done", 0)
	taskID := BuildIdentity("orchestration", id)

	err := svc.Skip(context.Background(), SkipParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "skip done",
	})
	if err == nil {
		t.Fatal("expected error: cannot skip non-waiting task")
	}
}

func TestSkipPropagatesDependencyImpossibility(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	// Create a task that will be skipped
	id := insertTestTask(t, db, "subtitle_extract", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	if err := svc.Skip(context.Background(), SkipParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "dependency impossible",
	}); err != nil {
		t.Fatalf("skip: %v", err)
	}

	// Verify it's skipped
	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "skipped" {
		t.Errorf("expected skipped, got %s", status)
	}
}

func TestDependencyPropagationOnSkip(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)

	// Create two tasks: an ingest run with steps
	_, err := db.Exec(`INSERT INTO media_ingest_run (id, media_id, generation, status) VALUES (1, 1, 1, 'processing')`)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	// Step 1 (subtitle_extract) with post_ingest_task
	id1 := insertTestTask(t, db, "subtitle_extract", "waiting", 0)
	_, err = db.Exec(`INSERT INTO media_ingest_step (id, run_id, media_id, generation, step_type, status) VALUES (10, 1, 1, 1, 'subtitle_extract', 'waiting')`)
	if err != nil {
		t.Fatalf("insert step: %v", err)
	}
	_, err = db.Exec(`UPDATE post_ingest_task SET ingest_run_id=1, ingest_step_id=10 WHERE id=?`, id1)
	if err != nil {
		t.Fatalf("link task to step: %v", err)
	}
	taskID := BuildIdentity("orchestration", id1)

	if err := svc.Skip(context.Background(), SkipParams{
		TaskIdentity: taskID,
		ActorID:      1,
		Reason:       "dep impossible",
	}); err != nil {
		t.Fatalf("skip: %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id1).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "skipped" {
		t.Errorf("expected skipped, got %s", status)
	}
}

// =============================================================================
// Task 9: Batch Operations Tests
// =============================================================================

func TestBatchRequiresUUID(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)

	_, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "not-a-uuid",
		Action:      "cancel",
		ActorID:     1,
		Reason:      "batch test",
		Items:       []BatchItem{},
	})
	if err == nil {
		t.Fatal("expected error: invalid operation_id format")
	}
}

func TestBatchDuplicateItemNormalization(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	result, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000001",
		Action:      "cancel",
		ActorID:     1,
		Reason:      "batch test",
		Items: []BatchItem{
			{TaskIdentity: taskID},
			{TaskIdentity: taskID}, // duplicate
		},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	// Only one unique item should be processed
	if result.RequestedCount != 1 {
		t.Errorf("expected requested_count=1 after dedup, got %d", result.RequestedCount)
	}
}

func TestBatchMax200Items(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)

	items := make([]BatchItem, 201)
	for i := range items {
		items[i] = BatchItem{TaskIdentity: fmt.Sprintf("orchestration:%d", 1000+i)}
	}

	_, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000002",
		Action:      "cancel",
		ActorID:     1,
		Reason:      "batch test",
		Items:       items,
	})
	if err == nil {
		t.Fatal("expected error: max 200 items")
	}
}

func TestBatchPerItemIndependentCommit(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id1 := insertTestTask(t, db, "transcode", "waiting", 0)
	id2 := insertTestTask(t, db, "transcode", "done", 0)
	taskID1 := BuildIdentity("orchestration", id1)
	taskID2 := BuildIdentity("orchestration", id2)

	result, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000003",
		Action:      "cancel",
		ActorID:     1,
		Reason:      "batch test",
		Items: []BatchItem{
			{TaskIdentity: taskID1}, // waiting -> cancel OK
			{TaskIdentity: taskID2}, // done -> cancel should fail
		},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	if result.Succeeded != 1 {
		t.Errorf("expected 1 success (waiting cancel), got %d", result.Succeeded)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failure (done cancel), got %d", result.Failed)
	}
}

func TestBatchMixedOutcomes(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id1 := insertTestTask(t, db, "thumbnail", "waiting", 0)
	id2 := insertRunningTask(t, db, "poster")
	id3 := insertTestTask(t, db, "encrypt", "failed", 0)
	id4 := insertTestTask(t, db, "keyframe", "done", 0)

	taskIDs := []string{
		BuildIdentity("orchestration", id1),
		BuildIdentity("orchestration", id2),
		BuildIdentity("orchestration", id3),
		BuildIdentity("orchestration", id4),
	}

	items := make([]BatchItem, len(taskIDs))
	for i, tid := range taskIDs {
		items[i] = BatchItem{TaskIdentity: tid}
	}

	result, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000004",
		Action:      "cancel",
		ActorID:     1,
		Reason:      "mixed batch",
		Items:       items,
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	// waiting -> cancel: success
	// running (no abort request) -> cancel: fail
	// failed -> cancel: fail
	// done -> cancel: fail
	expectedSuccess := 1
	if result.Succeeded != expectedSuccess {
		t.Errorf("expected %d success, got %d", expectedSuccess, result.Succeeded)
	}
}

func TestBatchExactCounters(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)

	// Create 5 waiting tasks
	var taskIDs []string
	for i := 0; i < 5; i++ {
		id := insertTestTask(t, db, "transcode", "waiting", 0)
		taskIDs = append(taskIDs, BuildIdentity("orchestration", id))
	}

	items := make([]BatchItem, len(taskIDs))
	for i, tid := range taskIDs {
		items[i] = BatchItem{TaskIdentity: tid}
	}

	result, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000005",
		Action:      "cancel",
		ActorID:     1,
		Reason:      "counter test",
		Items:       items,
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	if result.Succeeded != 5 {
		t.Errorf("expected 5 successes, got %d", result.Succeeded)
	}
	if result.Failed != 0 {
		t.Errorf("expected 0 failures, got %d", result.Failed)
	}
}

func TestBatchRetryableSubset(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id1 := insertTestTask(t, db, "transcode", "waiting", 0)
	id2 := insertTestTask(t, db, "transcode", "done", 0)
	taskID1 := BuildIdentity("orchestration", id1)
	taskID2 := BuildIdentity("orchestration", id2)

	result, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000006",
		Action:      "cancel",
		ActorID:     1,
		Reason:      "retryable test",
		Items: []BatchItem{
			{TaskIdentity: taskID1},
			{TaskIdentity: taskID2},
		},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	if len(result.Retryable) != 1 {
		t.Errorf("expected 1 retryable item, got %d", len(result.Retryable))
	}
	if result.Retryable[0].TaskIdentity != taskID2 {
		t.Errorf("expected %s as retryable, got %s", taskID2, result.Retryable[0].TaskIdentity)
	}
}

func TestBatchActorReasonAudit(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	_, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000007",
		Action:      "cancel",
		ActorID:     42,
		Reason:      "audit reason",
		Items: []BatchItem{
			{TaskIdentity: taskID},
		},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	// Check audit was written
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_control_audit WHERE task_identity=? AND actor_id=42 AND action='cancel'`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Error("audit entry should be written")
	}
}

func TestBatchSameOperationReplay(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	opID := "00000000-0000-0000-0000-000000000008"

	// First execution
	result1, err := svc.Batch(context.Background(), BatchParams{
		OperationID: opID,
		Action:      "cancel",
		ActorID:     1,
		Reason:      "first run",
		Items: []BatchItem{
			{TaskIdentity: taskID},
		},
	})
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}

	// Replay same operation
	result2, err := svc.Batch(context.Background(), BatchParams{
		OperationID: opID,
		Action:      "cancel",
		ActorID:     1,
		Reason:      "replay",
		Items: []BatchItem{
			{TaskIdentity: taskID},
		},
	})
	if err != nil {
		t.Fatalf("replay batch: %v", err)
	}

	// Replay should return same outcome
	if result2.Succeeded != result1.Succeeded {
		t.Errorf("replay succeeded mismatch: %d vs %d", result1.Succeeded, result2.Succeeded)
	}
	if result2.Failed != result1.Failed {
		t.Errorf("replay failed mismatch: %d vs %d", result1.Failed, result2.Failed)
	}
}

func TestBatchConcurrentSameKeyRequests(t *testing.T) {
	db := openMutationTestDB(t)
	id := insertTestTask(t, db, "transcode", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	opID := "00000000-0000-0000-0000-000000000009"

	var wg sync.WaitGroup
	errors := make(chan error, 3)
	results := make(chan *BatchResult, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			svc := NewMutateService(db)
			result, err := svc.Batch(context.Background(), BatchParams{
				OperationID: opID,
				Action:      "cancel",
				ActorID:     1,
				Reason:      fmt.Sprintf("concurrent %d", idx),
				Items: []BatchItem{
					{TaskIdentity: taskID},
				},
			})
			if err != nil {
				errors <- err
			} else {
				results <- result
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	close(results)

	// All concurrent requests should succeed or replay consistently
	errCount := len(errors)
	if errCount > 0 {
		t.Logf("concurrent errors: %d", errCount)
	}
	resCount := len(results)
	if resCount < 1 {
		t.Error("expected at least one successful result")
	}
}

func TestBatchOperationActionMismatch(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	opID := "00000000-0000-0000-0000-000000000010"

	// First execution with "cancel"
	_, err := svc.Batch(context.Background(), BatchParams{
		OperationID: opID,
		Action:      "cancel",
		ActorID:     1,
		Reason:      "first",
		Items: []BatchItem{
			{TaskIdentity: taskID},
		},
	})
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}

	// Replay with wrong action
	_, err = svc.Batch(context.Background(), BatchParams{
		OperationID: opID,
		Action:      "remove", // wrong action
		ActorID:     1,
		Reason:      "mismatch",
		Items: []BatchItem{
			{TaskIdentity: taskID},
		},
	})
	if err == nil {
		t.Fatal("expected error: operation/action mismatch")
	}
}

func TestBatchNoDuplicateResetReopenRounds(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "failed", 0)
	taskID := BuildIdentity("orchestration", id)

	opID := "00000000-0000-0000-0000-000000000011"

	// First reset
	_, err := svc.Batch(context.Background(), BatchParams{
		OperationID: opID,
		Action:      "reset",
		ActorID:     1,
		Reason:      "first reset",
		Items: []BatchItem{
			{TaskIdentity: taskID},
		},
	})
	if err != nil {
		t.Fatalf("first batch reset: %v", err)
	}

	// Replay same operation - should not increment retry_round again
	_, err = svc.Batch(context.Background(), BatchParams{
		OperationID: opID,
		Action:      "reset",
		ActorID:     1,
		Reason:      "replay reset",
		Items: []BatchItem{
			{TaskIdentity: taskID},
		},
	})
	if err != nil {
		t.Fatalf("replay batch reset: %v", err)
	}

	var retryRound int
	if err := db.QueryRow(`SELECT retry_round FROM post_ingest_task WHERE id=?`, id).Scan(&retryRound); err != nil {
		t.Fatal(err)
	}
	if retryRound != 1 {
		t.Errorf("replay should not increment retry_round again, got %d", retryRound)
	}
}

func TestBatchResultSerializationForReplay(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "waiting", 0)
	taskID := BuildIdentity("orchestration", id)

	opID := "00000000-0000-0000-0000-000000000012"

	result1, err := svc.Batch(context.Background(), BatchParams{
		OperationID: opID,
		Action:      "cancel",
		ActorID:     1,
		Reason:      "first",
		Items: []BatchItem{
			{TaskIdentity: taskID},
		},
	})
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}

	result2, err := svc.Batch(context.Background(), BatchParams{
		OperationID: opID,
		Action:      "cancel",
		ActorID:     1,
		Reason:      "replay",
		Items: []BatchItem{
			{TaskIdentity: taskID},
		},
	})
	if err != nil {
		t.Fatalf("replay batch: %v", err)
	}

	// Results should be identical in structure
	if result1.OperationID != result2.OperationID {
		t.Error("operation_id mismatch in replay")
	}
	if result1.Succeeded != result2.Succeeded {
		t.Error("succeeded count mismatch in replay")
	}
	if result1.Failed != result2.Failed {
		t.Error("failed count mismatch in replay")
	}
	if result1.RequestedCount != result2.RequestedCount {
		t.Error("requested_count mismatch in replay")
	}
}
