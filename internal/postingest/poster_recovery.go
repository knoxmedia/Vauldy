package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"knox-media/internal/store"
)

type PosterRecoveryRoots struct{ Upload, Derived string }
type repairPosterJournalRow struct {
	posterJournalRow
	queueID int64
	attempt int
}

type posterJournalRow struct {
	stageID                                       string
	mediaID, runID, stepID, generation            int64
	owner, fingerprint, state, stagedPath, hashes string
}

func ReconcilePosterStages(ctx context.Context, db *sql.DB, roots PosterRecoveryRoots, limit int) (checked, cleaned int, retErr error) {
	if db == nil {
		return 0, 0, errors.New("poster stage reconcile: database is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `SELECT stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,state,staged_path,hashes_sizes_json FROM media_asset_stage_journal WHERE artifact_kind='poster' AND recovery_error NOT IN ('cleaned_unreferenced','verified_committed') ORDER BY updated_at,stage_id LIMIT ?`, limit)
	if err != nil {
		return 0, 0, err
	}
	var batch []posterJournalRow
	for rows.Next() {
		var r posterJournalRow
		if err = rows.Scan(&r.stageID, &r.mediaID, &r.runID, &r.stepID, &r.generation, &r.owner, &r.fingerprint, &r.state, &r.stagedPath, &r.hashes); err != nil {
			rows.Close()
			return checked, cleaned, err
		}
		batch = append(batch, r)
	}
	if err = rows.Close(); err != nil {
		return checked, cleaned, err
	}
	for _, r := range batch {
		checked++
		committed, active, err := posterStageAuthority(ctx, db, r)
		if err != nil {
			return checked, cleaned, err
		}
		if committed {
			_, _ = db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error='verified_committed',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, r.stageID)
			continue
		}
		if active {
			continue
		}
		if r.state == "staged" {
			var claimed int64
			_, err = store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
				res, e := tx.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='quarantined',quarantine_path=staged_path,recovery_error='quarantined_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND artifact_kind='poster' AND state='staged' AND NOT EXISTS(SELECT 1 FROM media_ingest_evidence WHERE stage_id=?) AND NOT EXISTS(SELECT 1 FROM post_ingest_task p JOIN media_ingest_run run ON run.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.media_id=? AND p.ingest_run_id=? AND p.generation=? AND p.status='running' AND p.lease_owner=? AND run.superseded_at IS NULL AND run.superseded_by_generation IS NULL AND m.ingest_generation=p.generation)`, r.stageID, r.stageID, r.mediaID, r.runID, r.generation, r.owner)
				if e == nil {
					claimed, _ = res.RowsAffected()
				}
				return e
			})
			if err != nil {
				return checked, cleaned, err
			}
			if claimed != 1 {
				continue
			}
		} else if r.state != "quarantined" {
			continue
		}
		path, err := posterJournalPath(r)
		if err != nil {
			return checked, cleaned, err
		}
		if err = validateExactPosterStagePaths(roots, r, path); err != nil {
			_, _ = db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error=? WHERE stage_id=?`, `unsafe_path: `+err.Error(), r.stageID)
			return checked, cleaned, err
		}
		refs, err := posterPathReferenceCount(ctx, db, path, "", r.stageID, posterJournalOrdinary)
		if err != nil {
			return checked, cleaned, err
		}
		if refs > 0 {
			continue
		}
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			retErr = err
			continue
		}
		res, err := db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error='cleaned_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantined'`, r.stageID)
		if err != nil {
			return checked, cleaned, err
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			cleaned++
		}
	}
	remaining := limit
	if remaining > 0 {
		rows, e := db.QueryContext(ctx, `SELECT stage_id,queue_id,media_id,run_id,generation,owner_token,attempt,source_fingerprint,state,staged_path,hashes_sizes_json FROM poster_repair_stage WHERE recovery_error NOT IN ('cleaned_unreferenced','verified_committed') ORDER BY updated_at,stage_id LIMIT ?`, remaining)
		if e != nil {
			return checked, cleaned, e
		}
		var repairs []repairPosterJournalRow
		for rows.Next() {
			var r repairPosterJournalRow
			if e = rows.Scan(&r.stageID, &r.queueID, &r.mediaID, &r.runID, &r.generation, &r.owner, &r.attempt, &r.fingerprint, &r.state, &r.stagedPath, &r.hashes); e != nil {
				rows.Close()
				return checked, cleaned, e
			}
			repairs = append(repairs, r)
		}
		if e = rows.Close(); e != nil {
			return checked, cleaned, e
		}
		for _, rr := range repairs {
			r, queueID, attempt := rr.posterJournalRow, rr.queueID, rr.attempt
			checked++
			var committed, active int
			e = db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM poster_repair_stage s JOIN post_ingest_task p ON p.id=s.queue_id JOIN media m ON m.id=s.media_id WHERE s.stage_id=? AND s.queue_id=? AND s.attempt=? AND s.state='committed' AND p.status='done' AND (json_extract(m.meta_json,'$.scrape.poster')=json_extract(s.hashes_sizes_json,'$.url') OR json_extract(m.meta_json,'$.scrape.extra.poster')=json_extract(s.hashes_sizes_json,'$.url'))),(SELECT COUNT(*) FROM poster_repair_stage s JOIN post_ingest_task p ON p.id=s.queue_id JOIN media_ingest_run run ON run.id=s.run_id JOIN media m ON m.id=s.media_id WHERE s.stage_id=? AND s.queue_id=? AND s.attempt=? AND p.task_type='poster_repair' AND p.status='running' AND p.lease_owner=s.owner_token AND p.attempts=s.attempt AND p.ingest_run_id=s.run_id AND p.generation=s.generation AND p.ingest_step_id IS NULL AND run.superseded_at IS NULL AND run.superseded_by_generation IS NULL AND m.ingest_generation=s.generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL)`, r.stageID, queueID, attempt, r.stageID, queueID, attempt).Scan(&committed, &active)
			if e != nil {
				return checked, cleaned, e
			}
			if committed == 1 {
				_, _ = db.ExecContext(ctx, `UPDATE poster_repair_stage SET recovery_error='verified_committed',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, r.stageID)
				continue
			}
			if active == 1 {
				continue
			}
			if r.state == "staged" {
				var claimed int64
				_, e = store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
					res, x := tx.ExecContext(ctx, `UPDATE poster_repair_stage SET state='quarantined',recovery_error='quarantined_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND queue_id=? AND attempt=? AND state='staged' AND NOT EXISTS(SELECT 1 FROM post_ingest_task p JOIN media_ingest_run run ON run.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.task_type='poster_repair' AND p.status='running' AND p.lease_owner=? AND p.attempts=? AND run.superseded_at IS NULL AND run.superseded_by_generation IS NULL AND m.ingest_generation=p.generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL)`, r.stageID, queueID, attempt, queueID, r.owner, attempt)
					if x == nil {
						claimed, _ = res.RowsAffected()
					}
					return x
				})
				if e != nil {
					return checked, cleaned, e
				}
				if claimed != 1 {
					continue
				}
			}
			path, x := posterJournalPath(r)
			if x != nil {
				return checked, cleaned, x
			}
			if x = validateExactPosterStagePaths(roots, r, path); x != nil {
				return checked, cleaned, x
			}
			refs, x := posterPathReferenceCount(ctx, db, path, "", r.stageID, posterJournalRepair)
			if x != nil {
				return checked, cleaned, x
			}
			if refs > 0 {
				continue
			}
			if x = os.Remove(path); x != nil && !os.IsNotExist(x) {
				return checked, cleaned, x
			}
			res, x := db.ExecContext(ctx, `UPDATE poster_repair_stage SET recovery_error='cleaned_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND queue_id=? AND attempt=? AND state='quarantined'`, r.stageID, queueID, attempt)
			if x != nil {
				return checked, cleaned, x
			}
			n, _ := res.RowsAffected()
			if n == 1 {
				cleaned++
			}
		}
	}
	return checked, cleaned, retErr
}
func posterStageAuthority(ctx context.Context, db *sql.DB, r posterJournalRow) (bool, bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence e JOIN post_ingest_task p ON p.ingest_step_id=e.step_id WHERE e.stage_id=? AND e.kind='poster' AND e.media_id=? AND e.run_id=? AND e.generation=? AND e.source_fingerprint=? AND p.status='done'`, r.stageID, r.mediaID, r.runID, r.generation, r.fingerprint).Scan(&n)
	if err != nil {
		return false, false, err
	}
	if n == 1 {
		return true, false, nil
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task p JOIN media_ingest_run run ON run.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.media_id=? AND p.ingest_run_id=? AND p.generation=? AND p.task_type IN ('poster','poster_repair') AND p.status='running' AND p.lease_owner=? AND run.superseded_at IS NULL AND run.superseded_by_generation IS NULL AND m.ingest_generation=p.generation`, r.mediaID, r.runID, r.generation, r.owner).Scan(&n)
	return false, n == 1, err
}
func posterJournalPath(r posterJournalRow) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(r.hashes), &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("poster stage %s has no artifact path", r.stageID)
	}
	return filepath.Clean(p.Path), nil
}
func validatePosterRecoveryPath(roots PosterRecoveryRoots, r posterJournalRow, path string) error {
	for _, root := range []string{roots.Upload, roots.Derived} {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, _ := filepath.Abs(root)
		absPath, _ := filepath.Abs(path)
		rel, err := filepath.Rel(absRoot, absPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			if strings.Contains(filepath.ToSlash(absPath), fmt.Sprintf("generation-%d", r.generation)) || strings.EqualFold(filepath.Ext(path), ".enc") {
				return nil
			}
		}
	}
	return fmt.Errorf("poster path outside configured generation roots")
}
func RunPosterStageReconciler(ctx context.Context, db *sql.DB, roots PosterRecoveryRoots, interval time.Duration, limit int, report func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		_, _, err := ReconcilePosterStages(ctx, db, roots, limit)
		if err != nil && report != nil {
			report(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
