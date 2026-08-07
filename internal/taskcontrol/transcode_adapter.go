package taskcontrol

import (
	"context"
	"database/sql"
)

// TranscodeAdapter projects transcode_task rows into task-control rows.
type TranscodeAdapter struct{ db *sql.DB }

const transcodePretranscodePredicate = `(LOWER(TRIM(COALESCE(t.task_type,'')))='pretranscode' OR EXISTS(SELECT 1 FROM pretranscode_task_meta pm WHERE pm.task_id=t.id))`

// NewTranscodeAdapter creates a transcode_task source adapter.
func NewTranscodeAdapter(db *sql.DB) *TranscodeAdapter { return &TranscodeAdapter{db: db} }

// Kind returns the persistent source identity used by task details.
func (a *TranscodeAdapter) Kind() string { return "transcode_task" }

func (a *TranscodeAdapter) Read(ctx context.Context, tx *sql.Tx, id int64) (*RawTaskRow, error) {
	row := tx.QueryRowContext(ctx, `SELECT t.id, CASE WHEN `+transcodePretranscodePredicate+` THEN 'pretranscode' ELSE 'transcode' END,
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
	where, args, impossible := buildTranscodeWhere(filters)
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
	where, args, impossible := buildTranscodeWhere(filters)
	if impossible {
		return 0, nil
	}
	var count int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_task t LEFT JOIN media m ON m.id=COALESCE(t.media_id,
		(SELECT mf.id FROM media mf WHERE mf.file_id=t.file_id LIMIT 1))`+where, args...).Scan(&count)
	return count, err
}

func buildTranscodeWhere(f Filters) (string, []any, bool) {
	if f.Removed == "only" {
		return "", nil, true
	}
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 5)
	if f.TaskType == "pretranscode" {
		clauses = append(clauses, transcodePretranscodePredicate)
	} else {
		clauses = append(clauses, "NOT "+transcodePretranscodePredicate)
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
	return " WHERE " + joinWithAnd(clauses), args, false
}
