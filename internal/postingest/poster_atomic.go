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

	"github.com/google/uuid"
	"knox-media/internal/publication"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

var withImmediatePosterTx = store.WithImmediateConnTx
var withImmediatePosterJournalTx = store.WithImmediateConnTx

const posterReconcileTimeout = 5 * time.Second

type StagedPoster struct {
	Stage                   publication.StageRecord
	Path, URL, Source, Hash string
	Size                    int64
	Derived                 *storage.StagedDerivedAsset
}

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
	req := publication.StageRequest{MediaID: task.MediaID, RunID: *task.RunID, StepID: stepID, Generation: task.Generation, OwnerToken: task.LeaseOwner, SourcePath: input, SourceFingerprint: fp}
	cfg, err := a.configForLibrary(ctx, libraryID)
	if err != nil {
		return ordinary, err
	}
	staged, err := runner.StagePoster(ctx, req, libraryID, cfg)
	if err != nil {
		return ordinary, err
	}
	if err = commitStagedPoster(ctx, a.DB, task, staged); err != nil {
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

func commitStagedPoster(ctx context.Context, db *sql.DB, task Task, staged StagedPoster) error {
	req := staged.Stage.Request
	stepID := int64(0)
	if task.StepID != nil {
		stepID = *task.StepID
	}
	if task.RunID == nil || req.MediaID != task.MediaID || req.RunID != *task.RunID || req.StepID != stepID || req.Generation != task.Generation || req.OwnerToken != task.LeaseOwner {
		return fmt.Errorf("poster commit: stage/task identity mismatch")
	}
	var replaced []string
	_, err := withImmediatePosterTx(ctx, db, func(tx store.ImmediateConnTx) error {
		if task.Type == TaskPoster {
			var existing string
			e := tx.QueryRowContext(ctx, `SELECT stage_id FROM media_ingest_evidence WHERE step_id=? AND kind='poster'`, stepID).Scan(&existing)
			if e == nil {
				if existing == staged.Stage.StageID {
					return verifyCommittedPosterTx(ctx, tx, task, staged)
				}
				return fmt.Errorf("poster commit conflict: step selects %s", existing)
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if err := validatePosterIdentityTx(ctx, tx, task); err != nil {
			return err
		}
		var source string
		if err := tx.QueryRowContext(ctx, `SELECT file_path FROM media WHERE id=?`, task.MediaID).Scan(&source); err != nil {
			return err
		}
		fp, err := sourceFingerprint(source)
		if err != nil {
			return err
		}
		if fp != req.SourceFingerprint {
			return fmt.Errorf("poster commit: stale source fingerprint")
		}
		size, hash, err := hashPath(staged.Path)
		if err != nil {
			return err
		}
		if size != staged.Size || hash != staged.Hash {
			return fmt.Errorf("poster commit: staged hash/size mismatch")
		}
		if task.Type == TaskPoster {
			var one int
			if err = tx.QueryRowContext(ctx, `SELECT 1 FROM media_asset_stage_journal WHERE stage_id=? AND media_id=? AND run_id=? AND step_id=? AND generation=? AND owner_token=? AND source_fingerprint=? AND artifact_kind='poster' AND state='staged'`, staged.Stage.StageID, task.MediaID, *task.RunID, stepID, task.Generation, task.LeaseOwner, fp).Scan(&one); err != nil {
				return fmt.Errorf("poster commit: journal mismatch: %w", err)
			}
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
			if _, e = tx.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='committed',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged'`, staged.Stage.StageID); e != nil {
				return e
			}
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
		cleanupCtx, cancel := context.WithTimeout(context.Background(), posterReconcileTimeout)
		defer cancel()
		var uncertain *store.ImmediateCommitError
		if errors.As(err, &uncertain) {
			if ok, _ := reconcilePosterCommit(cleanupCtx, db, task, staged); ok {
				return nil
			}
			return err
		}
		_ = cleanupUnreferencedPoster(cleanupCtx, db, staged)
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), posterReconcileTimeout)
	defer cancel()
	_ = cleanupPosterPaths(cleanupCtx, db, replaced)
	return nil
}

func validatePosterIdentityTx(ctx context.Context, tx store.SQLExecutor, task Task) error {
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

func verifyCommittedPosterTx(ctx context.Context, tx store.SQLExecutor, task Task, staged StagedPoster) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence e JOIN post_ingest_task p ON p.ingest_step_id=e.step_id JOIN media_ingest_step s ON s.id=e.step_id JOIN media_asset_stage_journal j ON j.stage_id=e.stage_id WHERE e.stage_id=? AND e.run_id=? AND e.step_id=? AND e.media_id=? AND e.generation=? AND e.source_fingerprint=? AND p.id=? AND p.status='done' AND s.status='done' AND j.state='committed' AND j.owner_token=?`, staged.Stage.StageID, *task.RunID, *task.StepID, task.MediaID, task.Generation, staged.Stage.Request.SourceFingerprint, task.ID, task.LeaseOwner).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("poster commit: partial same-stage completion")
	}
	size, hash, err := hashPath(staged.Path)
	if err != nil || size != staged.Size || hash != staged.Hash {
		return fmt.Errorf("poster commit: committed artifact mismatch")
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
func reconcilePosterCommit(ctx context.Context, db *sql.DB, task Task, staged StagedPoster) (bool, error) {
	if task.Type != TaskPoster {
		return false, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	err = verifyCommittedPosterTx(ctx, conn, task, staged)
	return err == nil, err
}
func posterPathReferenceCount(ctx context.Context, db *sql.DB, path string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM media_derived_assets WHERE enc_path=?)+(SELECT COUNT(*) FROM media_ingest_evidence WHERE json_extract(artifact_refs_json,'$.path')=?)+(SELECT COUNT(*) FROM media WHERE json_extract(meta_json,'$.scrape.poster')=? OR json_extract(meta_json,'$.scrape.extra.poster')=?)`, path, path, path, path).Scan(&n)
	return n, err
}
func cleanupPosterPaths(ctx context.Context, db *sql.DB, paths []string) error {
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			continue
		}
		n, err := posterPathReferenceCount(ctx, db, p)
		if err != nil {
			return err
		}
		if n == 0 {
			if err = os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
func cleanupUnreferencedPoster(ctx context.Context, db *sql.DB, s StagedPoster) error {
	return cleanupPosterPaths(ctx, db, []string{s.Path})
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
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
			if r.Derived != nil {
				r.Derived.AbortStaged(derived)
			}
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
	path, url := plain, storage.PlainPosterURL(req.MediaID)
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
	if req.StepID == 0 {
		cleanup = false
		return staged, nil
	}
	_, err = withImmediatePosterJournalTx(ctx, r.DB, func(tx store.ImmediateConnTx) error {
		task := Task{MediaID: req.MediaID, RunID: &req.RunID, Generation: req.Generation, LeaseOwner: req.OwnerToken, Attempts: 1, Type: TaskPoster}
		if req.StepID > 0 {
			task.StepID = &req.StepID
		}
		if e := tx.QueryRowContext(ctx, `SELECT id,attempts,task_type FROM post_ingest_task WHERE media_id=? AND ingest_run_id=? AND ((?=0 AND ingest_step_id IS NULL) OR ingest_step_id=?) AND generation=? AND status='running' AND lease_owner=?`, req.MediaID, req.RunID, req.StepID, req.StepID, req.Generation, req.OwnerToken).Scan(&task.ID, &task.Attempts, &task.Type); e != nil {
			return e
		}
		if e := validatePosterIdentityTx(ctx, tx, task); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged',?,?,?)`, stageID, req.MediaID, req.RunID, req.StepID, req.Generation, req.OwnerToken, req.SourceFingerprint, req.SourcePath, dir, string(hs))
		return e
	})
	if err != nil {
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
