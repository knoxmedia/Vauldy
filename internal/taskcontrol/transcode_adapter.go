package taskcontrol

import (
	"context"
	"database/sql"
)

// TranscodeAdapter projects transcode_task rows into task-control rows.
type TranscodeAdapter struct {
	db *sql.DB
	// pretranscodeMetaExists is true when the pretranscode_task_meta table is
	// present. The community build never creates it (the commercial
	// pretranscode subsystem is excluded), so queries must not reference it.
	pretranscodeMetaExists bool
}

// NewTranscodeAdapter creates a transcode_task source adapter.
func NewTranscodeAdapter(db *sql.DB) *TranscodeAdapter {
	a := &TranscodeAdapter{db: db}
	a.pretranscodeMetaExists = a.tableExists("pretranscode_task_meta")
	return a
}

func (a *TranscodeAdapter) tableExists(table string) bool {
	var n int
	if err := a.db.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// pretranscodePredicate returns a SQL predicate identifying pretranscode rows,
// or "" when the meta table is absent (no pretranscode rows can exist).
func (a *TranscodeAdapter) pretranscodePredicate() string {
	if !a.pretranscodeMetaExists {
		return ""
	}
	return `(LOWER(TRIM(COALESCE(t.task_type,'')))='pretranscode' OR EXISTS(SELECT 1 FROM pretranscode_task_meta pm WHERE pm.task_id=t.id))`
}

// taskTypeExpr returns a SQL expression classifying a transcode_task row as
// 'pretranscode' or 'transcode'.
func (a *TranscodeAdapter) taskTypeExpr() string {
	pred := a.pretranscodePredicate()
	if pred == "" {
		return `'transcode'`
	}
	return `CASE WHEN ` + pred + ` THEN 'pretranscode' ELSE 'transcode' END`
}

// Kind returns the persistent source identity used by task details.
func (a *TranscodeAdapter) Kind() string { return "transcode_task" }

func (a *TranscodeAdapter) Read(ctx context.Context, tx *sql.Tx, id int64) (*RawTaskRow, error) {
	row := tx.QueryRowContext(ctx, `SELECT t.id, `+a.taskTypeExpr()+`,
		COALESCE(t.status,'waiting'), COALESCE(t.generation,0), COALESCE(t.retry_round,0), COALESCE(t.lease_owner,''),
		t.lease_until, COALESCE(t.error_message,''), t.created_at, t.created_at,
		m.id, COALESCE(m.title,''), COALESCE(m.file_path,''), m.library_id,
		CASE WHEN t.ingest_run_id IS NOT NULL OR t.ingest_step_id IS NOT NULL OR t.generation IS NOT NULL THEN 1 ELSE 0 END
		FROM transcode_task t LEFT JOIN media m ON m.id=COALESCE(t.media_id,
		(SELECT mf.id FROM media mf WHERE mf.file_id=t.file_id LIMIT 1)) WHERE t.id=?`, id)
	r := &RawTaskRow{SourceKind: a.Kind(), SourceID: id}
	var leaseUntil sql.NullTime
	var mediaID, libraryID sql.NullInt64
	var linked int
	if err := row.Scan(&r.SourceID, &r.TaskType, &r.RawStatus, &r.Generation, &r.RetryRound, &r.Owner, &leaseUntil,
		&r.TerminalReason, &r.CreatedAt, &r.UpdatedAt, &mediaID, &r.MediaTitle, &r.MediaFilePath, &libraryID, &linked); err != nil {
		return nil, err
	}
	r.Linked = linked == 1
	if leaseUntil.Valid {
		r.LeaseUntil = &leaseUntil.Time
	}
	if mediaID.Valid {
		r.MediaID = &mediaID.Int64
	}
	if libraryID.Valid {
		r.LibraryID = &libraryID.Int64
	}
	return r, nil
}

func (a *TranscodeAdapter) ListIDs(ctx context.Context, tx *sql.Tx, filters Filters) ([]int64, error) {
	where, args, impossible := a.buildTranscodeWhere(filters)
	if impossible {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id FROM transcode_task t LEFT JOIN media m ON m.id=COALESCE(t.media_id,
		(SELECT mf.id FROM media mf WHERE mf.file_id=t.file_id LIMIT 1))`+where+` ORDER BY t.id ASC`, args...)
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

func (a *TranscodeAdapter) Count(ctx context.Context, tx *sql.Tx, filters Filters) (int64, error) {
	where, args, impossible := a.buildTranscodeWhere(filters)
	if impossible {
		return 0, nil
	}
	var count int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_task t LEFT JOIN media m ON m.id=COALESCE(t.media_id,
		(SELECT mf.id FROM media mf WHERE mf.file_id=t.file_id LIMIT 1))`+where, args...).Scan(&count)
	return count, err
}

func (a *TranscodeAdapter) buildTranscodeWhere(f Filters) (string, []any, bool) {
	if f.Removed == "only" {
		return "", nil, true
	}
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 5)
	pred := a.pretranscodePredicate()
	if f.TaskType == "pretranscode" {
		if pred == "" {
			// No pretranscode rows exist without the meta table.
			return "", nil, true
		}
		clauses = append(clauses, pred)
	} else if pred != "" {
		clauses = append(clauses, "NOT "+pred)
	}
	if f.Status != "" {
		clauses = append(clauses, "t.status = ?")
		args = append(args, f.Status)
	}
	if f.Generation != nil {
		clauses = append(clauses, "COALESCE(t.generation,0) = ?")
		args = append(args, *f.Generation)
	}
	if f.LibraryID != nil {
		clauses = append(clauses, "m.library_id = ?")
		args = append(args, *f.LibraryID)
	}
	if f.Owner != "" {
		clauses = append(clauses, "t.lease_owner = ?")
		args = append(args, f.Owner)
	}
	if len(clauses) == 0 {
		return "", nil, false
	}
	return " WHERE " + joinWithAnd(clauses), args, false
}
