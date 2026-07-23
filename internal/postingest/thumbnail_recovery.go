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
)

const thumbnailStageBatchMax = 100

type thumbnailJournalRow struct {
	stageID                                       string
	mediaID, runID, stepID, generation            int64
	owner, fingerprint, state, stagedPath, hashes string
}

// ReconcileThumbnailStages safely reconciles a bounded batch of durable thumbnail stages.
func ReconcileThumbnailStages(ctx context.Context, db *sql.DB, limit int) (checked, cleaned int, retErr error) {
	if db == nil {
		return 0, 0, errors.New("thumbnail stage reconcile: database is required")
	}
	if limit <= 0 || limit > thumbnailStageBatchMax {
		limit = thumbnailStageBatchMax
	}
	rows, err := db.QueryContext(ctx, `SELECT stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,state,staged_path,hashes_sizes_json FROM media_asset_stage_journal WHERE artifact_kind='thumbnail' AND recovery_error<>'cleaned_unreferenced' ORDER BY updated_at,stage_id LIMIT ?`, limit)
	if err != nil {
		return 0, 0, err
	}
	var batch []thumbnailJournalRow
	for rows.Next() {
		var r thumbnailJournalRow
		if err = rows.Scan(&r.stageID, &r.mediaID, &r.runID, &r.stepID, &r.generation, &r.owner, &r.fingerprint, &r.state, &r.stagedPath, &r.hashes); err != nil {
			rows.Close()
			return checked, cleaned, err
		}
		batch = append(batch, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return checked, cleaned, err
	}
	if err = rows.Close(); err != nil {
		return checked, cleaned, err
	}
	for _, r := range batch {
		checked++
		committed, active, err := thumbnailStageAuthority(ctx, db, r)
		if err != nil {
			return checked, cleaned, err
		}
		if committed || active {
			continue
		}
		paths, err := thumbnailJournalPaths(r)
		if err != nil {
			return checked, cleaned, err
		}
		if err = cleanupUnreferencedThumbnailPaths(ctx, db, paths); err != nil {
			return checked, cleaned, err
		}
		res, err := db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error='cleaned_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged' AND NOT EXISTS(SELECT 1 FROM media_ingest_evidence WHERE stage_id=?)`, r.stageID, r.stageID)
		if err != nil {
			return checked, cleaned, err
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			cleaned++
		}
	}
	return checked, cleaned, nil
}
func thumbnailStageAuthority(ctx context.Context, db *sql.DB, r thumbnailJournalRow) (committed, active bool, err error) {
	var n int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence e JOIN post_ingest_task p ON p.ingest_step_id=e.step_id WHERE e.stage_id=? AND e.kind='thumbnail' AND e.media_id=? AND e.run_id=? AND e.step_id=? AND e.generation=? AND e.source_fingerprint=? AND p.status='done'`, r.stageID, r.mediaID, r.runID, r.stepID, r.generation, r.fingerprint).Scan(&n)
	if err != nil {
		return false, false, err
	}
	if n == 1 {
		return true, false, nil
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_run run ON run.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.media_id=? AND p.ingest_run_id=? AND p.ingest_step_id=? AND p.generation=? AND p.task_type='thumbnail' AND p.status='running' AND p.lease_owner=? AND s.status='running' AND s.lease_owner=p.lease_owner AND run.status='processing' AND run.superseded_at IS NULL AND run.superseded_by_generation IS NULL AND m.ingest_generation=p.generation`, r.mediaID, r.runID, r.stepID, r.generation, r.owner).Scan(&n)
	return false, n == 1, err
}
func thumbnailJournalPaths(r thumbnailJournalRow) ([]string, error) {
	var payload map[string]struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(r.hashes), &payload); err != nil {
		return nil, fmt.Errorf("thumbnail stage %s hashes: %w", r.stageID, err)
	}
	var paths []string
	for _, key := range []string{"thumb", "medium"} {
		if p := strings.TrimSpace(payload[key].Path); p != "" {
			paths = append(paths, filepath.Clean(p))
		}
	}
	if len(paths) == 0 && r.stagedPath != "" {
		entries, err := os.ReadDir(r.stagedPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				paths = append(paths, filepath.Join(r.stagedPath, entry.Name()))
			}
		}
	}
	return paths, nil
}

func RunThumbnailStageReconciler(ctx context.Context, db *sql.DB, interval time.Duration, limit int, report func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		_, _, err := ReconcileThumbnailStages(ctx, db, limit)
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
