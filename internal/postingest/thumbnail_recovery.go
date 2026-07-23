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

	"knox-media/internal/imagethumb"
	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

const thumbnailStageBatchMax = 100

var afterThumbnailStageQuarantined = func() {}

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
	rows, err := db.QueryContext(ctx, `SELECT stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,state,staged_path,hashes_sizes_json FROM media_asset_stage_journal WHERE artifact_kind='thumbnail' AND recovery_error NOT IN ('cleaned_unreferenced','verified_committed') ORDER BY updated_at,stage_id LIMIT ?`, limit)
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
		if r.state == "committed" {
			if err := verifyCommittedThumbnailJournal(ctx, db, r); err != nil {
				_, _ = db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error=? WHERE stage_id=?`, "committed_corrupt: "+err.Error(), r.stageID)
				return checked, cleaned, err
			}
			_, _ = db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error='verified_committed',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, r.stageID)
			continue
		}
		if r.state == "staged" {
			committed, active, err := thumbnailStageAuthority(ctx, db, r)
			if err != nil {
				return checked, cleaned, err
			}
			if committed || active {
				continue
			}
		}
		claimed := int64(1)
		if r.state == "staged" {
			_, err = store.WithImmediateConnTx(ctx, db, func(tx store.ImmediateConnTx) error {
				result, err := tx.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='quarantined',quarantine_path=staged_path,recovery_error='quarantined_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND artifact_kind='thumbnail' AND state='staged' AND media_id=? AND run_id=? AND step_id=? AND generation=? AND owner_token=? AND source_fingerprint=? AND NOT EXISTS(SELECT 1 FROM media_ingest_evidence WHERE stage_id=?) AND NOT EXISTS(SELECT 1 FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_run run ON run.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.media_id=? AND p.ingest_run_id=? AND p.ingest_step_id=? AND p.generation=? AND p.task_type='thumbnail' AND p.status='running' AND p.lease_owner=? AND s.status='running' AND s.lease_owner=p.lease_owner AND run.status='processing' AND run.superseded_at IS NULL AND run.superseded_by_generation IS NULL AND m.ingest_generation=p.generation)`, r.stageID, r.mediaID, r.runID, r.stepID, r.generation, r.owner, r.fingerprint, r.stageID, r.mediaID, r.runID, r.stepID, r.generation, r.owner)
				if err != nil {
					return err
				}
				claimed, _ = result.RowsAffected()
				return nil
			})
			if err != nil {
				return checked, cleaned, err
			}
			if claimed != 1 {
				continue
			}
			afterThumbnailStageQuarantined()
		} else if r.state != "quarantined" {
			continue
		}
		paths, err := thumbnailJournalPaths(r)
		if err != nil {
			return checked, cleaned, err
		}
		managedRoot := filepath.Dir(filepath.Dir(r.stagedPath))
		for i, path := range paths {
			expected := "thumb.jpg"
			if i == 1 {
				expected = "medium.jpg"
			}
			if pathErr := validateThumbnailManagedPath(managedRoot, r.stageID, expected, path); pathErr != nil {
				_, _ = db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error=? WHERE stage_id=?`, "unsafe_path: "+pathErr.Error(), r.stageID)
				return checked, cleaned, pathErr
			}
		}
		referenced := false
		for _, path := range paths {
			refs, refErr := thumbnailPathReferenceCount(ctx, db, path)
			if refErr != nil {
				return checked, cleaned, refErr
			}
			if refs > 0 {
				referenced = true
			}
		}
		if referenced {
			_, _ = db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error='quarantined_referenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantined'`, r.stageID)
			continue
		}
		if err = cleanupUnreferencedThumbnailPaths(ctx, db, paths); err != nil {
			retErr = err
			continue
		}
		result, err := db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET recovery_error='cleaned_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='quarantined' AND recovery_error<>'cleaned_unreferenced'`, r.stageID)
		if err != nil {
			return checked, cleaned, err
		}
		n, _ := result.RowsAffected()
		if n == 1 {
			cleaned++
		}
	}
	return checked, cleaned, retErr
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

func verifyCommittedThumbnailJournal(ctx context.Context, db *sql.DB, r thumbnailJournalRow) error {
	var task Task
	task.MediaID = r.mediaID
	task.Generation = r.generation
	task.LeaseOwner = r.owner
	task.RunID = &r.runID
	task.StepID = &r.stepID
	if err := db.QueryRowContext(ctx, `SELECT id,attempts,task_type FROM post_ingest_task WHERE media_id=? AND ingest_run_id=? AND ingest_step_id=? AND generation=?`, r.mediaID, r.runID, r.stepID, r.generation).Scan(&task.ID, &task.Attempts, &task.Type); err != nil {
		return err
	}
	paths, err := thumbnailJournalPaths(r)
	if err != nil {
		return err
	}
	if len(paths) != 2 {
		return fmt.Errorf("thumbnail committed journal variants invalid")
	}
	var payload map[string]struct {
		Kind        string `json:"kind"`
		LogicalName string `json:"logical_name"`
		Path        string `json:"path"`
		Size        int64  `json:"size"`
		SHA256      string `json:"sha256"`
		Derived     *struct {
			MediaID     int64  `json:"media_id"`
			Kind        string `json:"kind"`
			LogicalName string `json:"logical_name"`
			EncPath     string `json:"enc_path"`
			WrappedDEK  string `json:"wrapped_dek"`
			IV          string `json:"iv"`
		} `json:"derived"`
	}
	if err = json.Unmarshal([]byte(r.hashes), &payload); err != nil {
		return err
	}
	staged := imagethumb.StagedThumbnail{Stage: publication.StageRecord{StageID: r.stageID, Request: publication.StageRequest{MediaID: r.mediaID, RunID: r.runID, StepID: r.stepID, Generation: r.generation, OwnerToken: r.owner, SourceFingerprint: r.fingerprint}}}
	thumb := payload["thumb"]
	medium := payload["medium"]
	staged.Thumb = imagethumb.StagedVariant{Kind: thumb.Kind, LogicalName: thumb.LogicalName, Path: thumb.Path, Size: thumb.Size, Hash: thumb.SHA256}
	staged.Medium = imagethumb.StagedVariant{Kind: medium.Kind, LogicalName: medium.LogicalName, Path: medium.Path, Size: medium.Size, Hash: medium.SHA256}
	if thumb.Derived != nil {
		staged.Thumb.Derived = storage.RestoreStagedDerivedAsset(thumb.Derived.MediaID, thumb.Derived.Kind, thumb.Derived.LogicalName, thumb.Derived.EncPath, thumb.Derived.WrappedDEK, thumb.Derived.IV)
	}
	if medium.Derived != nil {
		staged.Medium.Derived = storage.RestoreStagedDerivedAsset(medium.Derived.MediaID, medium.Derived.Kind, medium.Derived.LogicalName, medium.Derived.EncPath, medium.Derived.WrappedDEK, medium.Derived.IV)
	}
	return verifyCommittedThumbnailTx(ctx, db, task, staged)
}

func validateThumbnailManagedPath(root, stageID, basename, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	expected := filepath.Join(rootAbs, "generation-"+strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(path))), "generation-"), stageID, basename)
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if filepath.Base(pathAbs) != basename || filepath.Base(filepath.Dir(pathAbs)) != stageID {
		return fmt.Errorf("thumbnail recovery path identity mismatch")
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("thumbnail recovery path outside managed root")
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(pathAbs))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		realRel, relErr := filepath.Rel(rootAbs, parentReal)
		if relErr != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("thumbnail recovery symlink escape")
		}
	}
	_ = expected
	return nil
}
