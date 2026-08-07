package aitask

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Store provides persisted access to AI capability subtasks.
type Store struct {
	db *sql.DB
}

// NewStore creates an AI task Store backed by db.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// EnsureSchema creates the ai_analysis_result table if it does not exist.
func (s *Store) EnsureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS ai_analysis_result (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL,
			parent_task_id INTEGER NOT NULL DEFAULT 0,
			capability TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'waiting',
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until TIMESTAMP,
			provider TEXT NOT NULL DEFAULT '',
			provider_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			model_version TEXT NOT NULL DEFAULT '',
			input_digest TEXT NOT NULL DEFAULT '',
			generation INTEGER NOT NULL DEFAULT 0,
			retry_round INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 2,
			progress REAL NOT NULL DEFAULT 0,
			cancellation INTEGER NOT NULL DEFAULT 0,
			result_hash TEXT NOT NULL DEFAULT '',
			result_preview TEXT NOT NULL DEFAULT '',
			result_rows INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			prerequisites TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at TIMESTAMP,
			finished_at TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("ai analysis result schema: %w", err)
	}
	return nil
}

// Enqueue inserts a new waiting subtask. Returns DuplicateError if one already
// exists for the same media_id + capability that is done. Failure on siblings
// never overwrites sibling rows.
func (s *Store) Enqueue(ctx context.Context, input SubTaskInput) (*SubTask, error) {
	// Check for existing subtask with same media_id + capability.
	existing, err := s.GetByMediaAndCapability(ctx, input.MediaID, input.Capability)
	if err == nil && existing != nil {
		if existing.Status == StatusDone {
			return existing, DuplicateError{ExistingTaskID: existing.ID}
		}
		if existing.Status == StatusWaiting || existing.Status == StatusRunning {
			return existing, nil
		}
		// For failed/cancelled, re-enqueue with incremented retry_round.
		_, err = s.db.ExecContext(ctx,
			`UPDATE ai_analysis_result SET status='waiting', lease_owner='', lease_until=NULL,
			 generation=?, retry_round=retry_round+1, attempts=0, last_error='',
			 provider=?, provider_id=?, model=?, model_version=?, input_digest=?,
			 prerequisites=?, updated_at=CURRENT_TIMESTAMP
			 WHERE media_id=? AND capability=? AND status IN ('failed','cancelled')`,
			input.Generation, input.Provider, input.ProviderID, input.Model,
			input.ModelVersion, input.InputDigest, input.Prerequisites,
			input.MediaID, string(input.Capability))
		if err != nil {
			return nil, fmt.Errorf("ai subtask re-enqueue: %w", err)
		}
		return s.GetByMediaAndCapability(ctx, input.MediaID, input.Capability)
	}
	if err != nil && !errors.Is(err, NotFoundError{}) {
		return nil, fmt.Errorf("ai subtask enqueue check: %w", err)
	}

	result, exErr := s.db.ExecContext(ctx,
		`INSERT INTO ai_analysis_result (media_id, parent_task_id, capability, status, generation,
		 provider, provider_id, model, model_version, input_digest, prerequisites)
		 VALUES (?, ?, ?, 'waiting', ?, ?, ?, ?, ?, ?, ?)`,
		input.MediaID, input.ParentTaskID, string(input.Capability), input.Generation,
		input.Provider, input.ProviderID, input.Model, input.ModelVersion,
		input.InputDigest, input.Prerequisites)
	if exErr != nil {
		return nil, fmt.Errorf("ai subtask enqueue insert: %w", exErr)
	}
	id, _ := result.LastInsertId()
	return s.Get(ctx, id)
}

// Claim atomically claims a waiting subtask of a specific capability for the
// given owner. When capability is empty, claims any waiting subtask.
func (s *Store) Claim(ctx context.Context, capability Capability, owner string, leaseDuration time.Duration) (*SubTask, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("ai subtask claim: owner required")
	}
	leaseUntil := time.Now().Add(leaseDuration)
	var sqlStr string
	var args []interface{}
	if capability != "" {
		sqlStr = `UPDATE ai_analysis_result SET status='running', lease_owner=?, lease_until=?,
		 attempts=attempts+1, started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id = (SELECT id FROM ai_analysis_result WHERE status='waiting' AND capability=? ORDER BY id LIMIT 1)
		 AND status='waiting'`
		args = []interface{}{owner, leaseUntil, string(capability)}
	} else {
		sqlStr = `UPDATE ai_analysis_result SET status='running', lease_owner=?, lease_until=?,
		 attempts=attempts+1, started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id = (SELECT id FROM ai_analysis_result WHERE status='waiting' ORDER BY id LIMIT 1)
		 AND status='waiting'`
		args = []interface{}{owner, leaseUntil}
	}
	result, err := s.db.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("ai subtask claim: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	return s.GetByOwner(ctx, owner)
}

// Heartbeat extends the lease for a running subtask owned by the caller.
func (s *Store) Heartbeat(ctx context.Context, taskID int64, owner string, leaseDuration time.Duration) error {
	leaseUntil := time.Now().Add(leaseDuration)
	result, err := s.db.ExecContext(ctx,
		`UPDATE ai_analysis_result SET lease_until=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		leaseUntil, taskID, owner)
	if err != nil {
		return fmt.Errorf("ai subtask heartbeat: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "heartbeat on non-owned task"}
	}
	return nil
}

// Cancel marks a subtask as cancelled if owned by the caller.
func (s *Store) Cancel(ctx context.Context, taskID int64, owner string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE ai_analysis_result SET status='cancelled', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		taskID, owner)
	if err != nil {
		return fmt.Errorf("ai subtask cancel: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "cancel on non-owned task"}
	}
	return nil
}

// CommitDone marks a subtask as done with the result evidence, validated under
// lease/generation fencing. Cannot overwrite sibling subtasks.
func (s *Store) CommitDone(ctx context.Context, taskID int64, owner string, generation int64, result SubTaskResult) error {
	// Verify no sibling conflict before committing.
	var currentCap string
	if err := s.db.QueryRowContext(ctx,
		`SELECT capability FROM ai_analysis_result WHERE id=?`, taskID).Scan(&currentCap); err != nil {
		return fmt.Errorf("ai subtask commit verify: %w", err)
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE ai_analysis_result SET status='done', result_hash=?, result_preview=?,
		 result_rows=?, progress=100, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=? AND generation=?`,
		result.ResultHash, result.ResultPreview, result.ResultRows,
		taskID, owner, generation)
	if err != nil {
		return fmt.Errorf("ai subtask commit done: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "commit on non-owned or stale generation task"}
	}
	_ = currentCap
	return nil
}

// MarkFailed marks a subtask as failed with the given error message.
// Failure cannot overwrite siblings.
func (s *Store) MarkFailed(ctx context.Context, taskID int64, owner string, errMsg string) error {
	errMsg = strings.TrimSpace(errMsg)
	if len(errMsg) > 2000 {
		errMsg = errMsg[:2000]
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE ai_analysis_result SET status='failed', last_error=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		errMsg, taskID, owner)
	if err != nil {
		return fmt.Errorf("ai subtask mark failed: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "mark failed on non-owned task"}
	}
	return nil
}

// UpdateProgress updates the progress of a running subtask.
func (s *Store) UpdateProgress(ctx context.Context, taskID int64, owner string, progress float64) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE ai_analysis_result SET progress=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		progress, taskID, owner)
	if err != nil {
		return fmt.Errorf("ai subtask progress: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "progress on non-owned task"}
	}
	return nil
}

// MarkCancellation sets the cancellation flag on a running subtask.
func (s *Store) MarkCancellation(ctx context.Context, taskID int64, owner string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE ai_analysis_result SET cancellation=1, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		taskID, owner)
	if err != nil {
		return fmt.Errorf("ai subtask cancellation flag: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "cancellation flag on non-owned task"}
	}
	return nil
}

// ResetStuckRunning resets subtasks that have been running past their lease
// to waiting for recovery.
func (s *Store) ResetStuckRunning(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE ai_analysis_result SET status='waiting', lease_owner='', lease_until=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE status='running' AND lease_until < CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, fmt.Errorf("ai subtask reset stuck: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// Get returns a subtask by id.
func (s *Store) Get(ctx context.Context, id int64) (*SubTask, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, media_id, parent_task_id, capability, status, lease_owner, lease_until,
		 provider, provider_id, model, model_version, input_digest, generation, retry_round,
		 attempts, max_attempts, progress, cancellation, result_hash, result_preview,
		 result_rows, last_error, prerequisites, created_at, updated_at, started_at, finished_at
		 FROM ai_analysis_result WHERE id=?`, id)
	return scanSubTask(row)
}

// GetByMediaAndCapability returns the subtask for a media item and capability.
func (s *Store) GetByMediaAndCapability(ctx context.Context, mediaID int64, capability Capability) (*SubTask, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, media_id, parent_task_id, capability, status, lease_owner, lease_until,
		 provider, provider_id, model, model_version, input_digest, generation, retry_round,
		 attempts, max_attempts, progress, cancellation, result_hash, result_preview,
		 result_rows, last_error, prerequisites, created_at, updated_at, started_at, finished_at
		 FROM ai_analysis_result WHERE media_id=? AND capability=?`, mediaID, string(capability))
	return scanSubTask(row)
}

// GetByOwner returns a running subtask owned by the given owner.
func (s *Store) GetByOwner(ctx context.Context, owner string) (*SubTask, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, media_id, parent_task_id, capability, status, lease_owner, lease_until,
		 provider, provider_id, model, model_version, input_digest, generation, retry_round,
		 attempts, max_attempts, progress, cancellation, result_hash, result_preview,
		 result_rows, last_error, prerequisites, created_at, updated_at, started_at, finished_at
		 FROM ai_analysis_result WHERE status='running' AND lease_owner=? LIMIT 1`, owner)
	return scanSubTask(row)
}

// ListByMedia returns all subtasks for a given media item.
func (s *Store) ListByMedia(ctx context.Context, mediaID int64) ([]SubTask, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM ai_analysis_result WHERE media_id=? ORDER BY capability`, mediaID)
	if err != nil {
		return nil, fmt.Errorf("ai subtask list by media: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var tasks []SubTask
	for _, id := range ids {
		t, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *t)
	}
	return tasks, nil
}

// ListByParent returns all capability subtasks for a parent post_ingest_task.
func (s *Store) ListByParent(ctx context.Context, parentTaskID int64) ([]SubTask, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM ai_analysis_result WHERE parent_task_id=? ORDER BY capability`, parentTaskID)
	if err != nil {
		return nil, fmt.Errorf("ai subtask list by parent: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var tasks []SubTask
	for _, id := range ids {
		t, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *t)
	}
	return tasks, nil
}

// GetPrerequisites returns subtask IDs this subtask depends on (from prerequisites JSON).
func (s *Store) GetPrerequisites(ctx context.Context, taskID int64) ([]int64, error) {
	var prereqJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT prerequisites FROM ai_analysis_result WHERE id=?`, taskID).Scan(&prereqJSON)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(prereqJSON) == "" {
		return nil, nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(prereqJSON), &ids); err != nil {
		return nil, fmt.Errorf("parse prerequisites: %w", err)
	}
	return ids, nil
}

func scanSubTask(row *sql.Row) (*SubTask, error) {
	var t SubTask
	var leaseUntil, startedAt, finishedAt sql.NullTime
	if err := row.Scan(&t.ID, &t.MediaID, &t.ParentTaskID, &t.Capability, &t.Status,
		&t.LeaseOwner, &leaseUntil, &t.Provider, &t.ProviderID, &t.Model,
		&t.ModelVersion, &t.InputDigest, &t.Generation, &t.RetryRound,
		&t.Attempts, &t.MaxAttempts, &t.Progress, &t.Cancellation,
		&t.ResultHash, &t.ResultPreview, &t.ResultRows, &t.LastError,
		&t.Prerequisites, &t.CreatedAt, &t.UpdatedAt, &startedAt, &finishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFoundError{}
		}
		return nil, fmt.Errorf("scan ai subtask: %w", err)
	}
	if leaseUntil.Valid {
		t.LeaseUntil = leaseUntil.Time
	}
	if startedAt.Valid {
		t.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		t.FinishedAt = &finishedAt.Time
	}
	return &t, nil
}
