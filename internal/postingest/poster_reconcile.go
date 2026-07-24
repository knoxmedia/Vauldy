package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func reconcilePosterJournalAuthoritative(ctx context.Context, db *sql.DB, staged StagedPoster) (posterCommitState, error) {
	req := staged.Stage.Request
	if req.StepID == 0 {
		var queueID, mediaID, runID, generation int64
		var attempt int
		var owner, fp, state, stagedPath, hashes string
		err := db.QueryRowContext(ctx, `SELECT queue_id,media_id,run_id,generation,owner_token,attempt,source_fingerprint,state,staged_path,hashes_sizes_json FROM poster_repair_stage WHERE stage_id=?`, staged.Stage.StageID).Scan(&queueID, &mediaID, &runID, &generation, &owner, &attempt, &fp, &state, &stagedPath, &hashes)
		if errors.Is(err, sql.ErrNoRows) {
			return posterCommitAbsent, nil
		}
		if err != nil {
			return posterCommitUnknown, err
		}
		if queueID != req.QueueID || mediaID != req.MediaID || runID != req.RunID || generation != req.Generation || owner != req.OwnerToken || attempt != req.Attempt || fp != req.SourceFingerprint || (state != "staged" && state != "committed") || !pathsEqual(stagedPath, staged.Stage.StagedPath) || hashes != staged.Stage.HashesSizesJSON {
			return posterCommitUnknown, fmt.Errorf("poster repair journal identity mismatch")
		}
		var v struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		}
		if json.Unmarshal([]byte(hashes), &v) != nil || !pathsEqual(v.Path, staged.Path) || v.Size != staged.Size || v.SHA256 != staged.Hash {
			return posterCommitUnknown, fmt.Errorf("poster repair journal artifact mismatch")
		}
		return posterCommitExact, nil
	}
	var mediaID, runID, stepID, generation int64
	var owner, fp, kind, state, stagedPath, hashes string
	err := db.QueryRowContext(ctx, `SELECT media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json FROM media_asset_stage_journal WHERE stage_id=?`, staged.Stage.StageID).Scan(&mediaID, &runID, &stepID, &generation, &owner, &fp, &kind, &state, &stagedPath, &hashes)
	if errors.Is(err, sql.ErrNoRows) {
		return posterCommitAbsent, nil
	}
	if err != nil {
		return posterCommitUnknown, err
	}
	if mediaID != req.MediaID || runID != req.RunID || stepID != req.StepID || generation != req.Generation || owner != req.OwnerToken || fp != req.SourceFingerprint || kind != "poster" || state != "staged" || !pathsEqual(stagedPath, staged.Stage.StagedPath) || hashes != staged.Stage.HashesSizesJSON {
		return posterCommitUnknown, fmt.Errorf("poster journal identity mismatch")
	}
	return posterCommitExact, nil
}
func validateExactPosterStagePaths(roots PosterRecoveryRoots, r posterJournalRow, artifact string) error {
	if r.generation <= 0 || r.stageID == "" {
		return fmt.Errorf("invalid poster stage identity")
	}
	expectedDir := filepath.Join(roots.Upload, "posters", fmt.Sprintf("generation-%d", r.generation), r.stageID)
	if !sameResolvedPath(expectedDir, r.stagedPath) {
		return fmt.Errorf("poster staged path is not exact trusted layout")
	}
	expected := filepath.Join(expectedDir, posterLogicalName)
	if sameResolvedPath(expected, artifact) {
		return nil
	}
	if filepath.Ext(artifact) == ".enc" && pathInsideResolvedRoot(roots.Derived, artifact) {
		return nil
	}
	return fmt.Errorf("poster artifact path is not exact trusted layout")
}
func sameResolvedPath(a, b string) bool {
	aa, e1 := filepath.Abs(a)
	bb, e2 := filepath.Abs(b)
	if e1 != nil || e2 != nil {
		return false
	}
	return pathsEqual(aa, bb)
}
func pathInsideResolvedRoot(root, path string) bool {
	if root == "" {
		return false
	}
	r, e := filepath.Abs(root)
	if e != nil {
		return false
	}
	p, e := filepath.Abs(path)
	if e != nil {
		return false
	}
	rel, e := filepath.Rel(r, p)
	return e == nil && rel != ".." && len(rel) > 0 && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
