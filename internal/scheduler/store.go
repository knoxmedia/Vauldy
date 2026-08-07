package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PolicyRevision is a persisted scheduler policy revision record.
type PolicyRevision struct {
	ID               int64
	SchemaVersion    int
	ParentRevisionID *int64
	PolicyJSON       string
	Author           string
	Reason           string
	ValidationHash   string
	IsActive         bool
	CreatedAt        time.Time
	ActivatedAt      *time.Time
}

// ControlState is a per-type control record.
type ControlState struct {
	TaskType  string
	State     string
	Revision  int
	UpdatedAt time.Time
}

// FairnessCursor is a per-type fairness tracking record.
type FairnessCursor struct {
	TaskType      string
	LastLibraryID *int64
	Initialized   bool
	Revision      int64
	UpdatedAt     time.Time
}

// Reservation is a lease-bound resource reservation.
type Reservation struct {
	ID               int64
	ExecutionID      string
	TaskType         string
	ReservedUnits    int
	PolicyRevisionID int64
	Status           string
	LeaseUntil       *time.Time
	ReleasedAt       *time.Time
	ReleaseReason    string
	ReleasedBy       string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// AuditEntry is an append-only audit record.
type AuditEntry struct {
	ID         int64
	EventType  string
	Actor      string
	DetailJSON string
	CreatedAt  time.Time
}

// Store provides access to persisted scheduler policy revision, control,
// fairness, reservation, and audit records.
type Store struct {
	db *sql.DB
}

// NewStore creates a scheduler Store backed by db.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreatePolicyRevision inserts a new inactive policy revision.
func (s *Store) CreatePolicyRevision(ctx context.Context, schemaVersion int, parentRevisionID *int64, policyJSON, author, reason, validationHash string) (*PolicyRevision, error) {
	var parentID interface{}
	if parentRevisionID != nil {
		parentID = *parentRevisionID
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduler_policy_revision(schema_version,parent_revision_id,policy_json,author,reason,validation_hash) VALUES(?,?,?,?,?,?)`,
		schemaVersion, parentID, policyJSON, author, reason, validationHash)
	if err != nil {
		return nil, fmt.Errorf("create policy revision: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return s.GetPolicyRevision(ctx, id)
}

// ActivatePolicyRevision activates a policy revision using optimistic
// expectedRevision within a BEGIN IMMEDIATE transaction. If expectedRevision
// is -1, activation succeeds only if no revision is currently active.
func (s *Store) ActivatePolicyRevision(ctx context.Context, revisionID int64, expectedRevision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentActive sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM scheduler_policy_revision WHERE is_active=1`).Scan(&currentActive); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("query active: %w", err)
	}
	if expectedRevision == -1 {
		if currentActive.Valid {
			return fmt.Errorf("optimistic activation conflict: expected no active revision, found id=%d", currentActive.Int64)
		}
	} else {
		if !currentActive.Valid || currentActive.Int64 != expectedRevision {
			return fmt.Errorf("optimistic activation conflict: expected=%d actual=%v", expectedRevision, currentActive)
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE scheduler_policy_revision SET is_active=0 WHERE is_active=1`); err != nil {
		return fmt.Errorf("deactivate current: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE scheduler_policy_revision SET is_active=1, activated_at=CURRENT_TIMESTAMP WHERE id=?`, revisionID); err != nil {
		return fmt.Errorf("activate revision %d: %w", revisionID, err)
	}
	return tx.Commit()
}

// ApplyPolicyRevisionParams carries all inputs for one atomic policy apply.
type ApplyPolicyRevisionParams struct {
	SchemaVersion    int
	ParentRevisionID *int64
	PolicyJSON       string
	Author           string
	Reason           string
	ValidationHash   string
	ExpectedRevision int64 // -1 means no active revision may exist
	AuditEventType   string
	AuditDetailJSON  string
}

// ApplyPolicyRevision atomically creates a new policy revision, deactivates
// the current active revision, activates the new one, and records an audit
// entry in a single transaction. expectedRevision is the id of the currently
// active revision (or -1 when none may be active); any mismatch returns a
// RevisionConflictError and nothing is written.
func (s *Store) ApplyPolicyRevision(ctx context.Context, p ApplyPolicyRevisionParams) (*PolicyRevision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("apply policy revision: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentActive sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM scheduler_policy_revision WHERE is_active=1`).Scan(&currentActive); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("apply policy revision: query active: %w", err)
	}
	current := int64(0)
	if currentActive.Valid {
		current = currentActive.Int64
	}
	if p.ExpectedRevision == -1 {
		if currentActive.Valid {
			return nil, RevisionConflictError{Expected: p.ExpectedRevision, Current: current}
		}
	} else if !currentActive.Valid || currentActive.Int64 != p.ExpectedRevision {
		return nil, RevisionConflictError{Expected: p.ExpectedRevision, Current: current}
	}

	var parentID interface{}
	if p.ParentRevisionID != nil {
		parentID = *p.ParentRevisionID
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO scheduler_policy_revision(schema_version,parent_revision_id,policy_json,author,reason,validation_hash) VALUES(?,?,?,?,?,?)`,
		p.SchemaVersion, parentID, p.PolicyJSON, p.Author, p.Reason, p.ValidationHash)
	if err != nil {
		return nil, fmt.Errorf("apply policy revision: create: %w", err)
	}
	newID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("apply policy revision: last insert id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scheduler_policy_revision SET is_active=0 WHERE is_active=1`); err != nil {
		return nil, fmt.Errorf("apply policy revision: deactivate current: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE scheduler_policy_revision SET is_active=1, activated_at=CURRENT_TIMESTAMP WHERE id=?`, newID); err != nil {
		return nil, fmt.Errorf("apply policy revision: activate: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO scheduler_audit(event_type,actor,detail_json) VALUES(?,?,?)`,
		p.AuditEventType, p.Author, p.AuditDetailJSON); err != nil {
		return nil, fmt.Errorf("apply policy revision: audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("apply policy revision: commit: %w", err)
	}
	return s.GetPolicyRevision(ctx, newID)
}

// GetActivePolicyRevision returns the currently active policy revision.
func (s *Store) GetActivePolicyRevision(ctx context.Context) (*PolicyRevision, error) {
	return scanPolicyRevision(s.db.QueryRowContext(ctx,
		`SELECT id,schema_version,parent_revision_id,policy_json,author,reason,validation_hash,is_active,created_at,activated_at FROM scheduler_policy_revision WHERE is_active=1`))
}

// GetPolicyRevision returns a policy revision by id.
func (s *Store) GetPolicyRevision(ctx context.Context, id int64) (*PolicyRevision, error) {
	return scanPolicyRevision(s.db.QueryRowContext(ctx,
		`SELECT id,schema_version,parent_revision_id,policy_json,author,reason,validation_hash,is_active,created_at,activated_at FROM scheduler_policy_revision WHERE id=?`, id))
}

func scanPolicyRevision(row *sql.Row) (*PolicyRevision, error) {
	var r PolicyRevision
	var parent sql.NullInt64
	var activated sql.NullTime
	if err := row.Scan(&r.ID, &r.SchemaVersion, &parent, &r.PolicyJSON, &r.Author, &r.Reason, &r.ValidationHash, &r.IsActive, &r.CreatedAt, &activated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("scan policy revision: %w", err)
	}
	if parent.Valid {
		r.ParentRevisionID = &parent.Int64
	}
	if activated.Valid {
		r.ActivatedAt = &activated.Time
	}
	return &r, nil
}

// ListPolicyRevisions returns all policy revisions ordered by creation.
func (s *Store) ListPolicyRevisions(ctx context.Context) ([]PolicyRevision, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,schema_version,parent_revision_id,policy_json,author,reason,validation_hash,is_active,created_at,activated_at FROM scheduler_policy_revision ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list policy revisions: %w", err)
	}
	defer rows.Close()
	var out []PolicyRevision
	for rows.Next() {
		var r PolicyRevision
		var parent sql.NullInt64
		var activated sql.NullTime
		if err := rows.Scan(&r.ID, &r.SchemaVersion, &parent, &r.PolicyJSON, &r.Author, &r.Reason, &r.ValidationHash, &r.IsActive, &r.CreatedAt, &activated); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if parent.Valid {
			r.ParentRevisionID = &parent.Int64
		}
		if activated.Valid {
			r.ActivatedAt = &activated.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetControlState upserts a per-type control record. The revision is
// auto-incremented on each call.
func (s *Store) SetControlState(ctx context.Context, taskType, state string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduler_control(task_type,state,revision,updated_at) VALUES(?,?,COALESCE((SELECT revision+1 FROM scheduler_control WHERE task_type=?),1),CURRENT_TIMESTAMP)
		 ON CONFLICT(task_type) DO UPDATE SET state=excluded.state, revision=COALESCE((SELECT revision+1 FROM scheduler_control WHERE task_type=? LIMIT 1),revision+1), updated_at=CURRENT_TIMESTAMP`,
		taskType, state, taskType, taskType); err != nil {
		return fmt.Errorf("set control state: %w", err)
	}
	return nil
}

// GetControlState returns the control state for a task type.
func (s *Store) GetControlState(ctx context.Context, taskType string) (*ControlState, error) {
	var cs ControlState
	if err := s.db.QueryRowContext(ctx,
		`SELECT task_type,state,revision,updated_at FROM scheduler_control WHERE task_type=?`, taskType).Scan(
		&cs.TaskType, &cs.State, &cs.Revision, &cs.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("get control state: %w", err)
	}
	return &cs, nil
}

// AdvanceFairnessCursor records the last-served library bucket for a task type.
// A nil library ID is the initialized synthetic null-library bucket.
func (s *Store) AdvanceFairnessCursor(ctx context.Context, taskType string, libraryID *int64) error {
	var library any
	if libraryID != nil {
		library = *libraryID
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduler_fairness(task_type,last_library_id,initialized,revision,updated_at) VALUES(?,?,1,1,CURRENT_TIMESTAMP)
		 ON CONFLICT(task_type) DO UPDATE SET last_library_id=excluded.last_library_id,initialized=1,revision=scheduler_fairness.revision+1,updated_at=CURRENT_TIMESTAMP`, taskType, library); err != nil {
		return fmt.Errorf("advance fairness cursor: %w", err)
	}
	return nil
}

// GetFairnessCursor returns the durable cursor for a task type.
func (s *Store) GetFairnessCursor(ctx context.Context, taskType string) (*FairnessCursor, error) {
	var fc FairnessCursor
	var library sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT task_type,last_library_id,initialized,revision,updated_at FROM scheduler_fairness WHERE task_type=?`, taskType).Scan(
		&fc.TaskType, &library, &fc.Initialized, &fc.Revision, &fc.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("get fairness cursor: %w", err)
	}
	if library.Valid {
		fc.LastLibraryID = &library.Int64
	}
	return &fc, nil
}

// CreateReservation creates a new active reservation. The reservation
// snapshots the active policy revision id and lease deadline.
func (s *Store) CreateReservation(ctx context.Context, executionID, taskType string, reservedUnits int, policyRevisionID int64, leaseUntil time.Time) (*Reservation, error) {
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduler_reservation(execution_id,task_type,reserved_units,policy_revision_id,status,lease_until) VALUES(?,?,?,?,'active',?)`,
		executionID, taskType, reservedUnits, policyRevisionID, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("create reservation: %w", err)
	}
	_, err = result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return s.GetReservation(ctx, executionID)
}

// ReleaseReservation marks a reservation as released with evidence.
// Released rows are retained and not deleted.
func (s *Store) ReleaseReservation(ctx context.Context, executionID, reason, releasedBy string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE scheduler_reservation SET status='released', released_at=CURRENT_TIMESTAMP, release_reason=?, released_by=?, updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='active'`,
		reason, releasedBy, executionID)
	if err != nil {
		return fmt.Errorf("release reservation: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("reservation %s not found or already released", executionID)
	}
	return nil
}

// GetReservation returns a reservation by execution id.
func (s *Store) GetReservation(ctx context.Context, executionID string) (*Reservation, error) {
	return scanReservation(s.db.QueryRowContext(ctx,
		`SELECT id,execution_id,task_type,reserved_units,policy_revision_id,status,lease_until,released_at,release_reason,released_by,created_at,updated_at FROM scheduler_reservation WHERE execution_id=?`, executionID))
}

// ListActiveReservations returns all active reservations.
func (s *Store) ListActiveReservations(ctx context.Context) ([]Reservation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,execution_id,task_type,reserved_units,policy_revision_id,status,lease_until,released_at,release_reason,released_by,created_at,updated_at FROM scheduler_reservation WHERE status='active' ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list active reservations: %w", err)
	}
	defer rows.Close()
	return scanReservations(rows)
}

func scanReservation(row *sql.Row) (*Reservation, error) {
	var r Reservation
	var lease, released sql.NullTime
	if err := row.Scan(&r.ID, &r.ExecutionID, &r.TaskType, &r.ReservedUnits, &r.PolicyRevisionID,
		&r.Status, &lease, &released, &r.ReleaseReason, &r.ReleasedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("scan reservation: %w", err)
	}
	if lease.Valid {
		r.LeaseUntil = &lease.Time
	}
	if released.Valid {
		r.ReleasedAt = &released.Time
	}
	return &r, nil
}

func scanReservations(rows *sql.Rows) ([]Reservation, error) {
	var out []Reservation
	for rows.Next() {
		var r Reservation
		var lease, released sql.NullTime
		if err := rows.Scan(&r.ID, &r.ExecutionID, &r.TaskType, &r.ReservedUnits, &r.PolicyRevisionID,
			&r.Status, &lease, &released, &r.ReleaseReason, &r.ReleasedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan reservation: %w", err)
		}
		if lease.Valid {
			r.LeaseUntil = &lease.Time
		}
		if released.Valid {
			r.ReleasedAt = &released.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordAudit appends an audit entry. The table is append-only.
func (s *Store) RecordAudit(ctx context.Context, eventType, actor, detailJSON string) (*AuditEntry, error) {
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduler_audit(event_type,actor,detail_json) VALUES(?,?,?)`,
		eventType, actor, detailJSON)
	if err != nil {
		return nil, fmt.Errorf("record audit: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return &AuditEntry{ID: id, EventType: eventType, Actor: actor, DetailJSON: detailJSON, CreatedAt: time.Now()}, nil
}

// ListAudit returns audit entries ordered by creation.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,event_type,actor,detail_json,created_at FROM scheduler_audit ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.EventType, &e.Actor, &e.DetailJSON, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
