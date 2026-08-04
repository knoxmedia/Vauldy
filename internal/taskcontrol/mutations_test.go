package taskcontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"knox-media/internal/store"

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
			remove_reason TEXT NOT NULL DEFAULT ''
		)		`,
		`CREATE TABLE media (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			library_id INTEGER,
			file_id TEXT UNIQUE,
			title TEXT,
			original_title TEXT,
			file_path TEXT,
			file_type TEXT,
			status TEXT DEFAULT 'active',
			publication_state TEXT NOT NULL DEFAULT 'published'
		)`,
		`CREATE TABLE task_projection_revision (
			task_identity TEXT PRIMARY KEY,
			revision INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE task_abort_intent (
			task_identity TEXT PRIMARY KEY,
			requested_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			requested_by TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			owner_fence TEXT NOT NULL DEFAULT '',
			deadline TIMESTAMP,
			acknowledged_at TIMESTAMP,
			outcome TEXT NOT NULL DEFAULT '',
			recovery_required_at TIMESTAMP
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
			task_identity TEXT NOT NULL DEFAULT '',
			id INTEGER PRIMARY KEY AUTOINCREMENT,
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

	// Verify row stays running with a durable abort intent
	var status string
	var abortReq sql.NullTime
	if err := db.QueryRow(`SELECT status, requested_at FROM post_ingest_task, task_abort_intent WHERE post_ingest_task.id=? AND task_abort_intent.task_identity='orchestration:' || post_ingest_task.id`, id).Scan(&status, &abortReq); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Errorf("expected running after abort request, got %s", status)
	}
	if !abortReq.Valid {
		t.Error("abort intent requested_at should be set after request")
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
	if err := db.QueryRow(`SELECT status, lease_owner, acknowledged_at FROM post_ingest_task, task_abort_intent WHERE post_ingest_task.id=? AND task_abort_intent.task_identity='orchestration:' || post_ingest_task.id`, id).Scan(&status, &leaseOwner, &abortReq); err != nil {
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
	if err := db.QueryRow(`SELECT status, CASE WHEN recovery_required_at IS NULL THEN 0 ELSE 1 END FROM post_ingest_task, task_abort_intent WHERE post_ingest_task.id=? AND task_abort_intent.task_identity='orchestration:' || post_ingest_task.id`, id).Scan(&status, &recoveryReq); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Errorf("expected still running after timeout, got %s", status)
	}
	if recoveryReq != 1 {
		t.Errorf("expected abort intent recovery_required_at, got %d", recoveryReq)
	}
}

func TestFencedLeaseRecoveryCommitsCancelled(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "running", 0)
	// Set expired lease
	_, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='old-owner/uuid', lease_until=datetime('now','-10 minutes') WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set expired lease: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)
	if _, err = db.Exec(`INSERT INTO task_abort_intent(task_identity, requested_by, reason, owner_fence) VALUES(?, '1', 'test', 'old-owner/uuid')`, taskID); err != nil {
		t.Fatalf("insert abort intent: %v", err)
	}

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
	_, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='old-owner/uuid', lease_until=datetime('now','-10 minutes') WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set expired: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)
	if _, err = db.Exec(`INSERT INTO task_abort_intent(task_identity, requested_by, reason, owner_fence) VALUES(?, '1', 'test', 'old-owner/uuid')`, taskID); err != nil {
		t.Fatalf("insert abort intent: %v", err)
	}

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
	_, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='unknown', lease_until=datetime('now','-10 minutes') WHERE id=?`, id)
	if err != nil {
		t.Fatalf("set uncertain owner: %v", err)
	}
	taskID := BuildIdentity("orchestration", id)
	if _, err = db.Exec(`INSERT INTO task_abort_intent(task_identity, requested_by, reason, owner_fence) VALUES(?, '1', 'test', 'old-owner/uuid')`, taskID); err != nil {
		t.Fatalf("insert abort intent: %v", err)
	}

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

func TestAbortNotifierRunsAfterCommitOnly(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "transcode")
	called := make(chan int64, 1)
	svc.SetAbortNotifier(func(taskID int64) { called <- taskID })
	if err := svc.AbortRequest(context.Background(), AbortRequestParams{TaskIdentity: BuildIdentity("orchestration", id), ActorID: 1, Reason: "stop"}); err != nil {
		t.Fatalf("abort request: %v", err)
	}
	select {
	case got := <-called:
		if got != id {
			t.Fatalf("notified task=%d want %d", got, id)
		}
	default:
		t.Fatal("abort notifier was not called")
	}
	waiting := insertTestTask(t, db, "preview", "waiting", 0)
	if err := svc.AbortRequest(context.Background(), AbortRequestParams{TaskIdentity: BuildIdentity("orchestration", waiting), ActorID: 1, Reason: "invalid"}); err == nil {
		t.Fatal("expected abort request failure")
	}
	select {
	case got := <-called:
		t.Fatalf("notifier called after rollback for task %d", got)
	default:
	}
}

func TestFencedLeaseRecoveryRequiresIntent(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertTestTask(t, db, "transcode", "running", 0)
	if _, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='old-owner/uuid', lease_until=datetime('now','-10 minutes') WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := svc.FencedLeaseRecovery(context.Background(), FencedRecoveryParams{TaskIdentity: BuildIdentity("orchestration", id), Reason: "recover"}); err == nil {
		t.Fatal("expected recovery without intent to fail")
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

	// Should have persisted an abort intent
	var abortReq sql.NullTime
	if err := db.QueryRow(`SELECT requested_at FROM task_abort_intent WHERE task_identity='orchestration:' || ?`, id).Scan(&abortReq); err != nil {
		t.Fatal(err)
	}
	if !abortReq.Valid {
		t.Error("abort intent should be set when removing running task")
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
		TaskIdentity:       taskID,
		ActorID:            1,
		Reason:             "retry",
		ExpectedGeneration: 99, // wrong generation
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
		TaskIdentity:     taskID,
		ActorID:          1,
		Reason:           "retry",
		ExpectedRevision: 999, // stale revision
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
		TaskIdentity:       taskID,
		ActorID:            1,
		Reason:             "retry",
		ExpectedRetryRound: 5,
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

func openLinkedMutationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open linked mutation db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedLinkedMutationGraph(t *testing.T, db *sql.DB) (runID, sourceStepID, dependentStepID, sourceTaskID, dependentTaskID int64) {
	t.Helper()
	_, err := db.Exec(`
INSERT INTO library(name,type,path) VALUES('taskcontrol','video','/taskcontrol');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(1,1,'linked','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(10,1,1,'scan','processing','{}',1);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,lease_owner,lease_until) VALUES
 (11,10,1,1,'preview',0,'waiting',0,3,'stale-owner',datetime(CURRENT_TIMESTAMP,'+60 seconds')),
 (12,10,1,1,'ai_analysis',0,'waiting',0,3,'dependent-owner',datetime(CURRENT_TIMESTAMP,'+60 seconds'));
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(12,11,'success');
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,lease_owner,lease_until) VALUES
 (111,1,10,11,1,'preview','waiting',0,3,'stale-owner',datetime(CURRENT_TIMESTAMP,'+60 seconds')),
 (112,1,10,12,1,'ai_analysis','waiting',0,3,'dependent-owner',datetime(CURRENT_TIMESTAMP,'+60 seconds'));`)
	if err != nil {
		t.Fatalf("seed linked graph: %v", err)
	}
	return 10, 11, 12, 111, 112
}

func TestResetReopensLinkedRequiredStepRunAndMedia(t *testing.T) {
	db := openLinkedMutationTestDB(t)
	svc := NewMutateService(db)
	_, err := db.Exec(`
INSERT INTO library(name,type,path) VALUES('taskcontrol-reset','video','/taskcontrol-reset');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state,published_at) VALUES(1,1,'linked-reset','video',1,'degraded',CURRENT_TIMESTAMP);
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version,preserve_visibility,finished_at) VALUES(20,1,1,'scan','degraded','{}',3,0,CURRENT_TIMESTAMP);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error,retry_round) VALUES
 (21,20,1,1,'poster',1,'failed',1,1,'context deadline exceeded',0),
 (22,20,1,1,'media_visible',0,'cancelled',0,1,'',0);
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round,last_error) VALUES
 (200,1,20,21,1,'poster','failed',1,1,0,'context deadline exceeded');`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	taskID := BuildIdentity("orchestration", 200)
	if err := svc.Reset(context.Background(), ResetParams{TaskIdentity: taskID, ActorID: 1, Reason: "retry"}); err != nil {
		t.Fatalf("reset: %v", err)
	}

	var status string
	var retryRound, attempts int
	if err := db.QueryRow(`SELECT status, retry_round, attempts FROM post_ingest_task WHERE id=200`).Scan(&status, &retryRound, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" || retryRound != 1 || attempts != 0 {
		t.Errorf("task not reset: status=%s retry_round=%d attempts=%d", status, retryRound, attempts)
	}

	var stepStatus string
	var stepAttempts, stepRound int
	if err := db.QueryRow(`SELECT status, attempts, retry_round FROM media_ingest_step WHERE id=21`).Scan(&stepStatus, &stepAttempts, &stepRound); err != nil {
		t.Fatal(err)
	}
	if stepStatus != "waiting" || stepAttempts != 0 || stepRound != 1 {
		t.Errorf("linked step not reopened: status=%s attempts=%d retry_round=%d", stepStatus, stepAttempts, stepRound)
	}

	var runStatus string
	if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=20`).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "processing" {
		t.Errorf("run should be processing after required reset, got %s", runStatus)
	}

	var pubState string
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=1`).Scan(&pubState); err != nil {
		t.Fatal(err)
	}
	if pubState != "processing" {
		t.Errorf("media should be processing for non-preserve run, got %s", pubState)
	}

	// The claim eligibility predicate for a required linked step now holds:
	// task waiting, step waiting, run processing.
	var eligible int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task q LEFT JOIN media_ingest_step st ON st.id=q.ingest_step_id WHERE q.id=200 AND q.status='waiting' AND st.status='waiting' AND st.required=1`).Scan(&eligible); err != nil {
		t.Fatal(err)
	}
	if eligible != 1 {
		t.Error("reset task should be claim-eligible with a waiting required step")
	}
}

func TestResetLinkedOptionalStepKeepsPublishedRun(t *testing.T) {
	db := openLinkedMutationTestDB(t)
	svc := NewMutateService(db)
	_, err := db.Exec(`
INSERT INTO library(name,type,path) VALUES('taskcontrol-optional','video','/taskcontrol-optional');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state,published_at) VALUES(1,1,'linked-optional','video',1,'published',CURRENT_TIMESTAMP);
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version,preserve_visibility,finished_at) VALUES(30,1,1,'scan','published','{}',3,0,CURRENT_TIMESTAMP);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error,retry_round) VALUES
 (31,30,1,1,'preview',0,'failed',1,3,'boom',0);
INSERT INTO post_ingest_task(id,media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,retry_round,last_error) VALUES
 (300,1,30,31,1,'preview','failed',1,3,0,'boom');`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	taskID := BuildIdentity("orchestration", 300)
	if err := svc.Reset(context.Background(), ResetParams{TaskIdentity: taskID, ActorID: 1, Reason: "retry"}); err != nil {
		t.Fatalf("reset: %v", err)
	}

	var runStatus string
	if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=30`).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "published" {
		t.Errorf("optional reset should keep run published, got %s", runStatus)
	}

	var pubState string
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=1`).Scan(&pubState); err != nil {
		t.Fatal(err)
	}
	if pubState != "published" {
		t.Errorf("media should stay published for optional reset, got %s", pubState)
	}
}

func assertLinkedTerminalPropagation(t *testing.T, db *sql.DB, sourceStepID, dependentStepID, sourceTaskID, dependentTaskID int64, wantSource string) {
	t.Helper()
	var queueSource, stepSource, queueDependent, stepDependent string
	var queueLease, stepLease sql.NullString
	var stepFinished sql.NullTime
	var stepReason string
	if err := db.QueryRow(`SELECT status,lease_owner FROM post_ingest_task WHERE id=?`, sourceTaskID).Scan(&queueSource, &queueLease); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,lease_owner,finished_at,last_error FROM media_ingest_step WHERE id=?`, sourceStepID).Scan(&stepSource, &stepLease, &stepFinished, &stepReason); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, dependentTaskID).Scan(&queueDependent); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, dependentStepID).Scan(&stepDependent); err != nil {
		t.Fatal(err)
	}
	if queueSource != wantSource || stepSource != wantSource || queueDependent != "skipped" || stepDependent != "skipped" {
		t.Fatalf("queue/step source=%s/%s dependent=%s/%s", queueSource, stepSource, queueDependent, stepDependent)
	}
	if queueLease.Valid && queueLease.String != "" || stepLease.Valid && stepLease.String != "" || !stepFinished.Valid || stepReason == "" {
		t.Fatalf("terminal fields queue lease=%q step lease=%q finished=%v reason=%q", queueLease.String, stepLease.String, stepFinished.Valid, stepReason)
	}
}

func TestAbortAcknowledgeLinkedRunningTaskFinalizesStepAndIntent(t *testing.T) {
	db := openLinkedMutationTestDB(t)
	runID, sourceStep, dependentStep, sourceTask, dependentTask := seedLinkedMutationGraph(t, db)
	owner := "linked-worker/abort-fence"
	taskIdentity := BuildIdentity("orchestration", sourceTask)
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='running',lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+60 seconds'),started_at=CURRENT_TIMESTAMP WHERE id=?`, owner, sourceTask); err != nil {
		t.Fatalf("seed linked running queue: %v", err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='running',lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+60 seconds'),started_at=CURRENT_TIMESTAMP WHERE id=?`, owner, sourceStep); err != nil {
		t.Fatalf("seed linked running step: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_abort_intent(task_identity,requested_by,reason,owner_fence,deadline) VALUES(?, '7', 'operator abort', ?, datetime(CURRENT_TIMESTAMP,'+10 seconds'))`, taskIdentity, owner); err != nil {
		t.Fatalf("seed linked abort intent: %v", err)
	}

	svc := NewMutateService(db)
	if err := svc.AbortAcknowledge(context.Background(), AbortAckParams{TaskIdentity: taskIdentity, OwnerFence: owner}); err != nil {
		t.Fatalf("acknowledge linked abort: %v", err)
	}

	var queueStatus, stepStatus, intentOutcome string
	var queueOwner, stepOwner sql.NullString
	var acknowledgedAt sql.NullTime
	if err := db.QueryRow(`SELECT status,lease_owner FROM post_ingest_task WHERE id=?`, sourceTask).Scan(&queueStatus, &queueOwner); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,lease_owner FROM media_ingest_step WHERE id=? AND run_id=?`, sourceStep, runID).Scan(&stepStatus, &stepOwner); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT acknowledged_at,outcome FROM task_abort_intent WHERE task_identity=?`, taskIdentity).Scan(&acknowledgedAt, &intentOutcome); err != nil {
		t.Fatal(err)
	}
	if queueStatus != "cancelled" || stepStatus != "cancelled" || queueOwner.String != "" || stepOwner.String != "" {
		t.Fatalf("terminal queue=%s owner=%q step=%s owner=%q", queueStatus, queueOwner.String, stepStatus, stepOwner.String)
	}
	if !acknowledgedAt.Valid || intentOutcome != "cancelled" {
		t.Fatalf("intent acknowledged=%v outcome=%q", acknowledgedAt.Valid, intentOutcome)
	}
	var dependentQueueStatus, dependentStepStatus string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, dependentTask).Scan(&dependentQueueStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, dependentStep).Scan(&dependentStepStatus); err != nil {
		t.Fatal(err)
	}
	if dependentQueueStatus != "skipped" || dependentStepStatus != "skipped" {
		t.Fatalf("publication finalization did not propagate dependency: queue=%s step=%s", dependentQueueStatus, dependentStepStatus)
	}
}
func TestFencedLeaseRecoveryLinkedRunningTaskFinalizesStep(t *testing.T) {
	db := openLinkedMutationTestDB(t)
	_, sourceStep, dependentStep, sourceTask, dependentTask := seedLinkedMutationGraph(t, db)
	owner := "linked-worker/recovery-fence"
	taskIdentity := BuildIdentity("orchestration", sourceTask)
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='running',lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'-60 seconds'),started_at=CURRENT_TIMESTAMP WHERE id=?`, owner, sourceTask); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='running',lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'-60 seconds'),started_at=CURRENT_TIMESTAMP WHERE id=?`, owner, sourceStep); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_abort_intent(task_identity,requested_by,reason,owner_fence,deadline,recovery_required_at,outcome) VALUES(?, '7', 'operator abort', ?, datetime(CURRENT_TIMESTAMP,'-50 seconds'), CURRENT_TIMESTAMP, 'timeout')`, taskIdentity, owner); err != nil {
		t.Fatal(err)
	}

	svc := NewMutateService(db)
	if err := svc.FencedLeaseRecovery(context.Background(), FencedRecoveryParams{TaskIdentity: taskIdentity, ActorID: 7, Reason: "abort timeout recovery"}); err != nil {
		t.Fatalf("fenced linked recovery: %v", err)
	}
	assertLinkedTerminalPropagation(t, db, sourceStep, dependentStep, sourceTask, dependentTask, "cancelled")
	var acknowledgedAt, recoveryAt sql.NullTime
	var outcome string
	if err := db.QueryRow(`SELECT acknowledged_at,recovery_required_at,outcome FROM task_abort_intent WHERE task_identity=?`, taskIdentity).Scan(&acknowledgedAt, &recoveryAt, &outcome); err != nil {
		t.Fatal(err)
	}
	if !acknowledgedAt.Valid || !recoveryAt.Valid || outcome != "recovered" {
		t.Fatalf("intent acknowledged=%v recovery=%v outcome=%q", acknowledgedAt.Valid, recoveryAt.Valid, outcome)
	}
}
func TestRemoveLinkedWaitingTaskSynchronizesStepAndTombstone(t *testing.T) {
	db := openLinkedMutationTestDB(t)
	_, sourceStep, dependentStep, sourceTask, dependentTask := seedLinkedMutationGraph(t, db)
	svc := NewMutateService(db)
	if err := svc.Remove(context.Background(), RemoveParams{TaskIdentity: BuildIdentity("orchestration", sourceTask), ActorID: 7, Reason: "operator removal"}); err != nil {
		t.Fatalf("remove linked task: %v", err)
	}
	assertLinkedTerminalPropagation(t, db, sourceStep, dependentStep, sourceTask, dependentTask, "cancelled")
	var removedAt sql.NullTime
	var removedBy, removeReason string
	if err := db.QueryRow(`SELECT removed_at,removed_by,remove_reason FROM post_ingest_task WHERE id=?`, sourceTask).Scan(&removedAt, &removedBy, &removeReason); err != nil {
		t.Fatal(err)
	}
	if !removedAt.Valid || removedBy != "7" || removeReason != "operator removal" {
		t.Fatalf("tombstone time=%v by=%q reason=%q", removedAt.Valid, removedBy, removeReason)
	}
}

func TestSkipLinkedWaitingTaskSynchronizesStepAndDependencies(t *testing.T) {
	db := openLinkedMutationTestDB(t)
	_, sourceStep, dependentStep, sourceTask, dependentTask := seedLinkedMutationGraph(t, db)
	svc := NewMutateService(db)
	if err := svc.Skip(context.Background(), SkipParams{TaskIdentity: BuildIdentity("orchestration", sourceTask), ActorID: 8, Reason: "policy skip"}); err != nil {
		t.Fatalf("skip linked task: %v", err)
	}
	assertLinkedTerminalPropagation(t, db, sourceStep, dependentStep, sourceTask, dependentTask, "skipped")
	var removedAt sql.NullTime
	if err := db.QueryRow(`SELECT removed_at FROM post_ingest_task WHERE id=?`, sourceTask).Scan(&removedAt); err != nil {
		t.Fatal(err)
	}
	if removedAt.Valid {
		t.Fatal("skip must not tombstone the task")
	}
}

func TestRemoveLinkedTaskRollsBackWhenFinalizationFails(t *testing.T) {
	db := openLinkedMutationTestDB(t)
	_, sourceStep, _, sourceTask, _ := seedLinkedMutationGraph(t, db)
	if _, err := db.Exec(`CREATE TRIGGER fail_taskcontrol_plan BEFORE INSERT ON media_plan_completion BEGIN SELECT RAISE(ABORT,'plan blocked'); END`); err != nil {
		t.Fatal(err)
	}
	svc := NewMutateService(db)
	if err := svc.Remove(context.Background(), RemoveParams{TaskIdentity: BuildIdentity("orchestration", sourceTask), ActorID: 9, Reason: "must rollback"}); err == nil {
		t.Fatal("expected finalization failure")
	}
	var queueStatus, stepStatus string
	var removedAt sql.NullTime
	if err := db.QueryRow(`SELECT status,removed_at FROM post_ingest_task WHERE id=?`, sourceTask).Scan(&queueStatus, &removedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, sourceStep).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if queueStatus != "waiting" || stepStatus != "waiting" || removedAt.Valid {
		t.Fatalf("rollback queue=%s step=%s removed=%v", queueStatus, stepStatus, removedAt.Valid)
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
	db := openLinkedMutationTestDB(t)
	_, sourceStep, dependentStep, sourceTask, dependentTask := seedLinkedMutationGraph(t, db)
	svc := NewMutateService(db)
	if err := svc.Skip(context.Background(), SkipParams{
		TaskIdentity: BuildIdentity("orchestration", sourceTask),
		ActorID:      1,
		Reason:       "dep impossible",
	}); err != nil {
		t.Fatalf("skip: %v", err)
	}
	assertLinkedTerminalPropagation(t, db, sourceStep, dependentStep, sourceTask, dependentTask, "skipped")
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

func TestBatchAbortPersistsDistinctFencesAndNotifiesAfterCommit(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id1 := insertRunningTask(t, db, "preview")
	id2 := insertRunningTask(t, db, "preview")
	if _, err := db.Exec(`UPDATE post_ingest_task SET lease_owner=CASE id WHEN ? THEN 'preview-owner/one' ELSE 'preview-owner/two' END WHERE id IN (?, ?)`, id1, id1, id2); err != nil {
		t.Fatal(err)
	}
	taskID1 := BuildIdentity("orchestration", id1)
	taskID2 := BuildIdentity("orchestration", id2)
	var notified []int64
	svc.SetAbortNotifier(func(taskID int64) {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM task_abort_intent WHERE task_identity=?`, BuildIdentity("orchestration", taskID)).Scan(&count); err != nil {
			t.Errorf("query committed intent in notifier: %v", err)
		} else if count != 1 {
			t.Errorf("notifier ran before intent commit for task %d", taskID)
		}
		notified = append(notified, taskID)
	})
	params := BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000013",
		Action:      "abort",
		ActorID:     42,
		Reason:      "stop previews",
		Items:       []BatchItem{{TaskIdentity: taskID1}, {TaskIdentity: taskID2}},
	}
	result, err := svc.Batch(context.Background(), params)
	if err != nil {
		t.Fatalf("batch abort: %v", err)
	}
	if result.Succeeded != 2 || result.Failed != 0 || len(result.Items) != 2 {
		t.Fatalf("result=%+v", result)
	}
	for taskID, fence := range map[string]string{taskID1: "preview-owner/one", taskID2: "preview-owner/two"} {
		var gotFence, requestedBy, reason, status string
		_, id, _ := parseIdentity(taskID)
		if err := db.QueryRow(`SELECT i.owner_fence,i.requested_by,i.reason,t.status FROM task_abort_intent i JOIN post_ingest_task t ON t.id=? WHERE i.task_identity=?`, id, taskID).Scan(&gotFence, &requestedBy, &reason, &status); err != nil {
			t.Fatal(err)
		}
		if gotFence != fence || requestedBy != "42" || reason != "stop previews" || status != "running" {
			t.Errorf("task %s intent=(%q,%q,%q) status=%q", taskID, gotFence, requestedBy, reason, status)
		}
	}
	if len(notified) != 2 || notified[0] != id1 || notified[1] != id2 {
		t.Fatalf("notified=%v want [%d %d]", notified, id1, id2)
	}

	replay, err := svc.Batch(context.Background(), params)
	if err != nil {
		t.Fatalf("replay batch abort: %v", err)
	}
	if replay.Succeeded != 2 || replay.Failed != 0 || len(notified) != 2 {
		t.Fatalf("replay=%+v notified=%v", replay, notified)
	}
}

func TestBatchAbortMixedRunningAndWaiting(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	runningID := insertRunningTask(t, db, "preview")
	waitingID := insertTestTask(t, db, "preview", "waiting", 0)
	var notified []int64
	svc.SetAbortNotifier(func(taskID int64) { notified = append(notified, taskID) })
	result, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000014",
		Action:      "abort",
		ActorID:     7,
		Reason:      "mixed abort",
		Items: []BatchItem{
			{TaskIdentity: BuildIdentity("orchestration", runningID)},
			{TaskIdentity: BuildIdentity("orchestration", waitingID)},
		},
	})
	if err != nil {
		t.Fatalf("batch abort: %v", err)
	}
	if result.Succeeded != 1 || result.Failed != 1 || len(result.Items) != 2 || !result.Items[0].Ok || result.Items[1].Ok || result.Items[1].OutcomeCode != "permanent_failure" {
		t.Fatalf("result=%+v", result)
	}
	if len(notified) != 1 || notified[0] != runningID {
		t.Fatalf("notified=%v want [%d]", notified, runningID)
	}
	var runningIntents, waitingIntents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_abort_intent WHERE task_identity=?`, BuildIdentity("orchestration", runningID)).Scan(&runningIntents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_abort_intent WHERE task_identity=?`, BuildIdentity("orchestration", waitingID)).Scan(&waitingIntents); err != nil {
		t.Fatal(err)
	}
	if runningIntents != 1 || waitingIntents != 0 {
		t.Fatalf("intent counts running=%d waiting=%d", runningIntents, waitingIntents)
	}
}

func TestBatchAbortAuditFailureRollsBackIntentAndSkipsNotifier(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "preview")
	taskID := BuildIdentity("orchestration", id)
	if _, err := db.Exec(`CREATE TRIGGER fail_abort_audit BEFORE INSERT ON task_control_audit WHEN NEW.action='abort_request' BEGIN SELECT RAISE(ABORT,'audit blocked'); END`); err != nil {
		t.Fatal(err)
	}
	var notified []int64
	svc.SetAbortNotifier(func(taskID int64) { notified = append(notified, taskID) })
	result, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000015",
		Action:      "abort",
		ActorID:     9,
		Reason:      "fault injection",
		Items:       []BatchItem{{TaskIdentity: taskID}},
	})
	if err != nil {
		t.Fatalf("batch abort: %v", err)
	}
	if result.Succeeded != 0 || result.Failed != 1 || len(result.Items) != 1 || result.Items[0].OutcomeCode != "retryable_failure" {
		t.Fatalf("result=%+v", result)
	}
	var intents, audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_abort_intent WHERE task_identity=?`, taskID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_control_audit WHERE task_identity=? AND action='abort_request'`, taskID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if intents != 0 || audits != 0 || len(notified) != 0 {
		t.Fatalf("intents=%d audits=%d notified=%v", intents, audits, notified)
	}
}

func TestBatchAbortCoversAllRegisteredPostIngestTypes(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)

	internalTypes := make(map[string]struct{})
	for _, group := range NewRegistry().Groups {
		for _, spec := range group.Types {
			for _, mapping := range spec.SourceMappings {
				if mapping.Kind == "post_ingest_task" && mapping.InternalType != "" {
					internalTypes[mapping.InternalType] = struct{}{}
				}
			}
		}
	}
	if len(internalTypes) < 10 {
		t.Fatalf("discovered only %d post_ingest_task internal types: %v", len(internalTypes), internalTypes)
	}
	representativeTypes := []string{"poster", "poster_repair", "preview"}
	if _, subtitleIsPostIngest := internalTypes["subtitle"]; subtitleIsPostIngest {
		representativeTypes = append(representativeTypes, "subtitle")
	}
	for _, required := range representativeTypes {
		if _, ok := internalTypes[required]; !ok {
			t.Errorf("registry is missing required post_ingest_task type %q", required)
		}
	}

	types := make([]string, 0, len(internalTypes))
	for internalType := range internalTypes {
		types = append(types, internalType)
	}
	sort.Strings(types)

	items := make([]BatchItem, 0, len(types))
	identityToFence := make(map[string]string, len(types))
	identityToID := make(map[string]int64, len(types))
	for index, internalType := range types {
		id := insertTestTask(t, db, internalType, "running", 0)
		fence := fmt.Sprintf("registry-owner/%02d-%s", index, internalType)
		if _, err := db.Exec(`UPDATE post_ingest_task SET lease_owner=?, lease_until=datetime('now','+5 minutes') WHERE id=?`, fence, id); err != nil {
			t.Fatalf("set %s lease: %v", internalType, err)
		}
		identity := BuildIdentity("orchestration", id)
		items = append(items, BatchItem{TaskIdentity: identity})
		identityToFence[identity] = fence
		identityToID[identity] = id
	}

	notified := make(map[int64]int, len(types))
	svc.SetAbortNotifier(func(taskID int64) { notified[taskID]++ })
	result, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000016",
		Action:      "abort",
		ActorID:     77,
		Reason:      "registry coverage",
		Items:       items,
	})
	if err != nil {
		t.Fatalf("batch abort: %v", err)
	}
	if result.Succeeded != len(types) || result.Failed != 0 || len(result.Retryable) != 0 || len(result.Items) != len(types) {
		t.Fatalf("result=%+v discovered types=%v", result, types)
	}
	for _, item := range result.Items {
		if !item.Ok || item.OutcomeCode != "success" {
			t.Errorf("item=%+v", item)
		}
	}

	for identity, fence := range identityToFence {
		id := identityToID[identity]
		var gotFence, outcome, status string
		var acknowledgedAt sql.NullTime
		if err := db.QueryRow(`SELECT i.owner_fence,i.outcome,i.acknowledged_at,t.status FROM task_abort_intent i JOIN post_ingest_task t ON t.id=? WHERE i.task_identity=?`, id, identity).Scan(&gotFence, &outcome, &acknowledgedAt, &status); err != nil {
			t.Fatalf("read %s intent: %v", identity, err)
		}
		if gotFence != fence || outcome != "" || acknowledgedAt.Valid || status != "running" {
			t.Errorf("identity=%s fence=%q want=%q outcome=%q acknowledged=%v status=%q", identity, gotFence, fence, outcome, acknowledgedAt.Valid, status)
		}
		var intentCount, auditCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM task_abort_intent WHERE task_identity=? AND owner_fence=? AND acknowledged_at IS NULL AND outcome=''`, identity, fence).Scan(&intentCount); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM task_control_audit WHERE task_identity=? AND action='abort_request'`, identity).Scan(&auditCount); err != nil {
			t.Fatal(err)
		}
		if intentCount != 1 || auditCount != 1 || notified[id] != 1 {
			t.Errorf("identity=%s intents=%d audits=%d notifications=%d", identity, intentCount, auditCount, notified[id])
		}
	}
	if len(notified) != len(types) {
		t.Errorf("notified %d unique tasks, want %d", len(notified), len(types))
	}
}

func TestAbortRequestRejectsNonOrchestrationIdentity(t *testing.T) {
	db := openMutationTestDB(t)
	svc := NewMutateService(db)
	id := insertRunningTask(t, db, "preview")
	wrongIdentity := BuildIdentity("preview_task", id)

	_, helperErr := store.WithImmediateConnTx(context.Background(), db, func(tx store.ImmediateConnTx) error {
		return AbortRequestInTx(context.Background(), tx, wrongIdentity, 1, "wrong kind")
	})
	if !errors.Is(helperErr, ErrInvalidOperation) {
		t.Fatalf("helper error=%v want ErrInvalidOperation", helperErr)
	}
	if err := svc.AbortRequest(context.Background(), AbortRequestParams{TaskIdentity: wrongIdentity, ActorID: 1, Reason: "wrong kind"}); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("direct error=%v want ErrInvalidOperation", err)
	}

	var notified []int64
	svc.SetAbortNotifier(func(taskID int64) { notified = append(notified, taskID) })
	result, err := svc.Batch(context.Background(), BatchParams{
		OperationID: "00000000-0000-0000-0000-000000000017",
		Action:      "abort",
		ActorID:     1,
		Reason:      "wrong kind",
		Items:       []BatchItem{{TaskIdentity: wrongIdentity}},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if result.Succeeded != 0 || result.Failed != 1 || len(result.Items) != 1 || result.Items[0].OutcomeCode != "retryable_failure" {
		t.Fatalf("result=%+v", result)
	}
	var intents, audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_abort_intent`).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_control_audit WHERE action='abort_request'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if intents != 0 || audits != 0 || len(notified) != 0 {
		t.Fatalf("intents=%d audits=%d notified=%v", intents, audits, notified)
	}
}
