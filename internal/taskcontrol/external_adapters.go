package taskcontrol

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ScrapeAdapter projects scrape_task, the authoritative scrape execution queue.
type ScrapeAdapter struct{ db *sql.DB }

func NewScrapeAdapter(db *sql.DB) *ScrapeAdapter { return &ScrapeAdapter{db: db} }
func (*ScrapeAdapter) Kind() string              { return "scrape_task" }
func (a *ScrapeAdapter) Read(ctx context.Context, tx *sql.Tx, id int64) (*RawTaskRow, error) {
	r := &RawTaskRow{SourceKind: a.Kind(), SourceID: id, TaskType: "metadata_scrape", MaxAttempts: 3}
	var avail, lease, started, finished sql.NullTime
	var mediaID, libraryID, generation, runID, stepID sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT q.id,COALESCE(q.status,'waiting'),COALESCE(q.fail_count,0),COALESCE(q.retry_round,0),COALESCE(q.priority,0),q.available_at,q.created_at,q.created_at,q.media_id,m.library_id,q.generation,q.ingest_run_id,q.ingest_step_id,COALESCE(q.lease_owner,''),q.lease_until,COALESCE(q.message,''),COALESCE(m.title,''),COALESCE(m.file_path,''),q.started_at,q.finished_at FROM scrape_task q LEFT JOIN media m ON m.id=q.media_id WHERE q.id=?`, id).Scan(&r.SourceID, &r.RawStatus, &r.Attempt, &r.RetryRound, &r.BasePriority, &avail, &r.CreatedAt, &r.UpdatedAt, &mediaID, &libraryID, &generation, &runID, &stepID, &r.Owner, &lease, &r.TerminalReason, &r.MediaTitle, &r.MediaFilePath, &started, &finished)
	if err != nil {
		return nil, err
	}
	if avail.Valid {
		r.AvailableAt = &avail.Time
	}
	if lease.Valid {
		r.LeaseUntil = &lease.Time
	}
	if mediaID.Valid {
		r.MediaID = &mediaID.Int64
	}
	if libraryID.Valid {
		r.LibraryID = &libraryID.Int64
	}
	if generation.Valid {
		r.Generation = generation.Int64
	}
	standalone := !runID.Valid && !stepID.Valid && (!generation.Valid || generation.Int64 == 0)
	r.Linked = !standalone
	if r.Linked && runID.Valid && stepID.Valid && generation.Valid && generation.Int64 > 0 && mediaID.Valid {
		var mediaGeneration, required int64
		var superseded int
		err := tx.QueryRowContext(ctx, `SELECT m.ingest_generation,s.required,CASE WHEN ir.superseded_at IS NOT NULL OR ir.superseded_by_generation IS NOT NULL THEN 1 ELSE 0 END FROM media m JOIN media_ingest_run ir ON ir.id=? AND ir.media_id=m.id AND ir.generation=? JOIN media_ingest_step s ON s.id=? AND s.run_id=ir.id AND s.media_id=m.id AND s.generation=ir.generation WHERE m.id=?`, runID.Int64, generation.Int64, stepID.Int64, mediaID.Int64).Scan(&mediaGeneration, &required, &superseded)
		if err == nil {
			r.LinkValid = true
			r.LinkOptional = required == 0
			r.LinkCurrent = mediaGeneration == generation.Int64 && superseded == 0
			r.LinkStale = !r.LinkCurrent
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return r, nil
}
func (a *ScrapeAdapter) ListIDs(ctx context.Context, tx *sql.Tx, f Filters) ([]int64, error) {
	w, args, bad := simpleExternalWhere(f, "q", "m.library_id", true)
	if bad {
		return nil, nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT q.id FROM scrape_task q LEFT JOIN media m ON m.id=q.media_id`+w+` ORDER BY q.id`, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (a *ScrapeAdapter) Count(ctx context.Context, tx *sql.Tx, f Filters) (int64, error) {
	w, args, bad := simpleExternalWhere(f, "q", "m.library_id", true)
	if bad {
		return 0, nil
	}
	var n int64
	e := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scrape_task q LEFT JOIN media m ON m.id=q.media_id`+w, args...).Scan(&n)
	return n, e
}

// ScanAdapter projects process-level library scans.
type ScanAdapter struct{ db *sql.DB }

func NewScanAdapter(db *sql.DB) *ScanAdapter { return &ScanAdapter{db: db} }
func (*ScanAdapter) Kind() string            { return "scan_task" }
func (a *ScanAdapter) Read(ctx context.Context, tx *sql.Tx, id int64) (*RawTaskRow, error) {
	r := &RawTaskRow{SourceKind: a.Kind(), SourceID: id, TaskType: "scan", MaxAttempts: 1}
	var lib int64
	var lease, started, finished sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT t.id,CASE WHEN COALESCE(t.cancelled,0)=1 THEN 'cancelled' ELSE COALESCE(t.status,'waiting') END,t.library_id,COALESCE(l.owner_id,''),l.lease_until,COALESCE(t.error_message,''),t.created_at,t.updated_at,t.started_at,t.finished_at FROM scan_task t LEFT JOIN scan_lease l ON l.scan_task_id=t.id WHERE t.id=?`, id).Scan(&r.SourceID, &r.RawStatus, &lib, &r.Owner, &lease, &r.TerminalReason, &r.CreatedAt, &r.UpdatedAt, &started, &finished)
	if err != nil {
		return nil, err
	}
	r.LibraryID = &lib
	if lease.Valid {
		r.LeaseUntil = &lease.Time
	}
	return r, nil
}
func (a *ScanAdapter) ListIDs(ctx context.Context, tx *sql.Tx, f Filters) ([]int64, error) {
	w, args, bad := simpleExternalWhere(f, "t", "t.library_id", false)
	if bad {
		return nil, nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT t.id FROM scan_task t LEFT JOIN scan_lease l ON l.scan_task_id=t.id`+w+` ORDER BY t.id`, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (a *ScanAdapter) Count(ctx context.Context, tx *sql.Tx, f Filters) (int64, error) {
	w, args, bad := simpleExternalWhere(f, "t", "t.library_id", false)
	if bad {
		return 0, nil
	}
	var n int64
	e := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scan_task t LEFT JOIN scan_lease l ON l.scan_task_id=t.id`+w, args...).Scan(&n)
	return n, e
}

func simpleExternalWhere(f Filters, alias, libraryExpr string, hasGeneration bool) (string, []any, bool) {
	if f.Removed == "only" || f.TaskType == "__impossible__" {
		return "", nil, true
	}
	var c []string
	var a []any
	if f.Status != "" {
		statuses := []string{f.Status}
		if f.Status == "failed" {
			statuses = append(statuses, "abandoned")
		}
		ph := make([]string, len(statuses))
		for i, v := range statuses {
			ph[i] = "?"
			a = append(a, v)
		}
		c = append(c, alias+".status IN ("+strings.Join(ph, ",")+")")
	}
	if f.LibraryID != nil {
		c = append(c, libraryExpr+" = ?")
		a = append(a, *f.LibraryID)
	}
	if f.Generation != nil {
		if !hasGeneration {
			return "", nil, true
		}
		c = append(c, "COALESCE("+alias+".generation,0) = ?")
		a = append(a, *f.Generation)
	}
	if f.Owner != "" {
		ownerExpr := alias + ".lease_owner"
		if alias == "t" {
			ownerExpr = "l.owner_id"
		}
		c = append(c, ownerExpr+" = ?")
		a = append(a, f.Owner)
	}
	if len(c) == 0 {
		return "", a, false
	}
	return " WHERE " + joinWithAnd(c), a, false
}
