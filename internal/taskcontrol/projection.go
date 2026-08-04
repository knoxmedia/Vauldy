package taskcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// NormalizedStatus is one of six canonical task lifecycle states.
type NormalizedStatus string

const (
	StatusWaiting   NormalizedStatus = "waiting"
	StatusRunning   NormalizedStatus = "running"
	StatusDone      NormalizedStatus = "done"
	StatusFailed    NormalizedStatus = "failed"
	StatusCancelled NormalizedStatus = "cancelled"
	StatusSkipped   NormalizedStatus = "skipped"
)

// IsTerminal returns true for statuses that represent a final, non-retrying state.
func (s NormalizedStatus) IsTerminal() bool {
	switch s {
	case StatusDone, StatusFailed, StatusCancelled, StatusSkipped:
		return true
	default:
		return false
	}
}

// IsRunning returns true for in-flight tasks.
func (s NormalizedStatus) IsRunning() bool { return s == StatusRunning }

// IsWaiting returns true for tasks awaiting admission.
func (s NormalizedStatus) IsWaiting() bool { return s == StatusWaiting }

// AllNormalizedStatuses is the ordered canonical list of normalized statuses.
var AllNormalizedStatuses = []NormalizedStatus{
	StatusWaiting,
	StatusRunning,
	StatusDone,
	StatusFailed,
	StatusCancelled,
	StatusSkipped,
}

// normalizeStatus maps a raw source-table status to its canonical
// NormalizedStatus. Unknown raw states fall back to StatusWaiting
// unless terminal evidence exists; then StatusFailed.
func normalizeStatus(raw string, terminalEvidence bool) NormalizedStatus {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "waiting", "pending", "queued", "ready":
		return StatusWaiting
	case "running", "processing", "active", "in_progress":
		return StatusRunning
	case "done", "completed", "success", "finished":
		return StatusDone
	case "failed", "error", "permanent_failure":
		return StatusFailed
	case "cancelled", "canceled", "aborted":
		return StatusCancelled
	case "skipped", "bypass":
		return StatusSkipped
	default:
		if terminalEvidence {
			return StatusFailed
		}
		return StatusWaiting
	}
}

// OwnerLeaseInfo captures the current owner and lease window.
type OwnerLeaseInfo struct {
	Owner     string     `json:"owner"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
}

// AdmissionInfo reports scheduler admission status for a task.
type AdmissionInfo struct {
	Runnable bool              `json:"runnable"`
	Blocker  string            `json:"blocker,omitempty"`
	Details  map[string]any    `json:"details,omitempty"`
}

// DependencyInfo describes one upstream dependency.
type DependencyInfo struct {
	TaskIdentity string           `json:"task_identity"`
	TaskType     string           `json:"task_type"`
	Status       NormalizedStatus `json:"status"`
	Required     bool             `json:"required"`
}

// EvidenceEntry records an error or terminal reason for the task.
type EvidenceEntry struct {
	At      time.Time `json:"at"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Attempt int       `json:"attempt"`
}

// ProjectionRow is the single canonical projected row for one task identity.
// It is produced by reading authoritative execution/plan/scheduler state
// through registered source adapters.
type ProjectionRow struct {
	TaskID           string           `json:"task_id"`
	SourceKind       string           `json:"source_kind"`
	SourceID         int64            `json:"source_id"`
	TaskType         string           `json:"task_type"`
	Family           string           `json:"family"`
	NormalizedStatus NormalizedStatus `json:"normalized_status"`
	RawStatus        string           `json:"raw_status"`
	Revision         int64            `json:"revision"`
	Generation       int64            `json:"generation"`
	RetryRound       int              `json:"retry_round"`
	Attempt          int              `json:"attempt"`
	MaxAttempts      int              `json:"max_attempts"`
	BasePriority     int64            `json:"base_priority"`
	EffectivePriority int64           `json:"effective_priority"`
	AvailableAt      *time.Time       `json:"available_at,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	MediaID          *int64           `json:"media_id,omitempty"`
	MediaTitle       string           `json:"media_title,omitempty"`
	MediaFilePath    string           `json:"media_file_path,omitempty"`
	LibraryID        *int64           `json:"library_id,omitempty"`
	Admission        *AdmissionInfo   `json:"admission,omitempty"`
	OwnerLease       *OwnerLeaseInfo  `json:"owner_lease,omitempty"`
	TerminalReason   string           `json:"terminal_reason,omitempty"`
	Tombstone        bool             `json:"tombstone"`
	RemovedAt        *time.Time       `json:"removed_at,omitempty"`
	RemovedBy        string           `json:"removed_by,omitempty"`
	RemoveReason     string           `json:"remove_reason,omitempty"`
	Dependencies     []DependencyInfo `json:"dependencies,omitempty"`
	Resources        []string         `json:"resources,omitempty"`
	ProjectionError  string           `json:"projection_error,omitempty"`
}

// RawTaskRow is a raw row read from a source table.
type RawTaskRow struct {
	SourceKind       string
	SourceID         int64
	TaskType         string
	RawStatus        string
	Generation       int64
	RetryRound       int
	Attempt          int
	MaxAttempts      int
	BasePriority     int64
	AvailableAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	MediaID          *int64
	MediaTitle       string
	MediaFilePath    string
	LibraryID        *int64
	Owner            string
	LeaseUntil       *time.Time
	TerminalReason   string
	Tombstone        bool
	RemovedAt        *time.Time
	RemovedBy        string
	RemoveReason     string
	ExecutionID      string
	RunNowExpires    *time.Time
}

// SourceAdapter reads raw task rows from a Phase 1-3 source table.
type SourceAdapter interface {
	// Kind returns the source kind identifier (e.g., "post_ingest_task").
	Kind() string
	// Read returns the raw row for a single source-id.
	Read(ctx context.Context, tx *sql.Tx, id int64) (*RawTaskRow, error)
	// ListIDs returns all source-ids matching the given filters, ordered
	// by the claim-order tuple for pagination. The adapter must apply
	// the filters in SQL — never fetch all rows and filter in Go.
	ListIDs(ctx context.Context, tx *sql.Tx, filters Filters) ([]int64, error)
	// Count returns the exact count matching the filters.
	Count(ctx context.Context, tx *sql.Tx, filters Filters) (int64, error)
}

// ProjectionBuilder computes normalized ProjectionRows from source adapters
// and the canonical projection revision store.
type ProjectionBuilder struct {
	db       *sql.DB
	registry *Registry
	adapters map[string]SourceAdapter
}

// NewProjectionBuilder creates a ProjectionBuilder backed by db.
func NewProjectionBuilder(db *sql.DB, registry *Registry) *ProjectionBuilder {
	return &ProjectionBuilder{
		db:       db,
		registry: registry,
		adapters: make(map[string]SourceAdapter),
	}
}

// RegisterAdapter adds a source adapter. Duplicate kinds overwrite.
func (b *ProjectionBuilder) RegisterAdapter(a SourceAdapter) {
	b.adapters[a.Kind()] = a
}

// adapterForKind returns the registered source adapter for kind, or nil.
func (b *ProjectionBuilder) adapterForKind(kind string) SourceAdapter {
	return b.adapters[kind]
}

// snapshotRevision returns the current global projection revision (max revision value).
func (b *ProjectionBuilder) snapshotRevision(ctx context.Context, tx *sql.Tx) (int64, error) {
	var rev sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT MAX(revision) FROM task_projection_revision`).Scan(&rev)
	if err != nil {
		return 0, err
	}
	if rev.Valid {
		return rev.Int64, nil
	}
	return 0, nil
}

// nextRevision allocates and returns the next projection revision atomically.
func (b *ProjectionBuilder) nextRevision(ctx context.Context, tx *sql.Tx) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO task_projection_sequence(singleton_id, next_revision) VALUES(1, 1)`); err != nil {
		return 0, fmt.Errorf("ensure projection sequence: %w", err)
	}
	var nextRev int64
	if err := tx.QueryRowContext(ctx,
		`SELECT next_revision FROM task_projection_sequence WHERE singleton_id=1`).Scan(&nextRev); err != nil {
		return 0, fmt.Errorf("read projection sequence: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE task_projection_sequence SET next_revision=next_revision+1 WHERE singleton_id=1`); err != nil {
		return 0, fmt.Errorf("advance projection sequence: %w", err)
	}
	return nextRev, nil
}

// projectionRevision returns the stored revision for a task identity.
func (b *ProjectionBuilder) projectionRevision(ctx context.Context, tx *sql.Tx, taskIdentity string) (int64, error) {
	var rev sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT revision FROM task_projection_revision WHERE task_identity=?`, taskIdentity).Scan(&rev); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	if rev.Valid {
		return rev.Int64, nil
	}
	return 0, nil
}

// writeRevision upserts a task projection revision and returns the revision.
func (b *ProjectionBuilder) writeRevision(ctx context.Context, tx *sql.Tx, taskIdentity string) (int64, error) {
	next, err := b.nextRevision(ctx, tx)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO task_projection_revision(task_identity, revision, updated_at) VALUES(?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(task_identity) DO UPDATE SET revision=excluded.revision, updated_at=CURRENT_TIMESTAMP`,
		taskIdentity, next); err != nil {
		return 0, fmt.Errorf("write projection revision: %w", err)
	}
	return next, nil
}

// parseIdentity splits a task identity into source kind and source id.
func parseIdentity(taskID string) (kind string, id int64, err error) {
	idx := strings.LastIndex(taskID, ":")
	if idx < 0 {
		return "", 0, fmt.Errorf("invalid task identity %q: missing colon separator", taskID)
	}
	kind = taskID[:idx]
	idStr := taskID[idx+1:]
	if _, scanErr := fmt.Sscanf(idStr, "%d", &id); scanErr != nil {
		return "", 0, fmt.Errorf("invalid task identity %q: %w", taskID, scanErr)
	}
	return kind, id, nil
}

// BuildIdentity constructs a task identity from source kind and id.
func BuildIdentity(kind string, id int64) string {
	return fmt.Sprintf("%s:%d", kind, id)
}

// Project reads and normalizes a single task identity into a ProjectionRow.
// It returns nil, nil when the identity does not exist in any source adapter.
func (b *ProjectionBuilder) Project(ctx context.Context, taskIdentity string) (*ProjectionRow, error) {
	kind, id, err := parseIdentity(taskIdentity)
	if err != nil {
		return nil, fmt.Errorf("project identity: %w", err)
	}
	adapter := b.adapterForKind(kind)
	if adapter == nil {
		return nil, nil
	}
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("project begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	raw, err := adapter.Read(ctx, tx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("project read: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	row := b.normalize(raw, kind)
	rev, _ := b.projectionRevision(ctx, tx, taskIdentity)
	row.Revision = rev
	row.TaskID = taskIdentity

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("project commit: %w", err)
	}
	return row, nil
}

// ProjectTx is like Project but operates within an existing transaction.
func (b *ProjectionBuilder) ProjectTx(ctx context.Context, tx *sql.Tx, taskIdentity string) (*ProjectionRow, error) {
	kind, id, err := parseIdentity(taskIdentity)
	if err != nil {
		return nil, fmt.Errorf("project identity: %w", err)
	}
	adapter := b.adapterForKind(kind)
	if adapter == nil {
		return nil, nil
	}
	raw, err := adapter.Read(ctx, tx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("project read: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	row := b.normalize(raw, kind)
	rev, _ := b.projectionRevision(ctx, tx, taskIdentity)
	row.Revision = rev
	row.TaskID = taskIdentity
	return row, nil
}

// normalize converts a raw row into a ProjectionRow using centralized mappings.
func (b *ProjectionBuilder) normalize(raw *RawTaskRow, kind string) *ProjectionRow {
	taskType := raw.TaskType
	family := ""
	if spec := b.lookupSpec(taskType); spec != nil {
		family = spec.Family
	}

	terminalEvidence := raw.Tombstone || raw.RemovedAt != nil ||
		raw.RawStatus == "failed" || raw.RawStatus == "done" ||
		raw.RawStatus == "cancelled" || raw.RawStatus == "skipped"

	status := normalizeStatus(raw.RawStatus, terminalEvidence)

	row := &ProjectionRow{
		SourceKind:       kind,
		SourceID:         raw.SourceID,
		TaskType:         taskType,
		Family:           family,
		NormalizedStatus: status,
		RawStatus:        raw.RawStatus,
		Generation:       raw.Generation,
		RetryRound:       raw.RetryRound,
		Attempt:          raw.Attempt,
		MaxAttempts:      raw.MaxAttempts,
		BasePriority:     raw.BasePriority,
		EffectivePriority: raw.BasePriority, // will be computed externally
		AvailableAt:      raw.AvailableAt,
		CreatedAt:        raw.CreatedAt,
		UpdatedAt:        raw.UpdatedAt,
		MediaID:          raw.MediaID,
		MediaTitle:       raw.MediaTitle,
		MediaFilePath:    raw.MediaFilePath,
		LibraryID:        raw.LibraryID,
		TerminalReason:   raw.TerminalReason,
		Tombstone:        raw.Tombstone,
		RemovedAt:        raw.RemovedAt,
		RemovedBy:        raw.RemovedBy,
		RemoveReason:     raw.RemoveReason,
	}

	if raw.Owner != "" {
		row.OwnerLease = &OwnerLeaseInfo{
			Owner:     raw.Owner,
			LeaseUntil: raw.LeaseUntil,
		}
	}

	if raw.RemovedAt == nil && raw.Tombstone == false {
		row.ProjectionError = ""
	}

	return row
}

// lookupSpec returns the TaskSpec for a task type, or nil.
func (b *ProjectionBuilder) lookupSpec(taskType string) *TaskSpec {
	if b.registry == nil {
		return nil
	}
	for _, g := range b.registry.Groups {
		for i := range g.Types {
			if g.Types[i].Type == taskType {
				return &g.Types[i]
			}
		}
	}
	return nil
}

// StorageExporter provides access to the builder's DB for query use.
func (b *ProjectionBuilder) DB() *sql.DB { return b.db }

// Registry returns the task type registry.
func (b *ProjectionBuilder) Registry() *Registry { return b.registry }

// StoreRevision durably commits a new projection revision for taskIdentity.
// It returns the newly allocated revision number.
func (b *ProjectionBuilder) StoreRevision(ctx context.Context, tx *sql.Tx, taskIdentity string) (int64, error) {
	return b.writeRevision(ctx, tx, taskIdentity)
}

// OracleAdapter is a SourceAdapter for post_ingest_task (the canonical orchestration rows).
type OracleAdapter struct {
	db *sql.DB
}

// NewOracleAdapter creates a post_ingest_task source adapter.
func NewOracleAdapter(db *sql.DB) *OracleAdapter {
	return &OracleAdapter{db: db}
}

// Kind returns "post_ingest_task".
func (a *OracleAdapter) Kind() string { return "orchestration" }

func (a *OracleAdapter) Read(ctx context.Context, tx *sql.Tx, id int64) (*RawTaskRow, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, task_type, status, COALESCE(attempts,0), COALESCE(max_attempts,3),
		       COALESCE(generation,1), COALESCE(retry_round,0),
		       COALESCE(lease_owner,''), lease_until, COALESCE(last_error,''),
		       COALESCE(base_priority,0), available_at, created_at, updated_at,
		       media_id, library_id,
		       removed_at, COALESCE(removed_by,''), COALESCE(remove_reason,''),
		       COALESCE(run_now_expires, NULL) AS run_now_expires,
		       COALESCE((SELECT title FROM media m WHERE m.id = post_ingest_task.media_id),''),
		       COALESCE((SELECT file_path FROM media m WHERE m.id = post_ingest_task.media_id),'')
		FROM post_ingest_task WHERE id=?`, id)
	return scanOracleRow(row, id)
}

func scanOracleRow(row *sql.Row, id int64) (*RawTaskRow, error) {
	r := &RawTaskRow{SourceKind: "orchestration", SourceID: id}
	var typ string
	var leaseUntil sql.NullTime
	var availableAt sql.NullTime
	var runNowExpires sql.NullTime
	var mediaID sql.NullInt64
	var libraryID sql.NullInt64
	var removedAt sql.NullTime
	if err := row.Scan(&r.SourceID, &typ, &r.RawStatus, &r.Attempt, &r.MaxAttempts,
		&r.Generation, &r.RetryRound, &r.Owner, &leaseUntil, &r.TerminalReason,
		&r.BasePriority, &availableAt, &r.CreatedAt, &r.UpdatedAt,
		&mediaID, &libraryID,
		&removedAt, &r.RemovedBy, &r.RemoveReason,
		&runNowExpires, &r.MediaTitle, &r.MediaFilePath); err != nil {
		return nil, err
	}
	if removedAt.Valid {
		r.RemovedAt = &removedAt.Time
	}
	r.TaskType = typ
	if leaseUntil.Valid {
		r.LeaseUntil = &leaseUntil.Time
	}
	if availableAt.Valid {
		r.AvailableAt = &availableAt.Time
	}
	if runNowExpires.Valid {
		r.RunNowExpires = &runNowExpires.Time
	}
	if mediaID.Valid {
		r.MediaID = &mediaID.Int64
	}
	if libraryID.Valid {
		r.LibraryID = &libraryID.Int64
	}
	return r, nil
}

// ListIDs returns all post_ingest_task ids matching the filters.
func (a *OracleAdapter) ListIDs(ctx context.Context, tx *sql.Tx, filters Filters) ([]int64, error) {
	where, args := compileFiltersSQL(filters, "post_ingest_task", "status", "task_type")
	query := `SELECT id FROM post_ingest_task` + where + ` ORDER BY COALESCE(base_priority,0) DESC, id ASC`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Count returns the filtered count of post_ingest_task rows.
func (a *OracleAdapter) Count(ctx context.Context, tx *sql.Tx, filters Filters) (int64, error) {
	where, args := compileFiltersSQL(filters, "post_ingest_task", "status", "task_type")
	var count int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM post_ingest_task`+where, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// Filters is a placeholder for query filter parameters.
// The full Filter type is defined in query.go; projection adapters use this
// simple struct for internal list/count operations.
type Filters struct {
	TaskType   string
	Status     string
	Generation *int64
	LibraryID  *int64
	Removed    string // "exclude", "include", "only"
	Owner      string
}

// compileFiltersSQL builds a WHERE clause from a Filters struct.
func compileFiltersSQL(f Filters, table, statusCol, typeCol string) (string, []any) {
	var clauses []string
	var args []any

	if f.TaskType != "" {
		clauses = append(clauses, typeCol+" = ?")
		args = append(args, f.TaskType)
	}
	if f.Status != "" {
		clauses = append(clauses, statusCol+" = ?")
		args = append(args, f.Status)
	}
	if f.Generation != nil {
		clauses = append(clauses, "generation = ?")
		args = append(args, *f.Generation)
	}
	if f.LibraryID != nil {
		clauses = append(clauses, "library_id = ?")
		args = append(args, *f.LibraryID)
	}
	if f.Owner != "" {
		clauses = append(clauses, "lease_owner = ?")
		args = append(args, f.Owner)
	}
	switch f.Removed {
	case "exclude", "":
		clauses = append(clauses, "removed_at IS NULL")
	case "only":
		clauses = append(clauses, "removed_at IS NOT NULL")
	case "include":
		// no filter on removed_at
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + joinWithAnd(clauses), args
}

func joinWithAnd(clauses []string) string {
	s := ""
	for i, c := range clauses {
		if i > 0 {
			s += " AND "
		}
		s += c
	}
	return s
}

// ProjectionRowJSON is a serialization helper used by tests and API handlers.
func (r *ProjectionRow) JSON() (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// EffectivePriority computes effective priority using scheduler policy parameters.
// This is a convenience wrapper for external callers.
func EffectivePriority(basePriority, rowPriority int64, ageSecs, agingStep int64) int64 {
	if ageSecs < 0 {
		ageSecs = 0
	}
	if agingStep <= 0 {
		agingStep = 0
	}
	agingBoost := (ageSecs / 60) * agingStep // simplified
	ep := basePriority + rowPriority + agingBoost
	return ep
}
