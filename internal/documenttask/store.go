package documenttask

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Store provides persisted access to document conversion tasks.
type Store struct {
	db *sql.DB
}

// NewStore creates a document task Store backed by db.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// EnsureSchema creates the document_task table if it does not exist.
func (s *Store) EnsureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS document_task (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL UNIQUE,
			status TEXT NOT NULL DEFAULT 'waiting',
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until TIMESTAMP,
			generation INTEGER NOT NULL DEFAULT 0,
			retry_round INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 3,
			source_path TEXT NOT NULL DEFAULT '',
			source_hash TEXT NOT NULL DEFAULT '',
			engine_kind TEXT NOT NULL DEFAULT '',
			output_path TEXT NOT NULL DEFAULT '',
			output_size INTEGER NOT NULL DEFAULT 0,
			output_hash TEXT NOT NULL DEFAULT '',
			page_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at TIMESTAMP,
			finished_at TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("document task schema: %w", err)
	}
	return nil
}

// Enqueue inserts a new waiting task or returns a DuplicateError if one exists
// for the same media_id that is not in a terminal state.
func (s *Store) Enqueue(ctx context.Context, mediaID int64, sourcePath string, generation int64) (*Task, error) {
	var existing Task
	err := s.db.QueryRowContext(ctx,
		`SELECT id, media_id, status, lease_owner, generation, retry_round, attempts, source_path, output_path
		 FROM document_task WHERE media_id = ?`, mediaID).Scan(
		&existing.ID, &existing.MediaID, &existing.Status, &existing.LeaseOwner,
		&existing.Generation, &existing.RetryRound, &existing.Attempts,
		&existing.SourcePath, &existing.OutputPath)
	if err == nil {
		if existing.Status == StatusDone && existing.OutputPath != "" {
			return &existing, DuplicateError{ExistingTaskID: existing.ID}
		}
		if existing.Status == StatusWaiting || existing.Status == StatusRunning {
			return &existing, nil
		}
		if existing.Status == StatusFailed || existing.Status == StatusCancelled {
			_, err = s.db.ExecContext(ctx,
				`UPDATE document_task SET status='waiting', lease_owner='', lease_until=NULL,
				 generation=?, source_path=?, retry_round=retry_round+1, attempts=0,
				 last_error='', updated_at=CURRENT_TIMESTAMP
				 WHERE media_id=? AND status IN ('failed','cancelled')`,
				generation, sourcePath, mediaID)
			if err != nil {
				return nil, fmt.Errorf("document task re-enqueue: %w", err)
			}
			return s.GetByMediaID(ctx, mediaID)
		}
		return &existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("document task enqueue check: %w", err)
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO document_task (media_id, status, generation, source_path)
		 VALUES (?, 'waiting', ?, ?)`,
		mediaID, generation, sourcePath)
	if err != nil {
		return nil, fmt.Errorf("document task enqueue insert: %w", err)
	}
	id, _ := result.LastInsertId()
	return s.Get(ctx, id)
}

// Claim atomically claims a waiting task for the given owner.
func (s *Store) Claim(ctx context.Context, owner string, leaseDuration time.Duration) (*Task, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("document task claim: owner required")
	}
	leaseUntil := time.Now().Add(leaseDuration)
	result, err := s.db.ExecContext(ctx,
		`UPDATE document_task SET status='running', lease_owner=?, lease_until=?,
		 attempts=attempts+1, started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id = (SELECT id FROM document_task WHERE status='waiting' ORDER BY id LIMIT 1)
		 AND status='waiting'`,
		owner, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("document task claim: %w", err)
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
		`UPDATE document_task SET lease_until=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		leaseUntil, taskID, owner)
	if err != nil {
		return fmt.Errorf("document task heartbeat: %w", err)
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
		`UPDATE document_task SET status='cancelled', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		taskID, owner)
	if err != nil {
		return fmt.Errorf("document task cancel: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "cancel on non-owned task"}
	}
	return nil
}

// CommitDone marks a task as done with the output evidence, validated under lease/generation fencing.
func (s *Store) CommitDone(ctx context.Context, taskID int64, owner string, generation int64, output ConvertOutput, engine EngineKind) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE document_task SET status='done', output_path=?, output_size=?, output_hash=?,
		 page_count=?, engine_kind=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=? AND generation=?`,
		output.PDFPath, output.PDFSize, output.PDFHash, output.PageCount,
		string(engine), taskID, owner, generation)
	if err != nil {
		return fmt.Errorf("document task commit done: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "commit on non-owned or stale generation task"}
	}
	return nil
}

// MarkFailed marks a task as failed with the given error message.
func (s *Store) MarkFailed(ctx context.Context, taskID int64, owner string, errMsg string) error {
	errMsg = strings.TrimSpace(errMsg)
	if len(errMsg) > 2000 {
		errMsg = errMsg[:2000]
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE document_task SET status='failed', last_error=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`,
		errMsg, taskID, owner)
	if err != nil {
		return fmt.Errorf("document task mark failed: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "mark failed on non-owned task"}
	}
	return nil
}

// ResetStuckRunning resets tasks that have been running past their lease to waiting for recovery.
func (s *Store) ResetStuckRunning(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE document_task SET status='waiting', lease_owner='', lease_until=NULL, updated_at=CURRENT_TIMESTAMP
		 WHERE status='running' AND lease_until < CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, fmt.Errorf("document task reset stuck: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// Get returns a task by id.
func (s *Store) Get(ctx context.Context, id int64) (*Task, error) {
	return scanTask(s.db.QueryRowContext(ctx,
		`SELECT id, media_id, status, lease_owner, lease_until, generation, retry_round, attempts,
		 max_attempts, source_path, source_hash, engine_kind, output_path, output_size, output_hash,
		 page_count, last_error, created_at, updated_at, started_at, finished_at
		 FROM document_task WHERE id=?`, id))
}

// GetByMediaID returns the task for a media item.
func (s *Store) GetByMediaID(ctx context.Context, mediaID int64) (*Task, error) {
	return scanTask(s.db.QueryRowContext(ctx,
		`SELECT id, media_id, status, lease_owner, lease_until, generation, retry_round, attempts,
		 max_attempts, source_path, source_hash, engine_kind, output_path, output_size, output_hash,
		 page_count, last_error, created_at, updated_at, started_at, finished_at
		 FROM document_task WHERE media_id=?`, mediaID))
}

// GetByOwner returns a running task owned by the given owner.
func (s *Store) GetByOwner(ctx context.Context, owner string) (*Task, error) {
	return scanTask(s.db.QueryRowContext(ctx,
		`SELECT id, media_id, status, lease_owner, lease_until, generation, retry_round, attempts,
		 max_attempts, source_path, source_hash, engine_kind, output_path, output_size, output_hash,
		 page_count, last_error, created_at, updated_at, started_at, finished_at
		 FROM document_task WHERE status='running' AND lease_owner=? LIMIT 1`, owner))
}

func scanTask(row *sql.Row) (*Task, error) {
	var t Task
	var leaseUntil, startedAt, finishedAt sql.NullTime
	if err := row.Scan(&t.ID, &t.MediaID, &t.Status, &t.LeaseOwner, &leaseUntil,
		&t.Generation, &t.RetryRound, &t.Attempts, &t.MaxAttempts,
		&t.SourcePath, &t.SourceHash, &t.EngineKind, &t.OutputPath,
		&t.OutputSize, &t.OutputHash, &t.PageCount, &t.LastError,
		&t.CreatedAt, &t.UpdatedAt, &startedAt, &finishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFoundError{}
		}
		return nil, fmt.Errorf("scan document task: %w", err)
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

// --- Fulltext store methods ---

// EnsureFulltextSchema creates the document_fulltext table.
func (s *Store) EnsureFulltextSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS document_fulltext (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL UNIQUE,
			status TEXT NOT NULL DEFAULT 'waiting',
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until TIMESTAMP,
			generation INTEGER NOT NULL DEFAULT 0,
			retry_round INTEGER NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 3,
			max_pages INTEGER NOT NULL DEFAULT 100,
			max_bytes INTEGER NOT NULL DEFAULT 0,
			mode TEXT NOT NULL DEFAULT '',
			language TEXT NOT NULL DEFAULT '',
			engine TEXT NOT NULL DEFAULT '',
			engine_version TEXT NOT NULL DEFAULT '',
			source_hash TEXT NOT NULL DEFAULT '',
			text_hash TEXT NOT NULL DEFAULT '',
			text_preview TEXT NOT NULL DEFAULT '',
			text_size INTEGER NOT NULL DEFAULT 0,
			page_count INTEGER NOT NULL DEFAULT 0,
			page_coverage INTEGER NOT NULL DEFAULT 0,
			fts_entity TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at TIMESTAMP,
			finished_at TIMESTAMP
		)
	`)
	return err
}

// EnqueueFulltext inserts a new fulltext task or returns an error if one exists.
func (s *Store) EnqueueFulltext(ctx context.Context, input FulltextInput) (*FulltextTask, error) {
	var existing FulltextTask
	err := s.db.QueryRowContext(ctx,
		`SELECT id, status FROM document_fulltext WHERE media_id=?`, input.MediaID).Scan(&existing.ID, &existing.Status)
	if err == nil {
		if existing.Status == StatusDone || existing.Status == StatusRunning || existing.Status == StatusWaiting {
			return nil, DuplicateError{}
		}
		_, err = s.db.ExecContext(ctx,
			`UPDATE document_fulltext SET status='waiting', lease_owner='', lease_until=NULL,
			 generation=?, retry_round=retry_round+1, attempts=0, last_error='', updated_at=CURRENT_TIMESTAMP
			 WHERE media_id=? AND status IN ('failed','cancelled')`,
			input.Generation, input.MediaID)
		if err != nil {
			return nil, err
		}
		return s.GetFulltextByMediaID(ctx, input.MediaID)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO document_fulltext(media_id,status,generation,language,max_pages,max_bytes)
		 VALUES(?,'waiting',?,?,?,?)`,
		input.MediaID, input.Generation, input.Language, input.MaxPages, input.MaxBytes)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return s.GetFulltext(ctx, id)
}

// ClaimFulltext atomically claims a waiting fulltext task.
func (s *Store) ClaimFulltext(ctx context.Context, owner string, leaseDuration time.Duration) (*FulltextTask, error) {
	leaseUntil := time.Now().Add(leaseDuration)
	result, err := s.db.ExecContext(ctx,
		`UPDATE document_fulltext SET status='running', lease_owner=?, lease_until=?,
		 attempts=attempts+1, started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=(SELECT id FROM document_fulltext WHERE status='waiting' ORDER BY id LIMIT 1)
		 AND status='waiting'`, owner, leaseUntil)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	return s.GetFulltextByOwner(ctx, owner)
}

// CommitFulltextDone marks a fulltext task as done with the extraction result.
func (s *Store) CommitFulltextDone(ctx context.Context, taskID int64, owner string, generation int64, ftResult FulltextResult, engine, engineVersion string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE document_fulltext SET status='done', mode=?, language=?, engine=?, engine_version=?,
		 text_hash=?, text_preview=?, text_size=?, page_count=?, page_coverage=?,
		 finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=? AND generation=?`,
		string(ftResult.Mode), ftResult.Language, engine, engineVersion,
		ftResult.TextHash, ftResult.TextPreview, ftResult.TextSize, ftResult.PageCount, ftResult.PageCoverage,
		taskID, owner, generation)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "commit fulltext on non-owned or stale generation task"}
	}
	return nil
}

// CancelFulltext cancels a running fulltext task.
func (s *Store) CancelFulltext(ctx context.Context, taskID int64, owner string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE document_fulltext SET status='cancelled', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status='running' AND lease_owner=?`, taskID, owner)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return FenceError{Reason: "cancel fulltext on non-owned task"}
	}
	return nil
}

// GetFulltext returns a fulltext task by id.
func (s *Store) GetFulltext(ctx context.Context, id int64) (*FulltextTask, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id,media_id,status,lease_owner,lease_until,generation,retry_round,attempts,
		 max_attempts,max_pages,max_bytes,mode,language,engine,engine_version,source_hash,text_hash,text_preview,
		 text_size,page_count,page_coverage,fts_entity,last_error,created_at,updated_at,
		 started_at,finished_at FROM document_fulltext WHERE id=?`, id)
	var t FulltextTask
	var lu, sa, fa sql.NullTime
	err := row.Scan(&t.ID, &t.MediaID, &t.Status, &t.LeaseOwner, &lu, &t.Generation,
		&t.RetryRound, &t.Attempts, &t.MaxAttempts, &t.MaxPages, &t.MaxBytes,
		&t.Mode, &t.Language,
		&t.Engine, &t.EngineVersion, &t.SourceHash, &t.TextHash, &t.TextPreview,
		&t.TextSize, &t.PageCount, &t.PageCoverage, &t.FTSEntity, &t.LastError,
		&t.CreatedAt, &t.UpdatedAt, &sa, &fa)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, NotFoundError{}
		}
		return nil, err
	}
	if lu.Valid {
		t.LeaseUntil = lu.Time
	}
	if sa.Valid {
		t.StartedAt = &sa.Time
	}
	if fa.Valid {
		t.FinishedAt = &fa.Time
	}
	return &t, nil
}

// GetFulltextByMediaID returns the fulltext task for a media item.
func (s *Store) GetFulltextByMediaID(ctx context.Context, mediaID int64) (*FulltextTask, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM document_fulltext WHERE media_id=?`, mediaID).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetFulltext(ctx, id)
}

// GetFulltextByOwner returns a running fulltext task for the owner.
func (s *Store) GetFulltextByOwner(ctx context.Context, owner string) (*FulltextTask, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM document_fulltext WHERE status='running' AND lease_owner=? LIMIT 1`, owner).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetFulltext(ctx, id)
}

// ListCompleteFulltext returns done fulltext tasks for the given media IDs.
func (s *Store) ListCompleteFulltext(ctx context.Context, mediaIDs []int64) ([]FulltextTask, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	query := `SELECT id FROM document_fulltext WHERE status='done' AND media_id IN (`
	args := make([]interface{}, len(mediaIDs))
	for i, id := range mediaIDs {
		if i > 0 {
			query += ","
		}
		query += "?"
		args[i] = id
	}
	query += ")"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []FulltextTask
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		t, err := s.GetFulltext(ctx, id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *t)
	}
	return tasks, rows.Err()
}
