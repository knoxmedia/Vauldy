package personscrape

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Store provides persisted access to person scrape tasks.
type Store struct {
	db *sql.DB
}

// NewStore creates a person scrape Store backed by db.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// EnsureSchema creates the person_scrape_task table if it does not exist.
func (s *Store) EnsureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS person_scrape_task (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			person_subject_id INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'waiting',
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until TIMESTAMP,
			method TEXT NOT NULL DEFAULT '',
			query_name TEXT NOT NULL DEFAULT '',
			external_id TEXT NOT NULL DEFAULT '',
			provider_source TEXT NOT NULL DEFAULT '',
			api_key TEXT NOT NULL DEFAULT '',
			language TEXT NOT NULL DEFAULT '',
			generation INTEGER NOT NULL DEFAULT 0,
			retry_round INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 3,
			ambiguity TEXT NOT NULL DEFAULT '',
			profile_json TEXT NOT NULL DEFAULT '',
			avatar_path TEXT NOT NULL DEFAULT '',
			avatar_staged TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at TIMESTAMP,
			finished_at TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("person scrape task schema: %w", err)
	}
	return nil
}

// Enqueue inserts a new waiting task or returns a DuplicateError if one exists
// for the same person_subject_id that is done. Failed/cancelled tasks are re-enqueued.
func (s *Store) Enqueue(ctx context.Context, input TaskInput) (*Task, error) {
	existing, err := s.GetByPersonSubject(ctx, input.PersonSubjectID)
	if err == nil && existing != nil {
		if existing.Status == StatusDone {
			return existing, DuplicateError{ExistingTaskID: existing.ID}
		}
		if existing.Status == StatusWaiting || existing.Status == StatusRunning {
			return existing, nil
		}
		// Re-enqueue failed/cancelled.
		_, err = s.db.ExecContext(ctx,
			`UPDATE person_scrape_task SET status='waiting', lease_owner='', lease_until=NULL,
			 generation=?, retry_round=retry_round+1, attempts=0, last_error='',
			 query_name=?, external_id=?, method=?, provider_source=?, api_key=?,
			 language=?, ambiguity='', updated_at=CURRENT_TIMESTAMP
			 WHERE person_subject_id=? AND status IN ('failed','cancelled')`,
			input.Generation, input.QueryName, input.ExternalID, string(input.Method),
			input.ProviderSource, input.APIKey, input.Language, input.PersonSubjectID)
		if err != nil {
			return nil, fmt.Errorf("person scrape re-enqueue: %w", err)
		}
		return s.GetByPersonSubject(ctx, input.PersonSubjectID)
	}
	if err != nil && !errors.Is(err, NotFoundError{}) {
		return nil, fmt.Errorf("person scrape enqueue check: %w", err)
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO person_scrape_task (person_subject_id, status, generation,
		 query_name, external_id, method, provider_source, api_key, language)
		 VALUES (?, 'waiting', ?, ?, ?, ?, ?, ?, ?)`,
		input.PersonSubjectID, input.Generation, input.QueryName, input.ExternalID,
		string(input.Method), input.ProviderSource, input.APIKey, input.Language)
	if err != nil {
		return nil, fmt.Errorf("person scrape enqueue insert: %w", err)
	}
	id, _ := result.LastInsertId()
	return s.Get(ctx, id)
}

// Claim atomically claims a waiting task for the given owner.
func (s *Store) Claim(ctx context.Context, owner string, leaseDuration time.Duration) (*Task, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("person scrape claim: owner required")
	}
	leaseUntil := time.Now().Add(leaseDuration)
	result, err := s.db.ExecContext(ctx,
		`UPDATE person_scrape_task SET status='running', lease_owner=?, lease_until=?,
		 attempts=attempts+1, started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id = (SELECT id FROM person_scrape_task WHERE status='waiting' ORDER BY id LIMIT 1)
		 AND status='waiting'`,
		owner, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("person scrape claim: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	return s.GetByOwner(ctx, owner)
}

// Heartbeat extends the lease for a running task owned by the caller.
func (s *Store) Heartbeat(ctx context.Context, taskID int64, owner string, leaseDuration time.Duration) error {
	leaseUntil := time.Now().Add(leaseDuration)
	result, err := s.db.ExecContext(ctx,
		`UPDATE person_scrape_task SET lease_until=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		leaseUntil, taskID, owner)
	if err != nil {
		return fmt.Errorf("person scrape heartbeat: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "heartbeat on non-owned task"}
	}
	return nil
}

// Cancel marks a task as cancelled if owned by the caller.
func (s *Store) Cancel(ctx context.Context, taskID int64, owner string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE person_scrape_task SET status='cancelled', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		taskID, owner)
	if err != nil {
		return fmt.Errorf("person scrape cancel: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "cancel on non-owned task"}
	}
	return nil
}

// CommitDone marks a task as done with the profile result, validated under
// lease/generation fencing. The person deletion fence requires the person
// still exists at commit time.
func (s *Store) CommitDone(ctx context.Context, taskID int64, owner string, generation int64, result PersonResult) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE person_scrape_task SET status='done', profile_json=?, avatar_path=?,
		 finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=? AND generation=?`,
		result.ProfileJSON, result.AvatarPath, taskID, owner, generation)
	if err != nil {
		return fmt.Errorf("person scrape commit done: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "commit on non-owned or stale generation task"}
	}
	return nil
}

// MarkFailed marks a task as failed with the given error message.
func (s *Store) MarkFailed(ctx context.Context, taskID int64, owner string, errMsg string, ambiguity AmbiguityLevel) error {
	errMsg = strings.TrimSpace(errMsg)
	if len(errMsg) > 2000 {
		errMsg = errMsg[:2000]
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE person_scrape_task SET status='failed', last_error=?, ambiguity=?,
		 finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		errMsg, string(ambiguity), taskID, owner)
	if err != nil {
		return fmt.Errorf("person scrape mark failed: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "mark failed on non-owned task"}
	}
	return nil
}

// SetAvatarStaged records the staged avatar path.
func (s *Store) SetAvatarStaged(ctx context.Context, taskID int64, owner string, stagedPath string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE person_scrape_task SET avatar_staged=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		stagedPath, taskID, owner)
	if err != nil {
		return fmt.Errorf("person scrape avatar staged: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "avatar staged on non-owned task"}
	}
	return nil
}

// ResetStuckRunning resets tasks that have been running past their lease to waiting.
func (s *Store) ResetStuckRunning(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE person_scrape_task SET status='waiting', lease_owner='', lease_until=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE status='running' AND lease_until < CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, fmt.Errorf("person scrape reset stuck: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// DeleteByPersonSubject removes all task records for a person subject (person deletion fence).
func (s *Store) DeleteByPersonSubject(ctx context.Context, personSubjectID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM person_scrape_task WHERE person_subject_id=?`, personSubjectID)
	if err != nil {
		return fmt.Errorf("person scrape delete by subject: %w", err)
	}
	return nil
}

// Get returns a task by id.
func (s *Store) Get(ctx context.Context, id int64) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, person_subject_id, status, lease_owner, lease_until,
		 method, query_name, external_id, provider_source, api_key, language,
		 generation, retry_round, attempts, max_attempts, ambiguity,
		 profile_json, avatar_path, avatar_staged, last_error,
		 created_at, updated_at, started_at, finished_at
		 FROM person_scrape_task WHERE id=?`, id)
	return scanTask(row)
}

// GetByPersonSubject returns the task for a person subject.
func (s *Store) GetByPersonSubject(ctx context.Context, personSubjectID int64) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, person_subject_id, status, lease_owner, lease_until,
		 method, query_name, external_id, provider_source, api_key, language,
		 generation, retry_round, attempts, max_attempts, ambiguity,
		 profile_json, avatar_path, avatar_staged, last_error,
		 created_at, updated_at, started_at, finished_at
		 FROM person_scrape_task WHERE person_subject_id=?`, personSubjectID)
	return scanTask(row)
}

// GetByOwner returns a running task owned by the given owner.
func (s *Store) GetByOwner(ctx context.Context, owner string) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, person_subject_id, status, lease_owner, lease_until,
		 method, query_name, external_id, provider_source, api_key, language,
		 generation, retry_round, attempts, max_attempts, ambiguity,
		 profile_json, avatar_path, avatar_staged, last_error,
		 created_at, updated_at, started_at, finished_at
		 FROM person_scrape_task WHERE status='running' AND lease_owner=? LIMIT 1`, owner)
	return scanTask(row)
}

func scanTask(row *sql.Row) (*Task, error) {
	var t Task
	var leaseUntil, startedAt, finishedAt sql.NullTime
	if err := row.Scan(&t.ID, &t.PersonSubjectID, &t.Status, &t.LeaseOwner, &leaseUntil,
		&t.Method, &t.QueryName, &t.ExternalID, &t.ProviderSource, &t.APIKey, &t.Language,
		&t.Generation, &t.RetryRound, &t.Attempts, &t.MaxAttempts, &t.Ambiguity,
		&t.ProfileJSON, &t.AvatarPath, &t.AvatarStaged, &t.LastError,
		&t.CreatedAt, &t.UpdatedAt, &startedAt, &finishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFoundError{}
		}
		return nil, fmt.Errorf("scan person scrape task: %w", err)
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
