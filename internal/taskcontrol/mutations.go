package taskcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"knox-media/internal/publication"
	"knox-media/internal/scheduler"
	"knox-media/internal/store"
)

// =============================================================================
// Mutation Request/Response Types
// =============================================================================

// AbortRequestParams carries the parameters for requesting a cooperative abort.
type AbortRequestParams struct {
	TaskIdentity string
	ActorID      int64
	Reason       string
}

// AbortAckParams carries the worker's acknowledgement of an abort signal.
type AbortAckParams struct {
	TaskIdentity string
	OwnerFence   string
}

// AbortTimeoutParams carries parameters when an abort times out.
type AbortTimeoutParams struct {
	TaskIdentity string
}

// CancelParams carries parameters for canceling a non-running task.
type CancelParams struct {
	TaskIdentity string
	ActorID      int64
	Reason       string
}

// FencedRecoveryParams carries parameters for fenced lease recovery.
type FencedRecoveryParams struct {
	TaskIdentity string
	ActorID      int64
	Reason       string
}

// RemoveParams carries parameters for tombstone removal.
type RemoveParams struct {
	TaskIdentity     string
	ActorID          int64
	Reason           string
	ExpectedRevision int64 // 0 = skip revision check
}

// ResetParams carries parameters for monotonic reset.
type ResetParams struct {
	TaskIdentity       string
	ActorID            int64
	Reason             string
	ExpectedGeneration int64 // 0 = skip generation check
	ExpectedRetryRound int   // 0 = skip check, >0 = must match
	ExpectedRevision   int64 // 0 = skip revision check
}

// ReopenParams carries parameters for explicit AI reopen.
type ReopenParams struct {
	TaskIdentity       string
	ActorID            int64
	Reason             string
	ExpectedRetryRound int // 0 = skip check
}

// RunNowParams carries parameters for run-now boost.
type RunNowParams struct {
	TaskIdentity string
	ActorID      int64
	Reason       string
}

// SkipParams carries parameters for skipping a task.
type SkipParams struct {
	TaskIdentity string
	ActorID      int64
	Reason       string
}

// BatchItem represents one item in a batch operation.
type BatchItem struct {
	TaskIdentity     string `json:"task_identity"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}

// BatchParams carries parameters for a batch operation.
type BatchParams struct {
	OperationID string
	Action      string
	ActorID     int64
	Reason      string
	Items       []BatchItem
}

// BatchItemResult is the outcome of a single item in a batch.
type BatchItemResult struct {
	TaskIdentity string `json:"task_identity"`
	Ok           bool   `json:"ok"`
	OutcomeCode  string `json:"outcome_code,omitempty"`
	Revision     int64  `json:"revision"`
}

// BatchResult is the result of a batch operation.
type BatchResult struct {
	OperationID    string            `json:"operation_id"`
	Action         string            `json:"action"`
	RequestedCount int               `json:"requested_count"`
	Succeeded      int               `json:"succeeded"`
	Failed         int               `json:"failed"`
	Skipped        int               `json:"skipped"`
	Retryable      []BatchItemResult `json:"retryable,omitempty"`
	Items          []BatchItemResult `json:"items"`
	CompletedAt    time.Time         `json:"completed_at"`
}

// =============================================================================
// ErrInvalidOperation is returned when an operation is invalid.
// =============================================================================

var (
	ErrInvalidOperation    = errors.New("taskcontrol: invalid operation")
	ErrNotRunning          = errors.New("taskcontrol: task is not running")
	ErrNotTerminal         = errors.New("taskcontrol: task is not in a terminal state")
	ErrNotWaiting          = errors.New("taskcontrol: task is not waiting")
	ErrActiveLease         = errors.New("taskcontrol: active lease prevents recovery")
	ErrUncertainOwner      = errors.New("taskcontrol: uncertain ownership requires operator")
	ErrStaleRevision       = errors.New("taskcontrol: stale revision; fetch latest first")
	ErrGenerationMismatch  = errors.New("taskcontrol: generation mismatch")
	ErrRetryRoundMismatch  = errors.New("taskcontrol: retry round mismatch")
	ErrAlreadyRemoved      = errors.New("taskcontrol: task already removed")
	ErrNotAI               = errors.New("taskcontrol: not an AI analysis task")
	ErrBatchTooLarge       = errors.New("taskcontrol: batch exceeds 200 items")
	ErrBatchInvalidUUID    = errors.New("taskcontrol: operation_id must be a valid UUID")
	ErrBatchActionMismatch = errors.New("taskcontrol: operation action mismatch")
)

// =============================================================================
// MutateService is the unified mutation service for task lifecycle mutations.
// =============================================================================

// MutateService performs idempotent, audited mutations on task rows.
type MutateService struct {
	db            *sql.DB
	abortNotifier func(taskID int64)
}

// NewMutateService creates a new MutateService.
func NewMutateService(db *sql.DB) *MutateService {
	return &MutateService{db: db}
}

// SetAbortNotifier installs the callback used to signal an in-process worker.
// It is invoked only after the durable mutation commits.
func (s *MutateService) SetAbortNotifier(notifier func(taskID int64)) {
	s.abortNotifier = notifier
}

func (s *MutateService) notifyAbort(taskIdentity string) {
	if s.abortNotifier == nil {
		return
	}
	_, id, err := parseIdentity(taskIdentity)
	if err == nil {
		s.abortNotifier(id)
	}
}

const abortGrace = 10 * time.Second

func upsertAbortIntentTx(ctx context.Context, tx store.ImmediateConnTx, taskIdentity, ownerFence string, actorID int64, reason string) error {
	deadline := time.Now().Add(abortGrace)
	_, err := tx.ExecContext(ctx, `INSERT INTO task_abort_intent
		(task_identity, requested_at, requested_by, reason, owner_fence, deadline, acknowledged_at, outcome, recovery_required_at)
		VALUES (?, CURRENT_TIMESTAMP, ?, ?, ?, ?, NULL, '', NULL)
		ON CONFLICT(task_identity) DO UPDATE SET requested_at=CURRENT_TIMESTAMP,
			requested_by=excluded.requested_by, reason=excluded.reason, owner_fence=excluded.owner_fence,
			deadline=excluded.deadline, acknowledged_at=NULL, outcome='', recovery_required_at=NULL`,
		taskIdentity, fmt.Sprintf("%d", actorID), reason, ownerFence, deadline)
	return err
}

// =============================================================================
// Abort Lifecycle
// =============================================================================

// AbortRequest persists a durable abort intent before signaling the worker.
// The queue row stays running until the worker acknowledges it.
func (s *MutateService) AbortRequest(ctx context.Context, p AbortRequestParams) error {
	outcome, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		return AbortRequestInTx(ctx, tx, p.TaskIdentity, p.ActorID, p.Reason)
	})
	if err != nil {
		return err
	}
	if outcome.CommitConfirmed {
		s.notifyAbort(p.TaskIdentity)
	}
	return nil
}

// AbortAcknowledge processes the worker's acknowledgement of an abort signal.
// It atomically commits cancelled status, clears temporary resources, and
// propagates dependency/plan/retirement state.
func (s *MutateService) AbortAcknowledge(ctx context.Context, p AbortAckParams) error {
	if p.TaskIdentity == "" || p.OwnerFence == "" {
		return fmt.Errorf("%w: task identity and owner fence required", ErrInvalidOperation)
	}

	_, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		_, id, err := parseIdentity(p.TaskIdentity)
		if err != nil {
			return err
		}
		var currentOwner, intentFence string
		var ingestRunID, ingestStepID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(lease_owner,''), ingest_run_id, ingest_step_id
			FROM post_ingest_task WHERE id=? AND status='running'`, id).Scan(&currentOwner, &ingestRunID, &ingestStepID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: task not running", ErrNotRunning)
			}
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT owner_fence FROM task_abort_intent
			WHERE task_identity=? AND acknowledged_at IS NULL AND outcome=''`, p.TaskIdentity).Scan(&intentFence); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: abort was not requested for this task", ErrInvalidOperation)
			}
			return err
		}
		if p.OwnerFence != currentOwner || p.OwnerFence != intentFence {
			return fmt.Errorf("%w: owner fence mismatch", ErrInvalidOperation)
		}
		result, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='cancelled', lease_owner='', lease_until=NULL,
			last_error='aborted by operator', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND status='running' AND lease_owner=?`, id, p.OwnerFence)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: abort ack race", ErrInvalidOperation)
		}
		result, err = tx.ExecContext(ctx, `UPDATE task_abort_intent SET acknowledged_at=CURRENT_TIMESTAMP,
			outcome='cancelled' WHERE task_identity=? AND owner_fence=? AND acknowledged_at IS NULL`, p.TaskIdentity, p.OwnerFence)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: abort intent race", ErrInvalidOperation)
		}
		if ingestRunID.Valid && ingestStepID.Valid {
			if err := syncLinkedStepTerminalTx(ctx, tx, ingestRunID.Int64, ingestStepID.Int64, "cancelled", "aborted by operator", "running"); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_control_audit
			(task_identity, actor_id, action, reason, previous_status, new_status)
			VALUES (?, 0, 'abort_acknowledge', 'worker_ack', 'running', 'cancelled')`, p.TaskIdentity)
		return err
	})
	return err
}

// AbortTimeout marks the task as requiring recovery after an abort timeout.
// The task stays running while its durable intent is marked for recovery.
func (s *MutateService) AbortTimeout(ctx context.Context, p AbortTimeoutParams) error {
	if p.TaskIdentity == "" {
		return fmt.Errorf("%w: task identity required", ErrInvalidOperation)
	}
	_, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		_, id, err := parseIdentity(p.TaskIdentity)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE task_abort_intent SET recovery_required_at=CURRENT_TIMESTAMP, outcome='timeout'
			WHERE task_identity=? AND acknowledged_at IS NULL AND outcome='' AND
			EXISTS (SELECT 1 FROM post_ingest_task WHERE id=? AND status='running')`, p.TaskIdentity, id)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: running task has no pending abort intent", ErrInvalidOperation)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_control_audit
			(task_identity, actor_id, action, reason, previous_status, new_status)
			VALUES (?, 0, 'abort_timeout', 'timeout', 'running', 'running')`, p.TaskIdentity)
		return err
	})
	return err
}

// FencedLeaseRecovery recovers a task whose lease expired while abort was requested.
// It commits cancelled and releases reservations exactly once.
// Active leases and uncertain ownership are rejected.
func (s *MutateService) FencedLeaseRecovery(ctx context.Context, p FencedRecoveryParams) error {
	if p.TaskIdentity == "" {
		return fmt.Errorf("%w: task identity required", ErrInvalidOperation)
	}
	_, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		_, id, err := parseIdentity(p.TaskIdentity)
		if err != nil {
			return err
		}
		var status, leaseOwner string
		var leaseUntil sql.NullTime
		var ingestRunID, ingestStepID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT status, COALESCE(lease_owner,''), lease_until, ingest_run_id, ingest_step_id
			FROM post_ingest_task WHERE id=?`, id).Scan(&status, &leaseOwner, &leaseUntil, &ingestRunID, &ingestStepID); err != nil {
			return err
		}
		if status != "running" {
			return fmt.Errorf("%w: task not running", ErrNotRunning)
		}
		var intentFence string
		if err := tx.QueryRowContext(ctx, `SELECT owner_fence FROM task_abort_intent WHERE task_identity=?
			AND acknowledged_at IS NULL AND outcome IN ('','timeout')`, p.TaskIdentity).Scan(&intentFence); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: recovery requires an abort intent", ErrInvalidOperation)
			}
			return err
		}
		if leaseOwner == "" || !strings.Contains(leaseOwner, "/") {
			return fmt.Errorf("%w: uncertain ownership for task %s", ErrUncertainOwner, p.TaskIdentity)
		}
		if intentFence != leaseOwner {
			return fmt.Errorf("%w: abort intent fence does not match queue owner", ErrInvalidOperation)
		}
		if leaseUntil.Valid && leaseUntil.Time.After(time.Now()) {
			return fmt.Errorf("%w: lease still active", ErrActiveLease)
		}
		result, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='cancelled', lease_owner='', lease_until=NULL,
			last_error='fenced recovery', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND status='running' AND lease_owner=?`, id, leaseOwner)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: recovery race", ErrInvalidOperation)
		}
		_, err = tx.ExecContext(ctx, `UPDATE task_abort_intent SET acknowledged_at=CURRENT_TIMESTAMP,
			outcome='recovered', recovery_required_at=COALESCE(recovery_required_at,CURRENT_TIMESTAMP)
			WHERE task_identity=? AND owner_fence=?`, p.TaskIdentity, leaseOwner)
		if err != nil {
			return err
		}
		// Admission reservations use an execution id derived from the worker prefix.
		ownerPrefix := strings.SplitN(leaseOwner, "/", 2)[0]
		rows, err := tx.QueryContext(ctx, `SELECT execution_id FROM scheduler_reservation
			WHERE status='active' AND execution_id LIKE ?`, ownerPrefix+"/%")
		if err != nil {
			return err
		}
		var executionIDs []string
		for rows.Next() {
			var executionID string
			if err := rows.Scan(&executionID); err != nil {
				rows.Close()
				return err
			}
			executionIDs = append(executionIDs, executionID)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, executionID := range executionIDs {
			if err := scheduler.ReleaseReservationTx(ctx, tx, executionID, "fenced_recovery", "recovery"); err != nil && !errors.Is(err, scheduler.ErrReservationNotActive) {
				return err
			}
		}
		if ingestRunID.Valid && ingestStepID.Valid {
			if err := syncLinkedStepTerminalTx(ctx, tx, ingestRunID.Int64, ingestStepID.Int64, "cancelled", p.Reason, "running"); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_control_audit
			(task_identity, actor_id, action, reason, previous_status, new_status)
			VALUES (?, ?, 'fenced_recovery', ?, 'running', 'cancelled')`, p.TaskIdentity, p.ActorID, p.Reason)
		return err
	})
	return err
}

// =============================================================================
// Cancel
// =============================================================================

// Cancel marks a non-running task as cancelled. Running tasks must go through
// AbortRequest -> AbortAcknowledge.
func (s *MutateService) Cancel(ctx context.Context, p CancelParams) error {
	if p.TaskIdentity == "" || p.ActorID <= 0 || p.Reason == "" {
		return fmt.Errorf("%w: actor and reason required", ErrInvalidOperation)
	}

	outcome, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		_, id, err := parseIdentity(p.TaskIdentity)
		if err != nil {
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: task not found", ErrInvalidOperation)
			}
			return err
		}
		if status == "running" {
			return fmt.Errorf("%w: running task must be aborted first", ErrInvalidOperation)
		}
		if status != "waiting" {
			return fmt.Errorf("%w: can only cancel waiting tasks (got %s)", ErrInvalidOperation, status)
		}

		result, err := tx.ExecContext(ctx,
			`UPDATE post_ingest_task SET status='cancelled', lease_owner='', lease_until=NULL,
			 last_error='cancelled by admin', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND status='waiting'`,
			id)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("%w: cancel race", ErrInvalidOperation)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
			 VALUES (?, ?, 'cancel', ?, 'waiting', 'cancelled')`,
			p.TaskIdentity, p.ActorID, p.Reason); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = outcome
	return nil
}

// =============================================================================
// Remove (Tombstone)
// =============================================================================

// Remove sets actor/reason/time, hides by default, preserves attempts/dependencies/
// journals/evidence/recovery, requests abort only for cancellable running work,
// and never deletes source/artifacts.
func (s *MutateService) Remove(ctx context.Context, p RemoveParams) error {
	if p.TaskIdentity == "" || p.ActorID <= 0 || p.Reason == "" {
		return fmt.Errorf("%w: actor and reason required", ErrInvalidOperation)
	}

	runningAbort := false
	outcome, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		_, id, err := parseIdentity(p.TaskIdentity)
		if err != nil {
			return err
		}
		var status, removedBy string
		var removedAt sql.NullTime
		var ingestRunID, ingestStepID sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT status, removed_at, removed_by, ingest_run_id, ingest_step_id FROM post_ingest_task WHERE id=?`,
			id).Scan(&status, &removedAt, &removedBy, &ingestRunID, &ingestStepID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: task not found", ErrInvalidOperation)
			}
			return err
		}
		// Already removed is idempotent
		if removedAt.Valid {
			// Still write audit for the replay
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
				 VALUES (?, ?, 'remove_replay', ?, ?, ?)`,
				p.TaskIdentity, p.ActorID, p.Reason, status, status); err != nil {
				return err
			}
			return nil
		}
		// Check revision if provided
		if p.ExpectedRevision > 0 {
			var currentRev int64
			if err := tx.QueryRowContext(ctx,
				`SELECT COALESCE(rev,0) FROM task_projection_revision WHERE task_identity=?`,
				p.TaskIdentity).Scan(&currentRev); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if currentRev > 0 && currentRev != p.ExpectedRevision {
				return fmt.Errorf("%w: expected rev %d, got %d", ErrStaleRevision, p.ExpectedRevision, currentRev)
			}
		}
		// For running tasks, request abort instead of immediate remove
		if status == "running" {
			var ownerFence string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(lease_owner,'') FROM post_ingest_task WHERE id=? AND status='running'`, id).Scan(&ownerFence); err != nil {
				return err
			}
			if ownerFence == "" {
				return fmt.Errorf("%w: running task has no lease owner", ErrUncertainOwner)
			}
			if err := upsertAbortIntentTx(ctx, tx, p.TaskIdentity, ownerFence, p.ActorID, p.Reason); err != nil {
				return err
			}
			runningAbort = true
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
				 VALUES (?, ?, 'remove_abort_request', ?, 'running', 'running')`,
				p.TaskIdentity, p.ActorID, p.Reason); err != nil {
				return err
			}
			return nil
		}
		// Non-running: set tombstone
		newStatus := status
		if status == "waiting" {
			newStatus = "cancelled"
		}
		removedByStr := fmt.Sprintf("%d", p.ActorID)
		result, err := tx.ExecContext(ctx,
			`UPDATE post_ingest_task SET status=?, removed_at=CURRENT_TIMESTAMP, removed_by=?, remove_reason=?,
			 lease_owner='', lease_until=NULL, finished_at=COALESCE(finished_at, CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND removed_at IS NULL`,
			newStatus, removedByStr, p.Reason, id)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("%w: remove race", ErrInvalidOperation)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
			 VALUES (?, ?, 'remove', ?, ?, ?)`,
			p.TaskIdentity, p.ActorID, p.Reason, status, newStatus); err != nil {
			return err
		}
		if status == "waiting" && ingestRunID.Valid && ingestStepID.Valid {
			if err := syncLinkedStepTerminalTx(ctx, tx, ingestRunID.Int64, ingestStepID.Int64, "cancelled", p.Reason, "waiting"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if outcome.CommitConfirmed && runningAbort {
		s.notifyAbort(p.TaskIdentity)
	}
	return nil
}

// =============================================================================
// Reset (Monotonic Retry)
// =============================================================================

// Reset increments retry_round N→N+1, preserves attempt history, creates/reopens
// waiting execution, clears only obsolete lease fields, validates generation/
// source strategy/dependencies, recomputes downstream/plan/retirement state,
// and writes revision/audit atomically.
func (s *MutateService) Reset(ctx context.Context, p ResetParams) error {
	if p.TaskIdentity == "" || p.ActorID <= 0 || p.Reason == "" {
		return fmt.Errorf("%w: actor and reason required", ErrInvalidOperation)
	}

	outcome, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		_, id, err := parseIdentity(p.TaskIdentity)
		if err != nil {
			return err
		}
		var status string
		var retryRound, generation int64
		var ingestRunID, ingestStepID sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT status, retry_round, generation, ingest_run_id, ingest_step_id FROM post_ingest_task WHERE id=?`,
			id).Scan(&status, &retryRound, &generation, &ingestRunID, &ingestStepID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: task not found", ErrInvalidOperation)
			}
			return err
		}
		// Stale revision check
		if p.ExpectedRevision > 0 {
			var currentRev int64
			if err := tx.QueryRowContext(ctx,
				`SELECT COALESCE(revision,0) FROM task_projection_revision WHERE task_identity=?`,
				p.TaskIdentity).Scan(&currentRev); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if currentRev > 0 && currentRev != p.ExpectedRevision {
				// Stale revision: return current state without mutation
				return nil
			}
		}
		// Validate generation
		if p.ExpectedGeneration > 0 && generation != p.ExpectedGeneration {
			return fmt.Errorf("%w: expected gen %d, got %d", ErrGenerationMismatch, p.ExpectedGeneration, generation)
		}
		// Validate retry round
		if p.ExpectedRetryRound > 0 && int(retryRound) != p.ExpectedRetryRound {
			return fmt.Errorf("%w: expected round %d, got %d", ErrRetryRoundMismatch, p.ExpectedRetryRound, retryRound)
		}
		// Must be terminal
		if !isTerminalStatus(status) {
			return fmt.Errorf("%w: cannot reset non-terminal task (%s)", ErrNotTerminal, status)
		}

		nextRound := int(retryRound) + 1
		result, err := tx.ExecContext(ctx,
			`UPDATE post_ingest_task SET status='waiting', last_error='', lease_owner='', lease_until=NULL,
			 started_at=NULL, finished_at=NULL, attempts=0, retry_round=?,
			 available_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND retry_round=?`,
			nextRound, id, retryRound)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("%w: reset race", ErrInvalidOperation)
		}

		// Reopen the linked ingest step so the task can be claimed again. A
		// required barrier step also reopens its run to processing; otherwise
		// claim eligibility (required steps need a processing run) never holds.
		if ingestRunID.Valid && ingestStepID.Valid {
			if err := resetLinkedStepTx(ctx, tx, ingestRunID.Int64, ingestStepID.Int64, nextRound); err != nil {
				return err
			}
		}

		// Write audit
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status, new_retry_round)
			 VALUES (?, ?, 'reset', ?, ?, 'waiting', ?)`,
			p.TaskIdentity, p.ActorID, p.Reason, status, nextRound); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = outcome
	return nil
}

func isTerminalStatus(s string) bool {
	switch s {
	case "done", "failed", "cancelled", "skipped":
		return true
	}
	return false
}

// =============================================================================
// Reopen (Explicit AI Reopen)
// =============================================================================

// Reopen performs an explicit AI reopen for a skipped ai_analysis task.
// Recognition reset alone and recognition waiting/running/success do not
// reopen AI. After recognition succeeds, explicit AI reopen increments
// AI retry round, leaves DAG topology/identity/provenance unchanged,
// marks plan nonterminal, recomputes retirement barrier, and audits actor/reason.
func (s *MutateService) Reopen(ctx context.Context, p ReopenParams) error {
	if p.TaskIdentity == "" || p.ActorID <= 0 || p.Reason == "" {
		return fmt.Errorf("%w: actor and reason required", ErrInvalidOperation)
	}

	outcome, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		_, id, err := parseIdentity(p.TaskIdentity)
		if err != nil {
			return err
		}
		var status, taskType string
		var retryRound int
		if err := tx.QueryRowContext(ctx,
			`SELECT status, task_type, retry_round FROM post_ingest_task WHERE id=?`,
			id).Scan(&status, &taskType, &retryRound); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: task not found", ErrInvalidOperation)
			}
			return err
		}
		// Only skipped AI tasks can be reopened
		if taskType != "ai_analysis" {
			return fmt.Errorf("%w: not an AI analysis task (got %s)", ErrNotAI, taskType)
		}
		if status != "skipped" {
			return fmt.Errorf("%w: can only reopen skipped AI tasks (got %s)", ErrInvalidOperation, status)
		}
		// Running/blocking/success do not reopen
		if p.ExpectedRetryRound > 0 && retryRound != p.ExpectedRetryRound {
			return fmt.Errorf("%w: expected round %d, got %d", ErrRetryRoundMismatch, p.ExpectedRetryRound, retryRound)
		}

		nextRound := retryRound + 1
		result, err := tx.ExecContext(ctx,
			`UPDATE post_ingest_task SET status='waiting', last_error='', lease_owner='', lease_until=NULL,
			 started_at=NULL, finished_at=NULL, attempts=0, retry_round=?,
			 available_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND retry_round=? AND task_type='ai_analysis'`,
			nextRound, id, retryRound)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("%w: reopen race", ErrInvalidOperation)
		}

		// Write audit
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status, new_retry_round)
			 VALUES (?, ?, 'reopen', ?, 'skipped', 'waiting', ?)`,
			p.TaskIdentity, p.ActorID, p.Reason, nextRound); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = outcome
	return nil
}

// =============================================================================
// Run-Now
// =============================================================================

// RunNow sets a bounded Phase 3 boost and bypasses no resource/capability limit.
// Priority becomes MAX(priority)+1 and available_at is set to the distant past.
func (s *MutateService) RunNow(ctx context.Context, p RunNowParams) error {
	if p.TaskIdentity == "" || p.ActorID <= 0 || p.Reason == "" {
		return fmt.Errorf("%w: actor and reason required", ErrInvalidOperation)
	}

	outcome, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		_, id, err := parseIdentity(p.TaskIdentity)
		if err != nil {
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: task not found", ErrInvalidOperation)
			}
			return err
		}
		if status != "waiting" {
			return fmt.Errorf("%w: can only run-now waiting tasks (got %s)", ErrNotWaiting, status)
		}

		// Get max priority and set boost
		var maxPri int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(base_priority), 0) FROM post_ingest_task`).Scan(&maxPri); err != nil {
			return err
		}
		nextPri := maxPri + 1
		// Expire run-now boost after 5 minutes
		runNowExpires := time.Now().Add(5 * time.Minute)

		result, err := tx.ExecContext(ctx,
			`UPDATE post_ingest_task SET base_priority=?, available_at=datetime('now','-100 years'),
			 run_now_expires=?, updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND status='waiting'`,
			nextPri, runNowExpires, id)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("%w: run-now race", ErrInvalidOperation)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
			 VALUES (?, ?, 'run_now', ?, 'waiting', 'waiting')`,
			p.TaskIdentity, p.ActorID, p.Reason); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = outcome
	return nil
}

// =============================================================================
// Skip
// =============================================================================

// Skip requires policy and propagates dependency impossibility atomically.
func (s *MutateService) Skip(ctx context.Context, p SkipParams) error {
	if p.TaskIdentity == "" || p.ActorID <= 0 || p.Reason == "" {
		return fmt.Errorf("%w: actor and reason required", ErrInvalidOperation)
	}

	outcome, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		_, id, err := parseIdentity(p.TaskIdentity)
		if err != nil {
			return err
		}
		var status string
		var ingestRunID, ingestStepID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT status, ingest_run_id, ingest_step_id FROM post_ingest_task WHERE id=?`, id).Scan(&status, &ingestRunID, &ingestStepID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: task not found", ErrInvalidOperation)
			}
			return err
		}
		if status != "waiting" {
			return fmt.Errorf("%w: can only skip waiting tasks (got %s)", ErrNotWaiting, status)
		}

		var result sql.Result
		if ingestRunID.Valid && ingestStepID.Valid {
			result, err = tx.ExecContext(ctx,
				`UPDATE post_ingest_task SET status='skipped', lease_owner='', lease_until=NULL,
				 last_error=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
				 WHERE id=? AND status='waiting'`,
				p.Reason, id)
		} else {
			result, err = tx.ExecContext(ctx,
				`UPDATE post_ingest_task SET status='skipped', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
				 WHERE id=? AND status='waiting'`,
				id)
		}
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("%w: skip race", ErrInvalidOperation)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
			 VALUES (?, ?, 'skip', ?, 'waiting', 'skipped')`,
			p.TaskIdentity, p.ActorID, p.Reason); err != nil {
			return err
		}
		if ingestRunID.Valid && ingestStepID.Valid {
			if err := syncLinkedStepTerminalTx(ctx, tx, ingestRunID.Int64, ingestStepID.Int64, "skipped", p.Reason, "waiting"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = outcome
	return nil
}

func syncLinkedStepTerminalTx(ctx context.Context, tx store.ImmediateConnTx, runID, stepID int64, status, reason, expectedStatus string) error {
	result, err := tx.ExecContext(ctx,
		`UPDATE media_ingest_step SET status=?, lease_owner='', lease_until=NULL,
		 last_error=?, finished_at=COALESCE(finished_at, CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND run_id=? AND status=?`,
		status, reason, stepID, runID, expectedStatus)
	if err != nil {
		return fmt.Errorf("taskcontrol: update linked ingest step: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("taskcontrol: linked ingest step rows affected: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("%w: linked ingest step %d in run %d is not %s", ErrInvalidOperation, stepID, runID, expectedStatus)
	}
	if err := publication.FinalizeNodeTransitionTx(ctx, tx, runID); err != nil {
		return fmt.Errorf("taskcontrol: finalize linked ingest step: %w", err)
	}
	return nil
}

// resetLinkedStepTx reopens a linked ingest step after a terminal→waiting reset
// so its orchestration task becomes claimable again. The step returns to
// waiting with its attempts and lease cleared. A required barrier step also
// reopens its ingest run to processing (and, for non-preserve runs, returns the
// media to processing), because claim eligibility for required steps requires a
// processing run. Optional steps keep the run state; their eligibility holds for
// published/degraded runs as long as no required work is pending.
func resetLinkedStepTx(ctx context.Context, tx store.ImmediateConnTx, runID, stepID int64, nextRound int) error {
	var required int
	var runStatus string
	if err := tx.QueryRowContext(ctx, `SELECT required FROM media_ingest_step WHERE id=? AND run_id=?`, stepID, runID).Scan(&required); err != nil {
		return fmt.Errorf("taskcontrol: load linked ingest step: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runStatus); err != nil {
		return fmt.Errorf("taskcontrol: load linked ingest run: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE media_ingest_step SET status='waiting', attempts=0, last_error='', lease_owner=NULL, lease_until=NULL,
		 finished_at=NULL, retry_round=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND run_id=?`, nextRound, stepID, runID)
	if err != nil {
		return fmt.Errorf("taskcontrol: reset linked ingest step: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: linked ingest step %d in run %d", ErrInvalidOperation, stepID, runID)
	}
	if required == 1 && runStatus != "processing" {
		reopened, err := tx.ExecContext(ctx,
			`UPDATE media_ingest_run SET status='processing', finished_at=NULL, error_message='', terminal_reason='', updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND superseded_at IS NULL AND superseded_by_generation IS NULL`, runID)
		if err != nil {
			return fmt.Errorf("taskcontrol: reopen linked ingest run: %w", err)
		}
		if n, _ := reopened.RowsAffected(); n == 1 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE media SET publication_state='processing', publication_error=''
				 WHERE id=(SELECT media_id FROM media_ingest_run WHERE id=?)
				   AND ingest_generation=(SELECT generation FROM media_ingest_run WHERE id=?)
				   AND publication_state IN ('published','degraded','failed')
				   AND NOT EXISTS (SELECT 1 FROM media_ingest_run r WHERE r.id=? AND r.preserve_visibility=1)`,
				runID, runID, runID); err != nil {
				return fmt.Errorf("taskcontrol: reopen linked media publication state: %w", err)
			}
		}
	}
	return publication.FinalizeNodeTransitionTx(ctx, tx, runID)
}

// =============================================================================
// Batch Operations
// =============================================================================

const maxBatchItems = 200

// Batch executes a batch of mutations with per-item idempotency.
// It creates/validates the operation first, then for each item reserves or
// loads its durable outcome and executes through Mutate in one immediate
// item transaction. Complete response material is serialized so replays
// don't depend on changed current state.
func (s *MutateService) Batch(ctx context.Context, p BatchParams) (*BatchResult, error) {
	// Validate UUID
	if _, err := uuid.Parse(p.OperationID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBatchInvalidUUID, err)
	}
	// Deduplicate items
	seen := make(map[string]bool)
	var unique []BatchItem
	for _, item := range p.Items {
		if !seen[item.TaskIdentity] {
			seen[item.TaskIdentity] = true
			unique = append(unique, item)
		}
	}
	if len(unique) > maxBatchItems {
		return nil, fmt.Errorf("%w: got %d items", ErrBatchTooLarge, len(unique))
	}

	// Create/validate operation
	opOutcome, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		// Check if operation already exists
		var existingAction string
		var completedAt sql.NullTime
		err := tx.QueryRowContext(ctx,
			`SELECT action, completed_at FROM task_batch_operation WHERE operation_id=?`,
			p.OperationID).Scan(&existingAction, &completedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			// Operation exists
			if existingAction != p.Action {
				return fmt.Errorf("%w: expected %s, got %s", ErrBatchActionMismatch, existingAction, p.Action)
			}
			if completedAt.Valid {
				// Already completed - just return existing result
				return nil
			}
			return nil
		}
		// Create new operation
		_, err = tx.ExecContext(ctx,
			`INSERT INTO task_batch_operation (operation_id, action, actor_id, reason, requested_count)
			 VALUES (?, ?, ?, ?, ?)`,
			p.OperationID, p.Action, p.ActorID, p.Reason, len(unique))
		return err
	})
	if err != nil {
		return nil, err
	}

	// Check if already completed (return stored result)
	var alreadyCompleted bool
	var existingResult BatchResult
	if opOutcome.CommitAttempted {
		// Check if all items have been processed
		var itemCount int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM task_batch_item WHERE operation_id=?`, p.OperationID).Scan(&itemCount); err == nil && itemCount == len(unique) {
			alreadyCompleted = true
			// Reconstruct result from stored items
			existingResult = s.reconstructBatchResult(ctx, p, unique)
		}
	}
	if alreadyCompleted {
		return &existingResult, nil
	}

	// Process each item independently
	result := &BatchResult{
		OperationID:    p.OperationID,
		Action:         p.Action,
		RequestedCount: len(unique),
	}
	var succeeded, failed int
	for _, item := range unique {
		itemResult := s.executeBatchItem(ctx, p, item)
		result.Items = append(result.Items, itemResult)
		if itemResult.Ok {
			succeeded++
		} else {
			failed++
			if itemResult.OutcomeCode != "permanent_failure" {
				result.Retryable = append(result.Retryable, itemResult)
			}
		}
	}
	result.Succeeded = succeeded
	result.Failed = failed
	result.CompletedAt = time.Now()

	// Mark operation as complete
	_, _ = s.db.ExecContext(ctx,
		`UPDATE task_batch_operation SET completed_at=CURRENT_TIMESTAMP WHERE operation_id=?`,
		p.OperationID)

	return result, nil
}

func (s *MutateService) reconstructBatchResult(ctx context.Context, p BatchParams, items []BatchItem) BatchResult {
	result := BatchResult{
		OperationID:    p.OperationID,
		Action:         p.Action,
		RequestedCount: len(items),
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_identity, ok, outcome_code, COALESCE(result_revision,0), result_json
		 FROM task_batch_item WHERE operation_id=? ORDER BY task_identity`,
		p.OperationID)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var ir BatchItemResult
		var resultJSON string
		if err := rows.Scan(&ir.TaskIdentity, &ir.Ok, &ir.OutcomeCode, &ir.Revision, &resultJSON); err != nil {
			continue
		}
		result.Items = append(result.Items, ir)
		if ir.Ok {
			result.Succeeded++
		} else {
			result.Failed++
			if ir.OutcomeCode != "permanent_failure" {
				result.Retryable = append(result.Retryable, ir)
			}
		}
	}
	return result
}

func (s *MutateService) executeBatchItem(ctx context.Context, p BatchParams, item BatchItem) BatchItemResult {
	ir := BatchItemResult{
		TaskIdentity: item.TaskIdentity,
	}
	// Check if this item already has a stored outcome
	var existingOk bool
	var existingCode string
	var existingRev int64
	err := s.db.QueryRowContext(ctx,
		`SELECT ok, outcome_code, COALESCE(result_revision,0) FROM task_batch_item
		 WHERE operation_id=? AND task_identity=? AND action=?`,
		p.OperationID, item.TaskIdentity, p.Action).Scan(&existingOk, &existingCode, &existingRev)
	if err == nil {
		ir.Ok = existingOk
		ir.OutcomeCode = existingCode
		ir.Revision = existingRev
		return ir
	}

	// Execute the mutation
	var mutateErr error
	var newRev int64
	var notifyAbort bool
	outcome, err := store.WithImmediateConnTx(ctx, s.db, func(tx store.ImmediateConnTx) error {
		switch p.Action {
		case "abort":
			mutateErr = AbortRequestInTx(ctx, tx, item.TaskIdentity, p.ActorID, p.Reason)
			notifyAbort = mutateErr == nil
		case "cancel":
			mutateErr = CancelInTx(ctx, tx, item.TaskIdentity, p.ActorID, p.Reason)
		case "remove":
			mutateErr = RemoveInTx(ctx, tx, item.TaskIdentity, p.ActorID, p.Reason, item.ExpectedRevision)
			if mutateErr == nil {
				var pending int
				_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_abort_intent WHERE task_identity=? AND acknowledged_at IS NULL AND outcome=''`, item.TaskIdentity).Scan(&pending)
				notifyAbort = pending == 1
			}
		case "reset":
			mutateErr = ResetInTx(ctx, tx, item.TaskIdentity, p.ActorID, p.Reason)
			if mutateErr == nil {
				// Get new retry round as revision
				_, id, _ := parseIdentity(item.TaskIdentity)
				_ = tx.QueryRowContext(ctx, `SELECT retry_round FROM post_ingest_task WHERE id=?`, id).Scan(&newRev)
			}
		case "run_now":
			mutateErr = RunNowInTx(ctx, tx, item.TaskIdentity, p.ActorID, p.Reason)
		case "skip":
			mutateErr = SkipInTx(ctx, tx, item.TaskIdentity, p.ActorID, p.Reason)
		case "reopen":
			mutateErr = ReopenInTx(ctx, tx, item.TaskIdentity, p.ActorID, p.Reason)
		default:
			mutateErr = fmt.Errorf("%w: unknown action %s", ErrInvalidOperation, p.Action)
		}
		return mutateErr
	})
	if err != nil {
		if mutateErr != nil {
			ir.Ok = false
			if errors.Is(mutateErr, ErrNotRunning) || errors.Is(mutateErr, ErrNotTerminal) ||
				errors.Is(mutateErr, ErrNotWaiting) || errors.Is(mutateErr, ErrNotAI) {
				ir.OutcomeCode = "permanent_failure"
			} else {
				ir.OutcomeCode = "retryable_failure"
			}
			_ = s.storeItemOutcome(ctx, p, item, ir)
			return ir
		}
		ir.Ok = false
		ir.OutcomeCode = "transaction_error"
		ir.Revision = 0
		_ = s.storeItemOutcome(ctx, p, item, ir)
		return ir
	}
	if outcome.CommitConfirmed && mutateErr == nil && notifyAbort {
		s.notifyAbort(item.TaskIdentity)
	}

	if mutateErr != nil {
		ir.Ok = false
		if errors.Is(mutateErr, ErrNotRunning) || errors.Is(mutateErr, ErrNotTerminal) ||
			errors.Is(mutateErr, ErrNotWaiting) || errors.Is(mutateErr, ErrNotAI) {
			ir.OutcomeCode = "permanent_failure"
		} else {
			ir.OutcomeCode = "retryable_failure"
		}
	} else {
		ir.Ok = true
		ir.OutcomeCode = "success"
		ir.Revision = newRev
	}

	_ = s.storeItemOutcome(ctx, p, item, ir)
	return ir
}

func (s *MutateService) storeItemOutcome(ctx context.Context, p BatchParams, item BatchItem, ir BatchItemResult) error {
	resultJSON := "{}"
	if b, err := json.Marshal(ir); err == nil {
		resultJSON = string(b)
	}
	okVal := 0
	if ir.Ok {
		okVal = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO task_batch_item (operation_id, task_identity, action, request_revision, ok, outcome_code, result_revision, result_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.OperationID, item.TaskIdentity, p.Action, item.ExpectedRevision, okVal, ir.OutcomeCode, ir.Revision, resultJSON)
	return err
}

// =============================================================================
// In-Tx mutation helpers (for batch and direct use within transactions)
// =============================================================================

// AbortRequestInTx persists an abort intent and its audit record atomically.
// Worker notification is deliberately the caller's post-commit responsibility.
func AbortRequestInTx(ctx context.Context, tx store.ImmediateConnTx, taskIdentity string, actorID int64, reason string) error {
	if taskIdentity == "" || actorID <= 0 || reason == "" {
		return fmt.Errorf("%w: actor and reason required", ErrInvalidOperation)
	}
	kind, id, err := parseIdentity(taskIdentity)
	if err != nil {
		return err
	}
	if kind != "orchestration" {
		return fmt.Errorf("%w: abort requires orchestration identity, got %s", ErrInvalidOperation, kind)
	}
	var status, ownerFence string
	if err := tx.QueryRowContext(ctx, `SELECT status, COALESCE(lease_owner,'') FROM post_ingest_task WHERE id=?`, id).Scan(&status, &ownerFence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: task %s not found", ErrNotRunning, taskIdentity)
		}
		return err
	}
	if status != "running" {
		return fmt.Errorf("%w: %s is %s, expected running", ErrNotRunning, taskIdentity, status)
	}
	if ownerFence == "" {
		return fmt.Errorf("%w: running task has no lease owner", ErrUncertainOwner)
	}
	if err := upsertAbortIntentTx(ctx, tx, taskIdentity, ownerFence, actorID, reason); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_control_audit
		(task_identity, actor_id, action, reason, previous_status, new_status)
		VALUES (?, ?, 'abort_request', ?, 'running', 'running')`, taskIdentity, actorID, reason)
	return err
}
func CancelInTx(ctx context.Context, tx store.ImmediateConnTx, taskIdentity string, actorID int64, reason string) error {
	_, id, err := parseIdentity(taskIdentity)
	if err != nil {
		return err
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		return err
	}
	if status != "waiting" {
		return fmt.Errorf("%w: cannot cancel non-waiting task", ErrInvalidOperation)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE post_ingest_task SET status='cancelled', lease_owner='', lease_until=NULL,
		 last_error='cancelled by admin', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='waiting'`, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
		 VALUES (?, ?, 'cancel', ?, 'waiting', 'cancelled')`, taskIdentity, actorID, reason)
	return err
}

func RemoveInTx(ctx context.Context, tx store.ImmediateConnTx, taskIdentity string, actorID int64, reason string, expectedRev int64) error {
	_, id, err := parseIdentity(taskIdentity)
	if err != nil {
		return err
	}
	var status string
	var removedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT status, removed_at FROM post_ingest_task WHERE id=?`, id).Scan(&status, &removedAt); err != nil {
		return err
	}
	if removedAt.Valid {
		return nil // idempotent
	}
	if expectedRev > 0 {
		var rev int64
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(revision,0) FROM task_projection_revision WHERE task_identity=?`, taskIdentity).Scan(&rev)
		if rev > 0 && rev != expectedRev {
			return fmt.Errorf("%w", ErrStaleRevision)
		}
	}
	if status == "running" {
		var ownerFence string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(lease_owner,'') FROM post_ingest_task WHERE id=?`, id).Scan(&ownerFence); err != nil {
			return err
		}
		if ownerFence == "" {
			return fmt.Errorf("%w: running task has no lease owner", ErrUncertainOwner)
		}
		if err := upsertAbortIntentTx(ctx, tx, taskIdentity, ownerFence, actorID, reason); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_control_audit
			(task_identity, actor_id, action, reason, previous_status, new_status)
			VALUES (?, ?, 'remove_abort_request', ?, 'running', 'running')`, taskIdentity, actorID, reason)
		return err
	}
	newStatus := status
	if status == "waiting" {
		newStatus = "cancelled"
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE post_ingest_task SET status=?, removed_at=CURRENT_TIMESTAMP, removed_by=?, remove_reason=?,
		 lease_owner='', lease_until=NULL, finished_at=COALESCE(finished_at,CURRENT_TIMESTAMP)
		 WHERE id=? AND removed_at IS NULL`,
		newStatus, fmt.Sprintf("%d", actorID), reason, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
		 VALUES (?, ?, 'remove', ?, ?, ?)`, taskIdentity, actorID, reason, status, newStatus)
	return err
}

func ResetInTx(ctx context.Context, tx store.ImmediateConnTx, taskIdentity string, actorID int64, reason string) error {
	_, id, err := parseIdentity(taskIdentity)
	if err != nil {
		return err
	}
	var status string
	var retryRound int64
	var ingestRunID, ingestStepID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT status, retry_round, ingest_run_id, ingest_step_id FROM post_ingest_task WHERE id=?`, id).Scan(&status, &retryRound, &ingestRunID, &ingestStepID); err != nil {
		return err
	}
	if !isTerminalStatus(status) {
		return fmt.Errorf("%w: cannot reset non-terminal (%s)", ErrNotTerminal, status)
	}
	nextRound := int(retryRound) + 1
	_, err = tx.ExecContext(ctx,
		`UPDATE post_ingest_task SET status='waiting', last_error='', lease_owner='', lease_until=NULL,
		 started_at=NULL, finished_at=NULL, attempts=0, retry_round=?, available_at=CURRENT_TIMESTAMP
		 WHERE id=? AND retry_round=?`,
		nextRound, id, retryRound)
	if err != nil {
		return err
	}
	if ingestRunID.Valid && ingestStepID.Valid {
		if err := resetLinkedStepTx(ctx, tx, ingestRunID.Int64, ingestStepID.Int64, nextRound); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
		 VALUES (?, ?, 'reset', ?, ?, 'waiting')`, taskIdentity, actorID, reason, status)
	return err
}

func RunNowInTx(ctx context.Context, tx store.ImmediateConnTx, taskIdentity string, actorID int64, reason string) error {
	_, id, err := parseIdentity(taskIdentity)
	if err != nil {
		return err
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		return err
	}
	if status != "waiting" {
		return fmt.Errorf("%w: run-now requires waiting", ErrNotWaiting)
	}
	var maxPri int64
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(base_priority),0) FROM post_ingest_task`).Scan(&maxPri)
	runNowExpires := time.Now().Add(5 * time.Minute)
	_, err = tx.ExecContext(ctx,
		`UPDATE post_ingest_task SET base_priority=?, available_at=datetime('now','-100 years'),
		 run_now_expires=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='waiting'`,
		maxPri+1, runNowExpires, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
		 VALUES (?, ?, 'run_now', ?, 'waiting', 'waiting')`, taskIdentity, actorID, reason)
	return err
}

func SkipInTx(ctx context.Context, tx store.ImmediateConnTx, taskIdentity string, actorID int64, reason string) error {
	_, id, err := parseIdentity(taskIdentity)
	if err != nil {
		return err
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		return err
	}
	if status != "waiting" {
		return fmt.Errorf("%w: skip requires waiting", ErrNotWaiting)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE post_ingest_task SET status='skipped', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='waiting'`, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
		 VALUES (?, ?, 'skip', ?, 'waiting', 'skipped')`, taskIdentity, actorID, reason)
	return err
}

func ReopenInTx(ctx context.Context, tx store.ImmediateConnTx, taskIdentity string, actorID int64, reason string) error {
	_, id, err := parseIdentity(taskIdentity)
	if err != nil {
		return err
	}
	var status, taskType string
	var retryRound int
	if err := tx.QueryRowContext(ctx, `SELECT status, task_type, retry_round FROM post_ingest_task WHERE id=?`, id).Scan(&status, &taskType, &retryRound); err != nil {
		return err
	}
	if taskType != "ai_analysis" {
		return fmt.Errorf("%w: not AI task", ErrNotAI)
	}
	if status != "skipped" {
		return fmt.Errorf("%w: can only reopen skipped AI", ErrInvalidOperation)
	}
	nextRound := retryRound + 1
	_, err = tx.ExecContext(ctx,
		`UPDATE post_ingest_task SET status='waiting', last_error='', lease_owner='', lease_until=NULL,
		 started_at=NULL, finished_at=NULL, attempts=0, retry_round=?, available_at=CURRENT_TIMESTAMP
		 WHERE id=? AND retry_round=? AND task_type='ai_analysis'`,
		nextRound, id, retryRound)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO task_control_audit (task_identity, actor_id, action, reason, previous_status, new_status)
		 VALUES (?, ?, 'reopen', ?, 'skipped', 'waiting')`, taskIdentity, actorID, reason)
	return err
}
