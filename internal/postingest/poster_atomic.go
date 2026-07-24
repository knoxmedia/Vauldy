package postingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"knox-media/internal/publication"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

var withImmediatePosterTx = store.WithImmediateConnTx
var withImmediatePosterPreflightTx = store.WithImmediateConnTx
var withImmediatePosterJournalTx = store.WithImmediateConnTx
var reconcilePosterJournal = reconcilePosterJournalAuthoritative
var posterHashPath = hashPath
var posterSourceFingerprint = sourceFingerprint
var posterBeforeSealHook = func() {}
var posterAfterSealHook = func() {}

const posterReconcileTimeout = 5 * time.Second

type StagedPoster struct {
	Stage                   publication.StageRecord
	Path, URL, Source, Hash string
	Size                    int64
	Derived                 *storage.StagedDerivedAsset
}

type posterCommitState uint8

const (
	posterCommitUnknown posterCommitState = iota
	posterCommitAbsent
	posterCommitExact
)

type stagedPosterRunner interface {
	StagePoster(context.Context, publication.StageRequest, int64, scraper.Config) (StagedPoster, error)
}

func (a *PosterAdapter) ExecuteWithResult(ctx context.Context, task Task) (ExecutionResult, error) {
	ordinary := ExecutionResult{Completion: CompleteThroughQueue}
	if task.RunID == nil || task.Generation <= 0 {
		return ordinary, a.Execute(ctx, task)
	}
	if a == nil || a.DB == nil {
		return ordinary, permanentPosterError("database is not configured")
	}
	if task.Type != TaskPoster && task.Type != TaskPosterRepair {
		return ordinary, permanentPosterError(fmt.Sprintf("unsupported task type %q", task.Type))
	}
	if task.MediaID <= 0 {
		return ordinary, permanentPosterError("invalid media id")
	}
	runner, ok := a.Runner.(stagedPosterRunner)
	if !ok || runner == nil {
		return ordinary, permanentPosterError("staging runner is not configured")
	}
	unlock, err := lockPosterMedia(ctx, task.MediaID)
	if err != nil {
		return ordinary, err
	}
	defer unlock()
	if task.Type == TaskPoster && task.StepID != nil {
		exact, checkErr := currentPosterEvidence(ctx, a.DB, task)
		if checkErr != nil {
			return ordinary, checkErr
		}
		if exact {
			return ExecutionResult{Completion: AlreadyCommittedAtomically}, nil
		}
	}
	if err = a.validateLease(ctx, task); err != nil {
		return ordinary, err
	}
	var libraryID int64
	var fileType, catalog string
	if err = a.DB.QueryRowContext(ctx, `SELECT library_id,COALESCE(file_type,''),COALESCE(file_path,'') FROM media WHERE id=?`, task.MediaID).Scan(&libraryID, &fileType, &catalog); err != nil {
		return ordinary, err
	}
	if !strings.EqualFold(strings.TrimSpace(fileType), "video") {
		return ordinary, permanentPosterError("poster requires video media")
	}
	input := storage.PreferredFFmpegPath(a.DB, task.MediaID, libraryID, catalog)
	fp, err := sourceFingerprint(input)
	if err != nil {
		return ordinary, err
	}
	stepID := int64(0)
	if task.StepID != nil {
		stepID = *task.StepID
	}
	req := publication.StageRequest{MediaID: task.MediaID, RunID: *task.RunID, StepID: stepID, Generation: task.Generation, OwnerToken: task.LeaseOwner, SourcePath: input, SourceFingerprint: fp, QueueID: task.ID, Attempt: task.Attempts}
	cfg, err := a.configForLibrary(ctx, libraryID)
	if err != nil {
		return ordinary, err
	}
	staged, err := runner.StagePoster(ctx, req, libraryID, cfg)
	if err != nil {
		return ordinary, err
	}
	roots := PosterRecoveryRoots{Upload: a.UploadDir}
	if a.Derived != nil {
		roots.Derived = a.Derived.BaseDir
	}
	if err = commitStagedPoster(ctx, a.DB, task, staged, roots); err != nil {
		var uncertain *store.ImmediateCommitError
		if errors.As(err, &uncertain) {
			return ExecutionResult{Completion: FinalizationOutcomeUncertain}, err
		}
		return ordinary, err
	}
	return ExecutionResult{Completion: AlreadyCommittedAtomically}, nil
}

func currentPosterEvidence(ctx context.Context, db *sql.DB, task Task) (bool, error) {
	if task.StepID == nil {
		return false, nil
	}
	var refs, fp, catalog string
	err := db.QueryRowContext(ctx, `SELECT e.artifact_refs_json,e.source_fingerprint,m.file_path FROM media_ingest_evidence e JOIN media m ON m.id=e.media_id JOIN post_ingest_task p ON p.ingest_step_id=e.step_id JOIN media_ingest_step s ON s.id=e.step_id JOIN media_ingest_run r ON r.id=e.run_id WHERE e.run_id=? AND e.step_id=? AND e.media_id=? AND e.generation=? AND e.kind='poster' AND p.id=? AND p.status='done' AND p.ingest_run_id=e.run_id AND p.generation=e.generation AND s.status='done' AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=e.generation`, *task.RunID, *task.StepID, task.MediaID, task.Generation, task.ID).Scan(&refs, &fp, &catalog)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current, err := sourceFingerprint(catalog)
	if err != nil || current != fp {
		return false, nil
	}
	var v struct {
		Path, URL, SHA256 string
		Size              int64
	}
	if json.Unmarshal([]byte(refs), &v) != nil {
		return false, nil
	}
	size, hash, err := hashPath(v.Path)
	if err != nil || size != v.Size || hash != v.SHA256 {
		return false, nil
	}
	var meta string
	if err = db.QueryRowContext(ctx, `SELECT meta_json FROM media WHERE id=?`, task.MediaID).Scan(&meta); err != nil {
		return false, err
	}
	return posterInMeta(decodePosterMeta(meta)) == v.URL, nil
}

type preverifiedPosterIdentity struct {
	sourcePath, sourceFingerprint string
	sourceSize                    int64
	sourceModTime                 time.Time
	artifactPath, artifactHash    string
	artifactSize                  int64
	artifactModTime               time.Time
}

func (v preverifiedPosterIdentity) verifyStats() error {
	if v.sourcePath != "" {
		s, err := os.Stat(v.sourcePath)
		if err != nil || s.Size() != v.sourceSize || !s.ModTime().Equal(v.sourceModTime) {
			return fmt.Errorf("poster commit: source stat changed")
		}
	}
	a, err := os.Stat(v.artifactPath)
	if err != nil || a.Size() != v.artifactSize || !a.ModTime().Equal(v.artifactModTime) {
		return fmt.Errorf("poster commit: staged stat changed")
	}
	return nil
}
func preverifyPosterArtifact(path string, expectedSize int64, expectedHash string) (preverifiedPosterIdentity, error) {
	size, hash, err := posterHashPath(path)
	if err != nil {
		return preverifiedPosterIdentity{}, err
	}
	if size != expectedSize || hash != expectedHash {
		return preverifiedPosterIdentity{}, fmt.Errorf("poster commit: staged hash/size mismatch")
	}
	st, err := os.Stat(path)
	if err != nil {
		return preverifiedPosterIdentity{}, err
	}
	return preverifiedPosterIdentity{artifactPath: path, artifactHash: hash, artifactSize: st.Size(), artifactModTime: st.ModTime()}, nil
}
func sealPlainPosterObject(ctx context.Context, uploadRoot string, staged StagedPoster) (StagedPoster, bool, error) {
	posterBeforeSealHook()
	if err := ctx.Err(); err != nil {
		return staged, false, err
	}
	final := storage.PosterObjectPath(uploadRoot, staged.Hash, ".jpg")
	if final == "" {
		return staged, false, fmt.Errorf("poster seal: invalid hash")
	}
	if st, err := os.Stat(final); err == nil {
		size, hash, e := posterHashPath(final)
		if e != nil || st.IsDir() || size != staged.Size || hash != staged.Hash {
			return staged, false, fmt.Errorf("poster seal: existing object mismatch")
		}
		staged.Path, staged.URL = final, storage.PosterObjectURL(staged.Hash)
		return staged, false, nil
	} else if !os.IsNotExist(err) {
		return staged, false, err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0755); err != nil {
		return staged, false, err
	}
	src, err := os.Open(staged.Path)
	if err != nil {
		return staged, false, err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(final), ".seal-*.tmp")
	if err != nil {
		return staged, false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), src)
	if copyErr == nil {
		copyErr = tmp.Sync()
	}
	closeErr := tmp.Close()
	if copyErr != nil {
		return staged, false, copyErr
	}
	if closeErr != nil {
		return staged, false, closeErr
	}
	hash := hex.EncodeToString(h.Sum(nil))
	if n != staged.Size || hash != staged.Hash {
		return staged, false, fmt.Errorf("poster seal: source hash/size mismatch")
	}
	created := false
	if err = os.Link(tmpName, final); err != nil {
		if st, e := os.Stat(final); e != nil || st.IsDir() {
			return staged, false, err
		}
		size, existingHash, e := posterHashPath(final)
		if e != nil || size != staged.Size || existingHash != staged.Hash {
			return staged, false, fmt.Errorf("poster seal: existing object mismatch")
		}
	} else {
		created = true
	}
	_ = os.Chmod(final, 0444)
	staged.Path, staged.URL = final, storage.PosterObjectURL(staged.Hash)
	posterAfterSealHook()
	return staged, created, nil
}

func commitStagedPoster(ctx context.Context, db *sql.DB, task Task, staged StagedPoster, roots PosterRecoveryRoots) error {
	req := staged.Stage.Request
	stepID := int64(0)
	if task.StepID != nil {
		stepID = *task.StepID
	}
	if err := validateStagedPosterIdentity(task, staged, roots); err != nil {
		return err
	}
	if _, err := withImmediatePosterPreflightTx(ctx, db, func(tx store.ImmediateConnTx) error { return validatePosterTaskTx(ctx, tx, task) }); err != nil {
		state, reconcileErr := reconcilePosterCommitState(ctx, db, task, staged)
		if reconcileErr == nil && state == posterCommitExact {
			return nil
		}
		return err
	}
	sealedNew := false
	if staged.Derived == nil && !exactPosterObjectPath(roots.Upload, staged.Path) {
		var sealErr error
		staged, sealedNew, sealErr = sealPlainPosterObject(ctx, roots.Upload, staged)
		if sealErr != nil {
			return sealErr
		}
	}
	var sourcePath string
	if err := db.QueryRowContext(ctx, `SELECT file_path FROM media WHERE id=?`, task.MediaID).Scan(&sourcePath); err != nil {
		return err
	}
	sourceFP, err := posterSourceFingerprint(sourcePath)
	if err != nil {
		return err
	}
	if sourceFP != req.SourceFingerprint {
		return fmt.Errorf("poster commit: stale source fingerprint")
	}
	sealedRefs, _ := json.Marshal(map[string]any{"path": staged.Path, "url": staged.URL, "source": staged.Source, "size": staged.Size, "sha256": staged.Hash, "generation": task.Generation, "stage_id": staged.Stage.StageID})
	staged.Stage.HashesSizesJSON = string(sealedRefs)
	verified, err := preverifyPosterArtifact(staged.Path, staged.Size, staged.Hash)
	if err != nil {
		return err
	}
	sourceStat, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	verified.sourcePath, verified.sourceFingerprint, verified.sourceSize, verified.sourceModTime = sourcePath, sourceFP, sourceStat.Size(), sourceStat.ModTime()
	var replaced []string
	_, err = withImmediatePosterTx(ctx, db, func(tx store.ImmediateConnTx) error {
		if task.Type == TaskPoster {
			var existing string
			e := tx.QueryRowContext(ctx, `SELECT stage_id FROM media_ingest_evidence WHERE step_id=? AND kind='poster'`, stepID).Scan(&existing)
			if e == nil {
				if existing == staged.Stage.StageID {
					return verifyCommittedPosterTx(ctx, tx, task, staged, verified)
				}
				return fmt.Errorf("poster commit conflict: step selects %s", existing)
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if err := validatePosterTaskTx(ctx, tx, task); err != nil {
			return err
		}
		var source string
		if err := tx.QueryRowContext(ctx, `SELECT file_path FROM media WHERE id=?`, task.MediaID).Scan(&source); err != nil {
			return err
		}
		if !sameResolvedPath(source, sourcePath) {
			return fmt.Errorf("poster commit: source path changed")
		}
		if e := verified.verifyStats(); e != nil {
			return e
		}
		fp := sourceFP
		var one int
		if task.Type == TaskPoster {
			err = tx.QueryRowContext(ctx, `SELECT 1 FROM media_asset_stage_journal WHERE stage_id=? AND media_id=? AND run_id=? AND step_id=? AND generation=? AND owner_token=? AND source_fingerprint=? AND artifact_kind='poster' AND state='staged'`, staged.Stage.StageID, task.MediaID, *task.RunID, stepID, task.Generation, task.LeaseOwner, fp).Scan(&one)
		} else {
			err = tx.QueryRowContext(ctx, `SELECT 1 FROM poster_repair_stage WHERE stage_id=? AND queue_id=? AND media_id=? AND run_id=? AND generation=? AND owner_token=? AND attempt=? AND source_fingerprint=? AND state='staged'`, staged.Stage.StageID, task.ID, task.MediaID, *task.RunID, task.Generation, task.LeaseOwner, task.Attempts, fp).Scan(&one)
		}
		if err != nil {
			return fmt.Errorf("poster commit: journal mismatch: %w", err)
		}
		if staged.Derived != nil {
			old, e := (&storage.DerivedAssetStore{}).CommitStagedTx(ctx, tx, staged.Derived)
			if e != nil {
				return e
			}
			replaced = append(replaced, old...)
		}
		oldMeta, e := persistPosterMetaTx(ctx, tx, task.MediaID, staged.URL, staged.Source, true)
		if e != nil {
			return e
		}
		replaced = append(replaced, oldMeta...)
		refs, _ := json.Marshal(map[string]any{"path": staged.Path, "url": staged.URL, "source": staged.Source, "size": staged.Size, "sha256": staged.Hash, "generation": task.Generation, "stage_id": staged.Stage.StageID})
		if task.Type == TaskPoster {
			if _, e = tx.ExecContext(ctx, `INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,'poster',?,?,'generated',CURRENT_TIMESTAMP,?)`, *task.RunID, stepID, task.MediaID, task.Generation, fp, string(refs), staged.Stage.StageID); e != nil {
				return e
			}
		}
		if task.Type == TaskPoster {
			_, e = tx.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='committed',hashes_sizes_json=?,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged'`, staged.Stage.HashesSizesJSON, staged.Stage.StageID)
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE poster_repair_stage SET state='committed',hashes_sizes_json=?,updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND queue_id=? AND attempt=? AND state='staged'`, staged.Stage.HashesSizesJSON, staged.Stage.StageID, task.ID, task.Attempts)
		}
		if e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='done',lease_owner=NULL,lease_until=NULL,last_error='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND attempts=?`, task.ID, task.LeaseOwner, task.Attempts)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("poster commit: queue fence lost")
		}
		if task.Type == TaskPoster {
			result, e = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='done',lease_owner=NULL,lease_until=NULL,last_error='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND attempts=?`, stepID, task.LeaseOwner, task.Attempts)
			if e != nil {
				return e
			}
			n, _ = result.RowsAffected()
			if n != 1 {
				return fmt.Errorf("poster commit: step fence lost")
			}
			return publication.AggregateTx(ctx, tx, *task.RunID)
		}
		return nil
	})
	if err != nil {
		if !sealedNew {
			staged.Path = ""
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), posterReconcileTimeout)
		defer cancel()
		var uncertain *store.ImmediateCommitError
		if errors.As(err, &uncertain) {
			state, reconcileErr := reconcilePosterCommitState(cleanupCtx, db, task, staged)
			if reconcileErr == nil && state == posterCommitExact {
				return nil
			}
			if reconcileErr == nil && state == posterCommitAbsent {
				_ = cleanupUnreferencedPoster(cleanupCtx, db, staged)
			}
			return err
		}
		_ = cleanupUnreferencedPoster(cleanupCtx, db, staged)
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), posterReconcileTimeout)
	defer cancel()
	_ = cleanupPosterPaths(cleanupCtx, db, replaced, staged.Path)
	return nil
}

func validateStagedPosterIdentity(task Task, staged StagedPoster, roots PosterRecoveryRoots) error {
	req := staged.Stage.Request
	stepID := int64(0)
	if task.StepID != nil {
		stepID = *task.StepID
	}
	stageID := strings.TrimSpace(staged.Stage.StageID)
	if invalidPosterStageID(stageID) || task.RunID == nil || task.ID <= 0 || task.MediaID <= 0 || task.Generation <= 0 || task.Attempts <= 0 || strings.TrimSpace(task.LeaseOwner) == "" || req.QueueID != task.ID || req.MediaID != task.MediaID || req.RunID != *task.RunID || req.StepID != stepID || req.Generation != task.Generation || req.OwnerToken != task.LeaseOwner || req.Attempt != task.Attempts || strings.TrimSpace(req.SourceFingerprint) == "" || staged.Stage.Kind != publication.ArtifactPoster || staged.Stage.State != "staged" {
		return fmt.Errorf("poster commit: stage/task identity mismatch")
	}
	if (task.Type == TaskPoster && (task.StepID == nil || stepID <= 0)) || (task.Type == TaskPosterRepair && task.StepID != nil) || (task.Type != TaskPoster && task.Type != TaskPosterRepair) {
		return fmt.Errorf("poster commit: invalid task class")
	}
	if strings.TrimSpace(staged.Stage.StagedPath) == "" || !filepath.IsAbs(staged.Stage.StagedPath) || strings.TrimSpace(roots.Upload) == "" || !filepath.IsAbs(roots.Upload) {
		return fmt.Errorf("poster commit: unsafe staged path")
	}
	expectedDir := filepath.Join(roots.Upload, "posters", fmt.Sprintf("generation-%d", task.Generation), stageID)
	if !sameResolvedPath(expectedDir, staged.Stage.StagedPath) {
		return fmt.Errorf("poster commit: staged path is not exact trusted layout")
	}
	if staged.Derived == nil {
		if !sameResolvedPath(filepath.Join(expectedDir, posterLogicalName), staged.Path) && !exactPosterObjectPath(roots.Upload, staged.Path) {
			return fmt.Errorf("poster commit: artifact is outside trusted upload root")
		}
	} else if strings.TrimSpace(roots.Derived) == "" || !filepath.IsAbs(roots.Derived) || !pathInsideResolvedRoot(roots.Derived, staged.Path) || !sameResolvedPath(staged.Derived.EncPath(), staged.Path) {
		return fmt.Errorf("poster commit: artifact is outside trusted derived root")
	}
	return nil
}
func invalidPosterStageID(id string) bool {
	return id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`)
}

func validatePosterTaskTx(ctx context.Context, tx store.SQLExecutor, task Task) error {
	var one int
	var err error
	if task.Type == TaskPoster && task.RunID != nil && task.StepID != nil {
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_run r ON r.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.media_id=? AND p.task_type='poster' AND p.status='running' AND p.lease_owner=? AND p.attempts=? AND p.ingest_run_id=? AND p.ingest_step_id=? AND p.generation=? AND s.status='running' AND s.lease_owner=p.lease_owner AND s.attempts=p.attempts AND s.run_id=p.ingest_run_id AND s.media_id=p.media_id AND s.generation=p.generation AND r.status='processing' AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=p.generation`, task.ID, task.MediaID, task.LeaseOwner, task.Attempts, *task.RunID, *task.StepID, task.Generation).Scan(&one)
	} else if task.Type == TaskPosterRepair && task.RunID != nil && task.StepID == nil {
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM post_ingest_task p JOIN media m ON m.id=p.media_id JOIN media_ingest_run r ON r.id=p.ingest_run_id WHERE p.id=? AND p.media_id=? AND p.task_type='poster_repair' AND p.status='running' AND p.lease_owner=? AND p.attempts=? AND p.ingest_run_id=? AND p.ingest_step_id IS NULL AND p.generation=? AND m.ingest_generation=p.generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL AND r.media_id=p.media_id AND r.generation=p.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL`, task.ID, task.MediaID, task.LeaseOwner, task.Attempts, *task.RunID, task.Generation).Scan(&one)
	} else {
		err = sql.ErrNoRows
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ClassifiedError{Kind: FailureShutdown, Err: fmt.Errorf("poster commit: stale exact identity")}
	}
	return err
}

func persistPosterMetaTx(ctx context.Context, tx store.SQLExecutor, mediaID int64, url, source string, replace bool) ([]string, error) {
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(meta_json,'') FROM media WHERE id=?`, mediaID).Scan(&current); err != nil {
		return nil, err
	}
	root := decodePosterMeta(current)
	scrape := mapValue(root, "scrape")
	extra := mapValue(scrape, "extra")
	var old []string
	for _, v := range []any{scrape["poster"], extra["poster"]} {
		if p := stringValue(v); p != "" && p != url {
			old = append(old, p)
		}
	}
	if replace || stringValue(scrape["poster"]) == "" {
		scrape["poster"] = url
	}
	if replace || stringValue(extra["poster"]) == "" {
		extra["poster"] = url
	}
	if strings.TrimSpace(source) != "" && (replace || stringValue(extra["local_poster_source"]) == "") {
		extra["local_poster_source"] = source
	}
	raw, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return old, store.UpdateMediaMetaAndPhotoTime(ctx, tx, mediaID, string(raw))
}

func verifyCommittedPosterTx(ctx context.Context, tx store.SQLExecutor, task Task, staged StagedPoster, verified preverifiedPosterIdentity) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence e JOIN post_ingest_task p ON p.ingest_step_id=e.step_id JOIN media_ingest_step s ON s.id=e.step_id JOIN media_asset_stage_journal j ON j.stage_id=e.stage_id WHERE e.stage_id=? AND e.run_id=? AND e.step_id=? AND e.media_id=? AND e.generation=? AND e.source_fingerprint=? AND p.id=? AND p.status='done' AND s.status='done' AND j.state='committed' AND j.owner_token=?`, staged.Stage.StageID, *task.RunID, *task.StepID, task.MediaID, task.Generation, staged.Stage.Request.SourceFingerprint, task.ID, task.LeaseOwner).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("poster commit: partial same-stage completion")
	}
	if verified.artifactPath != staged.Path || verified.artifactSize != staged.Size || verified.artifactHash != staged.Hash {
		return fmt.Errorf("poster commit: committed artifact mismatch")
	}
	if err := verified.verifyStats(); err != nil {
		return err
	}
	var meta string
	if err = tx.QueryRowContext(ctx, `SELECT meta_json FROM media WHERE id=?`, task.MediaID).Scan(&meta); err != nil {
		return err
	}
	if posterInMeta(decodePosterMeta(meta)) != staged.URL {
		return fmt.Errorf("poster commit: metadata pointer differs")
	}
	return nil
}
func reconcilePosterCommitState(ctx context.Context, db *sql.DB, task Task, staged StagedPoster) (posterCommitState, error) {
	if task.Type == TaskPosterRepair {
		var state, meta string
		err := db.QueryRowContext(ctx, `SELECT r.state,m.meta_json FROM poster_repair_stage r JOIN post_ingest_task p ON p.id=r.queue_id JOIN media m ON m.id=r.media_id JOIN media_ingest_run run ON run.id=r.run_id WHERE r.stage_id=? AND r.queue_id=? AND r.media_id=? AND r.run_id=? AND r.generation=? AND r.owner_token=? AND r.attempt=? AND r.source_fingerprint=? AND p.status='done' AND p.ingest_step_id IS NULL AND run.superseded_at IS NULL AND run.superseded_by_generation IS NULL AND m.ingest_generation=r.generation`, staged.Stage.StageID, task.ID, task.MediaID, *task.RunID, task.Generation, task.LeaseOwner, task.Attempts, staged.Stage.Request.SourceFingerprint).Scan(&state, &meta)
		if errors.Is(err, sql.ErrNoRows) {
			var n int
			if e := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM poster_repair_stage WHERE stage_id=? OR (queue_id=? AND attempt=?)`, staged.Stage.StageID, task.ID, task.Attempts).Scan(&n); e != nil {
				return posterCommitUnknown, e
			}
			if n == 0 {
				return posterCommitAbsent, nil
			}
			return posterCommitUnknown, err
		}
		if err != nil {
			return posterCommitUnknown, err
		}
		if state == "committed" && posterInMeta(decodePosterMeta(meta)) == staged.URL {
			size, hash, e := hashPath(staged.Path)
			if e == nil && size == staged.Size && hash == staged.Hash {
				return posterCommitExact, nil
			}
		}
		return posterCommitUnknown, fmt.Errorf("poster repair commit mismatch")
	}
	if task.Type != TaskPoster {
		return posterCommitUnknown, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return posterCommitUnknown, err
	}
	defer conn.Close()
	verified, preErr := preverifyPosterArtifact(staged.Path, staged.Size, staged.Hash)
	if preErr != nil {
		return posterCommitUnknown, preErr
	}
	if err = verifyCommittedPosterTx(ctx, conn, task, staged, verified); err == nil {
		return posterCommitExact, nil
	}
	var evidence, queue, step int
	if queryErr := conn.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM media_ingest_evidence WHERE stage_id=?),(SELECT COUNT(*) FROM post_ingest_task WHERE id=? AND status='done'),(SELECT COUNT(*) FROM media_ingest_step WHERE id=? AND status='done')`, staged.Stage.StageID, task.ID, *task.StepID).Scan(&evidence, &queue, &step); queryErr != nil {
		return posterCommitUnknown, queryErr
	}
	if evidence == 0 && queue == 0 && step == 0 {
		return posterCommitAbsent, nil
	}
	return posterCommitUnknown, err
}

func reconcilePosterCommit(ctx context.Context, db *sql.DB, task Task, staged StagedPoster) (bool, error) {
	if task.Type != TaskPoster {
		return false, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	verified, preErr := preverifyPosterArtifact(staged.Path, staged.Size, staged.Hash)
	if preErr != nil {
		return false, preErr
	}
	err = verifyCommittedPosterTx(ctx, conn, task, staged, verified)
	return err == nil, err
}

type posterJournalClass string

const (
	posterJournalOrdinary posterJournalClass = "ordinary"
	posterJournalRepair   posterJournalClass = "repair"
)

func posterPathReferenceCount(ctx context.Context, db *sql.DB, path, url, excludeStageID string, excludeClass posterJournalClass) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM media_derived_assets WHERE enc_path=?)+
 (SELECT COUNT(*) FROM media_ingest_evidence WHERE (json_extract(artifact_refs_json,'$.path')=? OR json_extract(artifact_refs_json,'$.url')=?) AND stage_id<>?)+
 (SELECT COUNT(*) FROM media_asset_stage_journal WHERE (json_extract(hashes_sizes_json,'$.path')=? OR json_extract(hashes_sizes_json,'$.url')=?) AND NOT (?='ordinary' AND stage_id=?))+
 (SELECT COUNT(*) FROM poster_repair_stage WHERE (json_extract(hashes_sizes_json,'$.path')=? OR json_extract(hashes_sizes_json,'$.url')=?) AND NOT (?='repair' AND stage_id=?))+
 (SELECT COUNT(*) FROM media WHERE json_extract(meta_json,'$.scrape.poster')=? OR json_extract(meta_json,'$.scrape.extra.poster')=?)`, path, path, url, excludeStageID, path, url, string(excludeClass), excludeStageID, path, url, string(excludeClass), excludeStageID, url, url).Scan(&n)
	return n, err
}
func managedPosterPath(url, exemplar string) string {
	const prefix = "/uploads/posters/objects/sha256/"
	if !strings.HasPrefix(url, prefix) {
		return ""
	}
	rel := strings.TrimPrefix(url, "/uploads/")
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 5 || parts[0] != "posters" || parts[1] != "objects" || parts[2] != "sha256" || len(parts[3]) != 2 || len(parts[4]) != 68 || !strings.HasSuffix(parts[4], ".jpg") {
		return ""
	}
	hash := strings.TrimSuffix(parts[4], ".jpg")
	if parts[3] != hash[:2] {
		return ""
	}
	for _, c := range hash {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return ""
		}
	}
	abs, e := filepath.Abs(exemplar)
	if e != nil {
		return ""
	}
	marker := string(filepath.Separator) + "posters" + string(filepath.Separator)
	i := strings.LastIndex(strings.ToLower(abs), strings.ToLower(marker))
	root := abs
	if i >= 0 {
		root = abs[:i]
	}
	candidate := filepath.Join(root, filepath.FromSlash(rel))
	if !pathInsideResolvedRoot(root, candidate) {
		return ""
	}
	parent := filepath.Dir(candidate)
	if st, e := os.Lstat(parent); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	return candidate
}
func cleanupPosterPaths(ctx context.Context, db *sql.DB, refs []string, exemplar string) error {
	for _, ref := range refs {
		p := ref
		if strings.HasPrefix(ref, "/uploads/") {
			p = managedPosterPath(ref, exemplar)
		}
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		uploadRoot := ""
		marker := string(filepath.Separator) + "posters" + string(filepath.Separator)
		if abs, e := filepath.Abs(p); e == nil {
			if i := strings.LastIndex(strings.ToLower(abs), strings.ToLower(marker)); i >= 0 {
				uploadRoot = abs[:i]
			}
		}
		if exactPosterObjectPath(uploadRoot, p) {
			q, renamed, e := quarantinePosterObjectIfUnreferenced(ctx, db, p, uploadRoot, nil)
			if e != nil {
				return e
			}
			if renamed {
				if e = deletePosterQuarantine(q); e != nil {
					return e
				}
				prunePosterObjectPrefix(uploadRoot, p)
			}
			continue
		}
		n, e := posterPathReferenceCount(ctx, db, p, "", "", "")
		if e != nil {
			return e
		}
		if n == 0 {
			if st, e := os.Lstat(p); e == nil && st.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if e = os.Remove(p); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
	}
	return nil
}
func cleanupUnreferencedPoster(ctx context.Context, db *sql.DB, s StagedPoster) error {
	generationPath := filepath.Join(s.Stage.StagedPath, posterLogicalName)
	_, _ = db.ExecContext(ctx, `DELETE FROM media_asset_stage_journal WHERE stage_id=? AND state='staged' AND media_id=? AND run_id=? AND generation=? AND owner_token=? AND source_fingerprint=?`, s.Stage.StageID, s.Stage.Request.MediaID, s.Stage.Request.RunID, s.Stage.Request.Generation, s.Stage.Request.OwnerToken, s.Stage.Request.SourceFingerprint)
	_, _ = db.ExecContext(ctx, `DELETE FROM poster_repair_stage WHERE stage_id=? AND state='staged' AND queue_id=? AND media_id=? AND run_id=? AND generation=? AND owner_token=? AND attempt=? AND source_fingerprint=?`, s.Stage.StageID, s.Stage.Request.QueueID, s.Stage.Request.MediaID, s.Stage.Request.RunID, s.Stage.Request.Generation, s.Stage.Request.OwnerToken, s.Stage.Request.Attempt, s.Stage.Request.SourceFingerprint)
	if err := cleanupPosterPaths(ctx, db, []string{s.Path, generationPath}, ""); err != nil {
		return err
	}
	_ = os.Remove(s.Stage.StagedPath)
	return nil
}

func (r *LocalPosterRunner) StagePoster(ctx context.Context, req publication.StageRequest, libraryID int64, cfg scraper.Config) (StagedPoster, error) {
	if r == nil || r.DB == nil || req.MediaID <= 0 || req.RunID <= 0 || req.Generation <= 0 || req.OwnerToken == "" || req.SourceFingerprint == "" {
		return StagedPoster{}, permanentPosterError("invalid poster stage identity")
	}
	var duration int64
	if err := r.DB.QueryRowContext(ctx, `SELECT COALESCE(duration,0) FROM media WHERE id=? AND library_id=?`, req.MediaID, libraryID).Scan(&duration); err != nil {
		return StagedPoster{}, err
	}
	stageID := uuid.NewString()
	dir := filepath.Join(strings.TrimSpace(r.UploadDir), "posters", fmt.Sprintf("generation-%d", req.Generation), stageID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return StagedPoster{}, err
	}
	plain := filepath.Join(dir, posterLogicalName)
	cleanup := true
	var derived *storage.StagedDerivedAsset
	preserve := false
	defer func() {
		if cleanup && !preserve {
			_ = os.Remove(plain)
			if r.Derived != nil {
				r.Derived.AbortStaged(derived)
			}
			_ = os.Remove(dir)
		}
	}()
	enabled := func(name string) bool {
		for _, v := range cfg.ImageSources {
			if strings.EqualFold(strings.TrimSpace(v), name) {
				return true
			}
		}
		return false
	}
	source := ""
	if enabled("embedded") && strings.TrimSpace(r.FFprobePath) != "" {
		if index, ok, e := r.attachedPicture(ctx, req.MediaID, req.SourcePath); e == nil && ok {
			_, e = r.ffmpeg(ctx, req.MediaID, req.SourcePath, nil, []string{"-map", fmt.Sprintf("0:%d", index), "-frames:v", "1", plain})
			if e == nil && nonEmptyFile(plain) {
				source = "embedded"
			}
		}
	}
	if source == "" && enabled("screen_grabber") {
		snap := posterSnapSecond(duration)
		if _, e := r.ffmpeg(ctx, req.MediaID, req.SourcePath, storage.PosterSeekPreInput(snap, req.SourcePath), []string{"-frames:v", "1", "-q:v", "3", plain}); e != nil {
			return StagedPoster{}, e
		}
		if nonEmptyFile(plain) {
			source = "screen_grabber"
		}
	}
	if source == "" {
		return StagedPoster{}, fmt.Errorf("local poster capture produced no file")
	}
	if err := r.validateStageGuard(ctx, req); err != nil {
		return StagedPoster{}, err
	}
	path, url := plain, storage.ImmutablePlainPosterURL(req.Generation, stageID)
	if r.Derived != nil && storage.NeedsDerivedEncryption(r.DB, req.MediaID) {
		var err error
		derived, err = r.Derived.StagePath(ctx, req.MediaID, posterKind, posterLogicalName, plain)
		if err != nil {
			return StagedPoster{}, err
		}
		_ = os.Remove(plain)
		path, url = derived.EncPath(), storage.DerivedPosterAPIPath(req.MediaID)
	}
	size, hash, err := hashPath(path)
	if err != nil {
		return StagedPoster{}, err
	}
	staged := StagedPoster{Stage: publication.StageRecord{StageID: stageID, Request: req, Kind: publication.ArtifactPoster, State: "staged", OriginalPath: req.SourcePath, StagedPath: dir}, Path: path, URL: url, Source: source, Hash: hash, Size: size, Derived: derived}
	hs, _ := json.Marshal(map[string]any{"path": path, "url": url, "source": source, "size": size, "sha256": hash, "derived": func() any {
		if derived != nil {
			return derived.RecoveryMetadata()
		}
		return nil
	}()})
	staged.Stage.HashesSizesJSON = string(hs)
	_, err = withImmediatePosterJournalTx(ctx, r.DB, func(tx store.ImmediateConnTx) error {
		task := Task{MediaID: req.MediaID, RunID: &req.RunID, Generation: req.Generation, LeaseOwner: req.OwnerToken, Attempts: 1, Type: TaskPoster}
		if req.StepID > 0 {
			task.StepID = &req.StepID
		}
		if e := tx.QueryRowContext(ctx, `SELECT id,attempts,task_type FROM post_ingest_task WHERE media_id=? AND ingest_run_id=? AND ((?=0 AND ingest_step_id IS NULL) OR ingest_step_id=?) AND generation=? AND status='running' AND lease_owner=?`, req.MediaID, req.RunID, req.StepID, req.StepID, req.Generation, req.OwnerToken).Scan(&task.ID, &task.Attempts, &task.Type); e != nil {
			return e
		}
		if e := validatePosterTaskTx(ctx, tx, task); e != nil {
			return e
		}
		var e error
		if req.StepID > 0 {
			_, e = tx.ExecContext(ctx, `INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged',?,?,?)`, stageID, req.MediaID, req.RunID, req.StepID, req.Generation, req.OwnerToken, req.SourceFingerprint, req.SourcePath, dir, string(hs))
		} else {
			_, e = tx.ExecContext(ctx, `INSERT INTO poster_repair_stage(stage_id,queue_id,media_id,run_id,generation,owner_token,attempt,source_fingerprint,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,?,'staged',?,?)`, stageID, task.ID, req.MediaID, req.RunID, req.Generation, req.OwnerToken, task.Attempts, req.SourceFingerprint, dir, staged.Stage.HashesSizesJSON)
		}
		return e
	})
	if err != nil {
		var uncertain *store.ImmediateCommitError
		if errors.As(err, &uncertain) {
			reconcileCtx, cancel := context.WithTimeout(context.Background(), posterReconcileTimeout)
			defer cancel()
			state, reconcileErr := reconcilePosterJournal(reconcileCtx, r.DB, staged)
			if reconcileErr != nil {
				preserve = true
			}
			if reconcileErr == nil && state == posterCommitExact {
				cleanup = false
				return staged, nil
			}
		}
		return StagedPoster{}, err
	}
	cleanup = false
	return staged, nil
}
func (r *LocalPosterRunner) validateStageGuard(ctx context.Context, req publication.StageRequest) error {
	var n int
	err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND ingest_run_id=? AND ((?=0 AND ingest_step_id IS NULL) OR ingest_step_id=?) AND generation=? AND status='running' AND lease_owner=?`, req.MediaID, req.RunID, req.StepID, req.StepID, req.Generation, req.OwnerToken).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ClassifiedError{Kind: FailureShutdown, Err: fmt.Errorf("poster stage: stale lease")}
	}
	return nil
}
