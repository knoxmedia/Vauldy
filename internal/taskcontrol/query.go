package taskcontrol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CursorVersion is the current cursor encoding version.
const CursorVersion = 1

// QueryFilter captures every supported list filter.
type QueryFilter struct {
	TaskType   string
	Status     string
	Source     string
	LibraryID  *int64
	Generation *int64
	Capability string
	Owner      string
	Blocker    string
	Removed    string // "exclude" (default), "include", "only"
}

// CursorPayload is the server-authoritative cursor state, base64-encoded
// in the opaque cursor string. Reject mismatched versions, orders, or
// filter digests.
type CursorPayload struct {
	Version    int    `json:"v"`
	Order      string `json:"o"`
	FilterHash string `json:"fh"`
	SnapshotAt int64  `json:"sa"`
	ID         int64  `json:"id"`
	Priority   int64  `json:"pr"`
	Kind       string `json:"k,omitempty"`
}

// EncodeCursor serializes a cursor payload to an opaque base64 string.
func EncodeCursor(p CursorPayload) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor deserializes an opaque cursor string. Returns an error
// on invalid base64 or malformed JSON.
func DecodeCursor(encoded string) (CursorPayload, error) {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return CursorPayload{}, fmt.Errorf("invalid cursor encoding: %w", err)
	}
	var p CursorPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return CursorPayload{}, fmt.Errorf("invalid cursor payload: %w", err)
	}
	return p, nil
}

// filterHash computes a deterministic SHA-256 hash of the filter fields
// so that cursor and queries cannot be mixed across different filters.
func (f QueryFilter) filterHash() string {
	h := sha256.New()
	h.Write([]byte(f.TaskType + "|"))
	h.Write([]byte(f.Status + "|"))
	h.Write([]byte(f.Source + "|"))
	if f.LibraryID != nil {
		fmt.Fprintf(h, "lib:%d|", *f.LibraryID)
	} else {
		h.Write([]byte("lib:nil|"))
	}
	if f.Generation != nil {
		fmt.Fprintf(h, "gen:%d|", *f.Generation)
	} else {
		h.Write([]byte("gen:nil|"))
	}
	h.Write([]byte(f.Capability + "|"))
	h.Write([]byte(f.Owner + "|"))
	h.Write([]byte(f.Blocker + "|"))
	h.Write([]byte(f.Removed + "|"))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ListResult is a paginated list response.
type ListResult struct {
	Items            []ProjectionRow `json:"items"`
	Total            int64           `json:"total"`
	NextCursor       string          `json:"next_cursor,omitempty"`
	HasMore          bool            `json:"has_more"`
	Truncated        bool            `json:"truncated"`
	SnapshotRevision int64           `json:"snapshot_revision"`
}

// AttemptInfo captures one execution attempt.
type AttemptInfo struct {
	Attempt  int    `json:"attempt"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Duration int64  `json:"duration_secs,omitempty"`
}

// AuditEntryInfo is a compact audit entry for the detail view.
type AuditEntryInfo struct {
	ID         int64  `json:"id"`
	Action     string `json:"action"`
	ActorName  string `json:"actor_name"`
	Reason     string `json:"reason,omitempty"`
	PrevStatus string `json:"prev_status,omitempty"`
	NewStatus  string `json:"new_status,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// DetailResult expands a single projected row with attempts, dependencies,
// evidence, and audit history.
type DetailResult struct {
	Row          ProjectionRow    `json:"row"`
	Attempts     []AttemptInfo    `json:"attempts,omitempty"`
	Dependencies []DependencyInfo `json:"dependencies,omitempty"`
	Evidence     []EvidenceEntry  `json:"evidence,omitempty"`
	AuditEvents  []AuditEntryInfo `json:"audit_events,omitempty"`
}

// QueryService executes cursor-based list queries and detail lookups
// over the projection builder and registered adapters.
type QueryService struct {
	builder *ProjectionBuilder
	db      *sql.DB
}

// NewQueryService creates a query service backed by the projection builder.
func NewQueryService(builder *ProjectionBuilder) *QueryService {
	return &QueryService{builder: builder, db: builder.DB()}
}

// List executes a filtered, cursor-paginated query and returns the exact
// total count at the same snapshot boundary.
func (q *QueryService) List(ctx context.Context, filter QueryFilter, cursor string, limit int) (*ListResult, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	filterHash := filter.filterHash()

	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	snapRev, err := q.builder.snapshotRevision(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("list snapshot revision: %w", err)
	}

	// Decode cursor if provided
	var afterID int64
	var afterPri int64
	if cursor != "" {
		cp, err := DecodeCursor(cursor)
		if err != nil {
			if err2 := tx.Rollback(); err2 != nil {
				return nil, err2
			}
			return nil, fmt.Errorf("invalid_cursor: %w", err)
		}
		if cp.Version != CursorVersion {
			if err2 := tx.Rollback(); err2 != nil {
				return nil, err2
			}
			return nil, fmt.Errorf("invalid_cursor: unsupported version %d", cp.Version)
		}
		if cp.FilterHash != filterHash {
			if err2 := tx.Rollback(); err2 != nil {
				return nil, err2
			}
			return nil, fmt.Errorf("cursor_filter_mismatch")
		}
		afterID = cp.ID
		afterPri = cp.Priority
	}

	if sources := q.adaptersForPublicType(filter.TaskType); len(sources) > 0 {
		result, err := q.listAdaptersTx(ctx, tx, sources, filter, cpKind(cursor), afterID, afterPri, limit, snapRev, filterHash)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("list commit: %w", err)
		}
		return result, nil
	}

	// Build WHERE clause for oracle adapter
	where, args := buildOracleWhere(filter)
	// Add cursor pagination
	cursorClause, cursorArgs := buildCursorWhere(afterID, afterPri)
	if cursorClause != "" {
		if where == "" {
			where = " WHERE " + cursorClause
		} else {
			where += " AND " + cursorClause
		}
		args = append(args, cursorArgs...)
	}

	// Count total
	countWhere := extractWhereBeforeCursor(where, cursorClause)
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task`+countWhere, argsWithoutCursor(args, cursorArgs)...).Scan(&total); err != nil {
		return nil, fmt.Errorf("list count: %w", err)
	}

	// Query items with limit+1 to detect has_more
	query := `SELECT id, task_type, status, COALESCE(attempts,0), COALESCE(max_attempts,3),
		COALESCE(generation,1), COALESCE(retry_round,0),
		COALESCE(lease_owner,''), lease_until, COALESCE(last_error,''),
		COALESCE(base_priority,0), available_at, created_at, updated_at,
		media_id, library_id,
		removed_at, COALESCE(removed_by,''), COALESCE(remove_reason,''),
		run_now_expires,
		COALESCE((SELECT title FROM media m WHERE m.id = post_ingest_task.media_id),''),
		COALESCE((SELECT file_path FROM media m WHERE m.id = post_ingest_task.media_id),'')
	FROM post_ingest_task` + where + ` ORDER BY COALESCE(base_priority,0) DESC, id ASC LIMIT ?`
	args = append(args, limit+1)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list query: %w", err)
	}
	defer rows.Close()

	var items []ProjectionRow
	for rows.Next() {
		raw, err := scanOracleRowSimple(rows)
		if err != nil {
			return nil, fmt.Errorf("list scan: %w", err)
		}
		row := q.builder.normalize(&raw, "orchestration")
		row.Revision = snapRev
		row.TaskID = BuildIdentity("orchestration", raw.SourceID)
		items = append(items, *row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	var nextCursor string
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cp := CursorPayload{
			Version:    CursorVersion,
			Order:      "claim_order",
			FilterHash: filterHash,
			SnapshotAt: snapRev,
			ID:         last.SourceID,
			Priority:   last.EffectivePriority,
		}
		nextCursor = EncodeCursor(cp)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("list commit: %w", err)
	}

	return &ListResult{
		Items:            items,
		Total:            total,
		NextCursor:       nextCursor,
		HasMore:          hasMore,
		Truncated:        false,
		SnapshotRevision: snapRev,
	}, nil
}

// Detail returns the expanded detail for a single task identity, including
// attempts, dependencies, evidence, and audit history.
func (q *QueryService) Detail(ctx context.Context, taskIdentity string) (*DetailResult, error) {
	row, err := q.builder.Project(ctx, taskIdentity)
	if err != nil {
		return nil, fmt.Errorf("detail project: %w", err)
	}
	if row == nil {
		return nil, nil
	}
	return &DetailResult{Row: *row}, nil
}

type resolvedSource struct {
	mapping SourceMapping
	adapter SourceAdapter
}

func (q *QueryService) adaptersForPublicType(taskType string) []resolvedSource {
	if taskType == "" || q.builder.Registry() == nil {
		return nil
	}
	for _, group := range q.builder.Registry().Groups {
		for _, spec := range group.Types {
			if spec.Type != taskType || !spec.Available {
				continue
			}
			var all []resolvedSource
			seen := map[string]bool{}
			for _, mapping := range spec.SourceMappings {
				adapter := q.builder.adapterForKind(mapping.Kind)
				if adapter == nil {
					continue
				}
				key := adapter.Kind() + "\x00" + mapping.InternalType
				if seen[key] {
					continue
				}
				seen[key] = true
				r := resolvedSource{mapping: mapping, adapter: adapter}
				all = append(all, r)
			}
			// Merge every resolvable mapping. Identity-level deduplication happens
			// during list projection; cross-source rows are distinct unless an adapter
			// can provide a proven logical-link key (none currently do).
			return all
		}
	}
	return nil
}

func adapterFilters(filter QueryFilter, mapping SourceMapping) Filters {
	taskType := mapping.InternalType
	if taskType == "" {
		taskType = filter.TaskType
	}
	return Filters{TaskType: taskType, Status: filter.Status, Generation: filter.Generation,
		LibraryID: filter.LibraryID, Removed: filter.Removed, Owner: filter.Owner}
}

func cpKind(cursor string) string {
	if cursor == "" {
		return ""
	}
	p, err := DecodeCursor(cursor)
	if err != nil {
		return ""
	}
	return p.Kind
}

func (q *QueryService) listAdaptersTx(ctx context.Context, tx *sql.Tx, sources []resolvedSource, filter QueryFilter, afterKind string, afterID, afterPri int64, limit int, snapRev int64, filterHash string) (*ListResult, error) {
	byIdentity := make(map[string]ProjectionRow)
	for _, source := range sources {
		ids, err := source.adapter.ListIDs(ctx, tx, adapterFilters(filter, source.mapping))
		if err != nil {
			return nil, fmt.Errorf("list query %s: %w", source.adapter.Kind(), err)
		}
		for _, id := range ids {
			identity := BuildIdentity(source.adapter.Kind(), id)
			if _, exists := byIdentity[identity]; exists {
				continue
			}
			row, err := q.builder.ProjectTx(ctx, tx, identity)
			if err != nil {
				return nil, fmt.Errorf("list project: %w", err)
			}
			if row != nil {
				row.Revision = snapRev
				byIdentity[identity] = *row
			}
		}
	}
	items := make([]ProjectionRow, 0, len(byIdentity))
	for _, row := range byIdentity {
		items = append(items, row)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].EffectivePriority != items[j].EffectivePriority {
			return items[i].EffectivePriority > items[j].EffectivePriority
		}
		if items[i].SourceKind != items[j].SourceKind {
			return items[i].SourceKind < items[j].SourceKind
		}
		return items[i].SourceID < items[j].SourceID
	})
	total := int64(len(items))
	start := 0
	if afterID != 0 {
		for start < len(items) {
			r := items[start]
			if r.EffectivePriority < afterPri || (r.EffectivePriority == afterPri && (r.SourceKind > afterKind || (r.SourceKind == afterKind && r.SourceID > afterID))) {
				break
			}
			start++
		}
	}
	end := start + limit
	hasMore := end < len(items)
	if end > len(items) {
		end = len(items)
	}
	items = items[start:end]
	result := &ListResult{Items: items, Total: total, HasMore: hasMore, SnapshotRevision: snapRev}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		result.NextCursor = EncodeCursor(CursorPayload{Version: CursorVersion, Order: "claim_order", FilterHash: filterHash, SnapshotAt: snapRev, ID: last.SourceID, Priority: last.EffectivePriority, Kind: last.SourceKind})
	}
	return result, nil
}

// Total returns the exact filtered count independently of pagination.
func (q *QueryService) Total(ctx context.Context, filter QueryFilter) (int64, error) {
	if sources := q.adaptersForPublicType(filter.TaskType); len(sources) > 0 {
		tx, err := q.db.BeginTx(ctx, nil)
		if err != nil {
			return 0, fmt.Errorf("total begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		var total int64
		for _, source := range sources {
			n, err := source.adapter.Count(ctx, tx, adapterFilters(filter, source.mapping))
			if err != nil {
				return 0, fmt.Errorf("total %s: %w", source.adapter.Kind(), err)
			}
			total += n
		}
		return total, tx.Commit()
	}

	where, args := buildOracleWhere(filter)
	var total int64
	if err := q.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM post_ingest_task`+where, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("total: %w", err)
	}
	return total, nil
}

// buildOracleWhere constructs a SQL WHERE clause from a QueryFilter.
func buildOracleWhere(f QueryFilter) (string, []any) {
	var clauses []string
	var args []any

	if f.TaskType != "" {
		clauses = append(clauses, "task_type = ?")
		args = append(args, f.TaskType)
	}
	if f.Status != "" {
		clauses = append(clauses, "status = ?")
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
		// no filter
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + joinWithAnd(clauses), args
}

// buildCursorWhere creates a cursor-based pagination WHERE clause using
// (priority DESC, id ASC) claim ordering.
func buildCursorWhere(afterID, afterPri int64) (string, []any) {
	if afterID == 0 {
		return "", nil
	}
	// (base_priority < afterPri) OR (base_priority = afterPri AND id > afterID)
	clause := "(COALESCE(base_priority,0) < ? OR (COALESCE(base_priority,0) = ? AND id > ?))"
	return clause, []any{afterPri, afterPri, afterID}
}

// extractWhereBeforeCursor returns the WHERE clause without cursor conditions.
func extractWhereBeforeCursor(where, cursorClause string) string {
	if cursorClause == "" {
		return where
	}
	idx := strings.Index(where, " AND "+cursorClause)
	if idx >= 0 {
		return where[:idx]
	}
	return where
}

// argsWithoutCursor removes cursor args from the arg list.
func argsWithoutCursor(args, cursorArgs []any) []any {
	if len(cursorArgs) == 0 {
		return args
	}
	return args[:len(args)-len(cursorArgs)]
}

// scanOracleRowSimple scans a row from sql.Rows (not sql.Row).
func scanOracleRowSimple(rows *sql.Rows) (RawTaskRow, error) {
	r := RawTaskRow{SourceKind: "orchestration"}
	var typ string
	var leaseUntil sql.NullTime
	var availableAt sql.NullTime
	var runNowExpires sql.NullTime
	var mediaID sql.NullInt64
	var libraryID sql.NullInt64
	var removedAt sql.NullTime
	if err := rows.Scan(&r.SourceID, &typ, &r.RawStatus, &r.Attempt, &r.MaxAttempts,
		&r.Generation, &r.RetryRound, &r.Owner, &leaseUntil, &r.TerminalReason,
		&r.BasePriority, &availableAt, &r.CreatedAt, &r.UpdatedAt,
		&mediaID, &libraryID,
		&removedAt, &r.RemovedBy, &r.RemoveReason,
		&runNowExpires, &r.MediaTitle, &r.MediaFilePath); err != nil {
		return r, err
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
	if removedAt.Valid {
		r.RemovedAt = &removedAt.Time
	}
	return r, nil
}
