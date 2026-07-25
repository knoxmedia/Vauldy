package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"knox-media/internal/imagethumb"
	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

const thumbnailStageBatchMax = 100

type ThumbnailRecoveryRoots struct{ Preview, Derived string }

var afterThumbnailStageQuarantined = func() {}

type thumbnailJournalRow struct {
	stageID                                       string
	mediaID, runID, stepID, generation            int64
	owner, fingerprint, state, stagedPath, hashes string
}

// ReconcileThumbnailStages safely reconciles a bounded batch of durable thumbnail stages.
func ReconcileThumbnailStages(ctx context.Context, db *sql.DB, roots ThumbnailRecoveryRoots, limit int) (checked, cleaned int, retErr error) {
	if db == nil {
		return 0, 0, errors.New("thumbnail stage reconcile: database is required")
	}
	if limit <= 0 || limit > thumbnailStageBatchMax {
		limit = thumbnailStageBatchMax
	}
	rows, err := db.QueryContext(ctx, `SELECT j.stage_id,j.media_id,j.run_id,j.step_id,j.generation,j.owner_token,j.source_fingerprint,j.state,j.staged_path,j.hashes_sizes_json FROM media_asset_stage_journal j WHERE j.artifact_kind='thumbnail' AND j.state IN ('staged','quarantined','committed') AND j.recovery_error NOT IN ('cleaned_unreferenced','verified_committed') AND (j.state<>'staged' OR NOT EXISTS(SELECT 1 FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_run run ON run.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.media_id=j.media_id AND p.ingest_run_id=j.run_id AND p.ingest_step_id=j.step_id AND p.generation=j.generation AND p.task_type='thumbnail' AND p.status='running' AND p.lease_owner=j.owner_token AND s.status='running' AND s.lease_owner=p.lease_owner AND run.status='processing' AND run.superseded_at IS NULL AND run.superseded_by_generation IS NULL AND m.ingest_generation=p.generation)) ORDER BY j.updated_at,j.stage_id LIMIT ?`, limit)
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
				if _, updateErr := db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='failed_closed',recovery_error=?,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='committed'`, boundedRecoveryError("committed_corrupt: ", err), r.stageID); updateErr != nil {
					return checked, cleaned, updateErr
				}
				retErr = errors.Join(retErr, err)
				continue
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
			if updateErr := failClosedAssetStage(ctx, db, r.stageID, err); updateErr != nil {
				return checked, cleaned, updateErr
			}
			retErr = errors.Join(retErr, err)
			continue
		}
		meta, metaErr := thumbnailJournalVariantMetadata(r)
		if metaErr != nil {
			if updateErr := failClosedAssetStage(ctx, db, r.stageID, metaErr); updateErr != nil {
				return checked, cleaned, updateErr
			}
			retErr = errors.Join(retErr, metaErr)
			continue
		}
		for i, path := range paths {
			key := "thumb"
			expected := "thumb.jpg"
			if i == 1 {
				key = "medium"
				expected = "medium.jpg"
			}
			var pathErr error
			if meta[key].Derived != nil {
				pathErr = validateDerivedThumbnailPath(roots.Derived, meta[key].Derived.MediaID, meta[key].Derived.Kind, meta[key].Derived.LogicalName, path)
			} else {
				pathErr = validateThumbnailManagedPath(roots.Preview, r.generation, r.stageID, expected, path)
			}
			if pathErr != nil {
				if updateErr := failClosedAssetStage(ctx, db, r.stageID, pathErr); updateErr != nil {
					return checked, cleaned, updateErr
				}
				retErr = errors.Join(retErr, pathErr)
				paths = nil
				break
			}
		}
		if paths == nil {
			continue
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

func RunThumbnailStageReconciler(ctx context.Context, db *sql.DB, roots ThumbnailRecoveryRoots, interval time.Duration, limit int, report func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		_, _, err := ReconcileThumbnailStages(ctx, db, roots, limit)
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
	restore := func(meta *struct {
		MediaID     int64  `json:"media_id"`
		Kind        string `json:"kind"`
		LogicalName string `json:"logical_name"`
		EncPath     string `json:"enc_path"`
		WrappedDEK  string `json:"wrapped_dek"`
		IV          string `json:"iv"`
	}) (*storage.StagedDerivedAsset, error) {
		if meta == nil {
			return nil, nil
		}
		var path, wrapped, iv string
		err := db.QueryRowContext(ctx, `SELECT enc_path,wrapped_dek,iv FROM media_derived_assets WHERE media_id=? AND artifact_kind=? AND logical_name=?`, meta.MediaID, meta.Kind, meta.LogicalName).Scan(&path, &wrapped, &iv)
		if err != nil {
			return nil, err
		}
		if !pathsEqual(path, meta.EncPath) || strings.TrimSpace(wrapped) == "" || strings.TrimSpace(iv) == "" {
			return nil, fmt.Errorf("thumbnail committed derived identity invalid")
		}
		return storage.RestoreStagedDerivedAsset(meta.MediaID, meta.Kind, meta.LogicalName, path, wrapped, iv), nil
	}
	staged.Thumb.Derived, err = restore(thumb.Derived)
	if err != nil {
		return err
	}
	staged.Medium.Derived, err = restore(medium.Derived)
	if err != nil {
		return err
	}
	return verifyCommittedThumbnailTx(ctx, db, task, staged)
}

func validateThumbnailManagedPath(root string, generation int64, stageID, basename, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	expected := filepath.Join(rootAbs, fmt.Sprintf("generation-%d", generation), stageID, basename)
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	same := filepath.Clean(pathAbs) == filepath.Clean(expected)
	if runtime.GOOS == "windows" {
		same = strings.EqualFold(filepath.Clean(pathAbs), filepath.Clean(expected))
	}
	if !same {
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
		rootReal, rootErr := filepath.EvalSymlinks(rootAbs)
		if rootErr != nil {
			return rootErr
		}
		realRel, relErr := filepath.Rel(rootReal, parentReal)
		if relErr != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("thumbnail recovery symlink escape")
		}
	}
	return nil
}

func pathsEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

type thumbnailRecoveryVariant struct {
	Path    string `json:"path"`
	Derived *struct {
		MediaID     int64  `json:"media_id"`
		Kind        string `json:"kind"`
		LogicalName string `json:"logical_name"`
		EncPath     string `json:"enc_path"`
	} `json:"derived"`
}

func thumbnailJournalVariantMetadata(r thumbnailJournalRow) (map[string]thumbnailRecoveryVariant, error) {
	var m map[string]thumbnailRecoveryVariant
	err := json.Unmarshal([]byte(r.hashes), &m)
	return m, err
}
func validateDerivedThumbnailPath(root string, mediaID int64, kind, logical, path string) error {
	if root == "" || mediaID <= 0 || (kind != "photo_thumb" && kind != "photo_medium") || ((kind == "photo_thumb" && logical != "thumb.jpg") || (kind == "photo_medium" && logical != "medium.jpg")) {
		return fmt.Errorf("thumbnail derived identity invalid")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	dir := filepath.Join(rootAbs, fmt.Sprintf("%d", mediaID), kind)
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(dir, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Dir(pathAbs) != dir {
		return fmt.Errorf("thumbnail derived path outside managed root")
	}
	base := filepath.Base(pathAbs)
	prefix := logical + "."
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".enc") || len(strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".enc")) < 32 {
		return fmt.Errorf("thumbnail derived filename invalid")
	}
	return validateContainedSymlinks(rootAbs, pathAbs)
}
func validateContainedSymlinks(root, path string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		rel, e := filepath.Rel(rootReal, parentReal)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("thumbnail recovery symlink escape")
		}
	}
	return nil
}

func boundedRecoveryError(prefix string, err error) string {
	message := prefix + err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}
func failClosedAssetStage(ctx context.Context, db *sql.DB, stageID string, cause error) error {
	_, err := db.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='failed_closed',recovery_error=?,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state IN ('staged','quarantined','committed')`, boundedRecoveryError("recovery_error: ", cause), stageID)
	return err
}
