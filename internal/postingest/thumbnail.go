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

	"knox-media/internal/imagethumb"
	"knox-media/internal/keystore"
	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

var withImmediateThumbnailTx = store.WithImmediateConnTx
var reconcileThumbnailCommit = reconcileThumbnailCommitAuthoritative

type thumbnailCommitState uint8

const (
	thumbnailCommitUnknown thumbnailCommitState = iota
	thumbnailCommitAbsent
	thumbnailCommitExact
)

const thumbnailReconcileTimeout = 5 * time.Second

type thumbnailStageWorker interface {
	Stage(context.Context, Task) (imagethumb.StagedThumbnail, error)
}
type thumbnailAdapter struct {
	db     *sql.DB
	worker any
}

func NewThumbnailAdapter(db *sql.DB, worker any) Adapter {
	return &thumbnailAdapter{db: db, worker: worker}
}

func (a *thumbnailAdapter) Execute(ctx context.Context, task Task) error {
	_, err := a.ExecuteWithResult(ctx, task)
	return err
}
func (a *thumbnailAdapter) ExecuteWithResult(ctx context.Context, task Task) (ExecutionResult, error) {
	ordinary := ExecutionResult{Completion: CompleteThroughQueue}
	if a == nil || a.db == nil {
		return ordinary, permanentAdapterError(TaskThumbnail, "database is not configured")
	}
	if err := validateBasicAdapterTask(task, TaskThumbnail); err != nil {
		return ordinary, err
	}
	if task.RunID == nil || task.StepID == nil || task.Generation <= 0 {
		return ordinary, permanentAdapterError(TaskThumbnail, "linked publication identity is required")
	}
	worker, ok := a.worker.(thumbnailStageWorker)
	if !ok || worker == nil {
		return ordinary, permanentAdapterError(TaskThumbnail, "staging worker is not configured")
	}
	var fileType string
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(file_type,'') FROM media WHERE id=?`, task.MediaID).Scan(&fileType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ordinary, permanentAdapterError(TaskThumbnail, "media not found")
		}
		return ordinary, err
	}
	if !strings.EqualFold(strings.TrimSpace(fileType), "image") {
		return ordinary, permanentAdapterError(TaskThumbnail, "thumbnail requires image media")
	}
	if err := validateAdapterLease(ctx, a.db, task); err != nil {
		return ordinary, err
	}
	staged, err := worker.Stage(ctx, task)
	if err != nil {
		if errors.Is(err, imagethumb.ErrInvalidImage) {
			return ordinary, ClassifiedError{Kind: FailurePermanent, Err: err}
		}
		return ordinary, ClassifiedError{Kind: FailureRetryable, Err: err}
	}
	if err = commitStagedThumbnail(ctx, a.db, task, staged); err != nil {
		var uncertain *store.ImmediateCommitError
		if errors.As(err, &uncertain) {
			return ExecutionResult{Completion: FinalizationOutcomeUncertain}, err
		}
		return ordinary, err
	}
	return ExecutionResult{Completion: AlreadyCommittedAtomically}, nil
}

func commitStagedThumbnail(ctx context.Context, db *sql.DB, task Task, staged imagethumb.StagedThumbnail) error {
	if task.RunID == nil || task.StepID == nil {
		return permanentAdapterError(TaskThumbnail, "linked publication identity is required")
	}
	req := staged.Stage.Request
	if req.MediaID != task.MediaID || req.RunID != *task.RunID || req.StepID != *task.StepID || req.Generation != task.Generation || req.OwnerToken != task.LeaseOwner {
		return fmt.Errorf("thumbnail commit: stage/task identity mismatch")
	}
	var replaced []string
	_, err := withImmediateThumbnailTx(ctx, db, func(tx store.ImmediateConnTx) error {
		var existingStage string
		existingErr := tx.QueryRowContext(ctx, `SELECT stage_id FROM media_ingest_evidence WHERE step_id=? AND kind='thumbnail'`, *task.StepID).Scan(&existingStage)
		if existingErr == nil {
			if existingStage == staged.Stage.StageID {
				return verifyCommittedThumbnailTx(ctx, tx, task, staged)
			}
			return fmt.Errorf("thumbnail commit conflict: step evidence selects stage %s", existingStage)
		}
		if !errors.Is(existingErr, sql.ErrNoRows) {
			return existingErr
		}
		var source string
		var one int
		guard := `SELECT m.file_path FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_run r ON r.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.task_type='thumbnail' AND p.media_id=? AND p.generation=? AND p.ingest_run_id=? AND p.ingest_step_id=? AND p.status='running' AND p.lease_owner=? AND p.attempts=? AND s.status='running' AND s.run_id=p.ingest_run_id AND s.media_id=p.media_id AND s.generation=p.generation AND s.step_type='thumbnail' AND s.attempts=p.attempts AND s.lease_owner=p.lease_owner AND r.status='processing' AND r.superseded_by_generation IS NULL AND r.superseded_at IS NULL AND m.ingest_generation=p.generation`
		if err := tx.QueryRowContext(ctx, guard, task.ID, task.MediaID, task.Generation, *task.RunID, *task.StepID, task.LeaseOwner, task.Attempts).Scan(&source); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ClassifiedError{Kind: FailureShutdown, Err: fmt.Errorf("thumbnail commit: stale exact task/step/run identity")}
			}
			return err
		}
		fp, err := sourceFingerprint(source)
		if err != nil {
			return err
		}
		if fp != req.SourceFingerprint {
			return fmt.Errorf("thumbnail commit: stale source fingerprint")
		}
		if err = verifyVariant(staged.Thumb); err != nil {
			return err
		}
		if err = verifyVariant(staged.Medium); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT 1 FROM media_asset_stage_journal WHERE stage_id=? AND media_id=? AND run_id=? AND step_id=? AND generation=? AND owner_token=? AND source_fingerprint=? AND artifact_kind='thumbnail' AND state='staged'`, staged.Stage.StageID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp).Scan(&one); err != nil {
			return fmt.Errorf("thumbnail commit: stage journal mismatch: %w", err)
		}
		old, err := commitThumbnailPointersTx(ctx, tx, staged)
		if err != nil {
			return err
		}
		replaced = append(replaced, old...)
		refs, _ := json.Marshal(map[string]any{"producer": "imagethumb/stage-v1", "generation": task.Generation, "stage_id": staged.Stage.StageID, "source_fingerprint": fp, "variants": []any{variantRef(staged.Thumb), variantRef(staged.Medium)}, "validation": "verified"})
		if _, err = tx.ExecContext(ctx, `INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,'thumbnail',?,?,'generated',CURRENT_TIMESTAMP,?)`, *task.RunID, *task.StepID, task.MediaID, task.Generation, fp, string(refs), staged.Stage.StageID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='committed',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged'`, staged.Stage.StageID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='done',lease_owner=NULL,lease_until=NULL,last_error='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND attempts=?`, task.ID, task.LeaseOwner, task.Attempts)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("thumbnail commit: queue completion fence lost")
		}
		result, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='done',lease_owner=NULL,lease_until=NULL,last_error='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND attempts=?`, *task.StepID, task.LeaseOwner, task.Attempts)
		if err != nil {
			return err
		}
		n, _ = result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("thumbnail commit: step completion fence lost")
		}
		return publication.AggregateTx(ctx, tx, *task.RunID)
	})
	if err != nil {
		var uncertain *store.ImmediateCommitError
		if errors.As(err, &uncertain) {
			reconcileCtx, cancel := context.WithTimeout(context.Background(), thumbnailReconcileTimeout)
			defer cancel()
			state, reconcileErr := reconcileThumbnailCommit(reconcileCtx, db, task, staged)
			if reconcileErr != nil {
				return uncertain
			}
			if state == thumbnailCommitExact {
				return nil
			}
			if state == thumbnailCommitAbsent {
				if cleanupErr := cleanupUnreferencedThumbnailPaths(reconcileCtx, db, []string{staged.Thumb.Path, staged.Medium.Path}); cleanupErr != nil {
					return uncertain
				}
			}
			return uncertain
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), thumbnailReconcileTimeout)
		defer cancel()
		if cleanupErr := cleanupUnreferencedThumbnailPaths(cleanupCtx, db, []string{staged.Thumb.Path, staged.Medium.Path}); cleanupErr != nil {
			return errors.Join(err, cleanupErr)
		}
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), thumbnailReconcileTimeout)
	defer cancel()
	_ = cleanupUnreferencedThumbnailPaths(cleanupCtx, db, replaced)
	return nil
}

func cleanupUnreferencedThumbnailPaths(ctx context.Context, db *sql.DB, paths []string) error {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		var refs int
		if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM media_derived_assets WHERE enc_path=?)+(SELECT COUNT(*) FROM media_ingest_evidence e,json_each(e.artifact_refs_json,'$.variants') v WHERE json_extract(v.value,'$.path')=?)`, path, path).Scan(&refs); err != nil {
			return fmt.Errorf("thumbnail cleanup reference query: %w", err)
		}
		if refs == 0 {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func verifyCommittedThumbnailTx(ctx context.Context, tx store.SQLExecutor, task Task, staged imagethumb.StagedThumbnail) error {
	var sourceFingerprint, refs, journalOwner, journalFingerprint, journalKind, journalState, metaRaw string
	var evidenceMedia, evidenceRun, evidenceStep, evidenceGeneration int64
	err := tx.QueryRowContext(ctx, `SELECT e.media_id,e.run_id,e.step_id,e.generation,e.source_fingerprint,e.artifact_refs_json,j.owner_token,j.source_fingerprint,j.artifact_kind,j.state,m.meta_json FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_evidence e ON e.step_id=s.id AND e.kind='thumbnail' JOIN media_asset_stage_journal j ON j.stage_id=e.stage_id JOIN media m ON m.id=e.media_id WHERE p.id=? AND p.status='done' AND p.ingest_run_id=? AND p.ingest_step_id=? AND p.generation=? AND s.status='done' AND e.stage_id=?`, task.ID, *task.RunID, *task.StepID, task.Generation, staged.Stage.StageID).Scan(&evidenceMedia, &evidenceRun, &evidenceStep, &evidenceGeneration, &sourceFingerprint, &refs, &journalOwner, &journalFingerprint, &journalKind, &journalState, &metaRaw)
	if err != nil {
		return fmt.Errorf("thumbnail commit: partial same-stage completion: %w", err)
	}
	if evidenceMedia != task.MediaID || evidenceRun != *task.RunID || evidenceStep != *task.StepID || evidenceGeneration != task.Generation || sourceFingerprint != staged.Stage.Request.SourceFingerprint {
		return fmt.Errorf("thumbnail commit conflict: evidence identity differs")
	}
	if journalOwner != task.LeaseOwner || journalFingerprint != sourceFingerprint || journalKind != "thumbnail" || journalState != "committed" {
		return fmt.Errorf("thumbnail commit conflict: journal identity differs")
	}
	if err = verifyVariant(staged.Thumb); err != nil {
		return err
	}
	if err = verifyVariant(staged.Medium); err != nil {
		return err
	}
	var evidence struct {
		Variants []struct {
			Kind        string `json:"kind"`
			LogicalName string `json:"logical_name"`
			Path        string `json:"path"`
			SHA256      string `json:"sha256"`
			Size        int64  `json:"size"`
		} `json:"variants"`
	}
	if err = json.Unmarshal([]byte(refs), &evidence); err != nil {
		return err
	}
	for _, v := range []imagethumb.StagedVariant{staged.Thumb, staged.Medium} {
		matched := false
		for _, ref := range evidence.Variants {
			if ref.Kind == v.Kind && ref.LogicalName == v.LogicalName && filepath.Clean(ref.Path) == filepath.Clean(v.Path) && ref.SHA256 == v.Hash && ref.Size == v.Size {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("thumbnail commit conflict: evidence does not match staged variants")
		}
	}
	var root map[string]any
	if err = json.Unmarshal([]byte(metaRaw), &root); err != nil {
		return err
	}
	photo, _ := root["photo"].(map[string]any)
	thumb, _ := photo["thumb_path"].(string)
	medium, _ := photo["medium_path"].(string)
	if filepath.Clean(thumb) != filepath.Clean(staged.Thumb.Path) || filepath.Clean(medium) != filepath.Clean(staged.Medium.Path) {
		return fmt.Errorf("thumbnail commit conflict: metadata pointers differ")
	}
	for _, v := range []imagethumb.StagedVariant{staged.Thumb, staged.Medium} {
		if v.Derived != nil {
			var selected string
			if err = tx.QueryRowContext(ctx, `SELECT enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind=? AND logical_name=?`, task.MediaID, v.Kind, v.LogicalName).Scan(&selected); err != nil {
				return err
			}
			if filepath.Clean(selected) != filepath.Clean(v.Path) {
				return fmt.Errorf("thumbnail commit conflict: derived pointer differs")
			}
		}
	}
	return nil
}
func variantRef(v imagethumb.StagedVariant) map[string]any {
	return map[string]any{"kind": v.Kind, "logical_name": v.LogicalName, "path": v.Path, "size": v.Size, "sha256": v.Hash}
}
func verifyVariant(v imagethumb.StagedVariant) error {
	size, hash, err := hashPath(v.Path)
	if err != nil {
		return err
	}
	if size != v.Size || hash != v.Hash {
		return fmt.Errorf("thumbnail commit: staged variant hash/size mismatch")
	}
	return nil
}
func commitThumbnailPointersTx(ctx context.Context, tx store.ImmediateConnTx, staged imagethumb.StagedThumbnail) ([]string, error) {
	var old []string
	var err error
	if staged.Thumb.Derived != nil || staged.Medium.Derived != nil {
		old, err = (&storage.DerivedAssetStore{}).CommitStagedTx(ctx, tx, staged.Thumb.Derived, staged.Medium.Derived)
		if err != nil {
			return nil, err
		}
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(meta_json,'') FROM media WHERE id=?`, staged.Stage.Request.MediaID).Scan(&raw); err != nil {
		return nil, err
	}
	var root map[string]any
	_ = json.Unmarshal([]byte(raw), &root)
	if root == nil {
		root = map[string]any{}
	}
	photo, _ := root["photo"].(map[string]any)
	if photo == nil {
		photo = map[string]any{}
	}
	for _, key := range []string{"thumb_path", "medium_path"} {
		if prior, ok := photo[key].(string); ok && prior != "" && prior != staged.Thumb.Path && prior != staged.Medium.Path {
			old = append(old, prior)
		}
	}
	photo["thumb_path"], photo["medium_path"] = staged.Thumb.Path, staged.Medium.Path
	root["photo"] = photo
	merged, _ := json.Marshal(root)
	return old, store.UpdateMediaMetaAndPhotoTime(ctx, tx, staged.Stage.Request.MediaID, string(merged))
}

type LocalThumbnailWorker struct {
	DB                     *sql.DB
	Vault                  *keystore.Vault
	Derived                *storage.DerivedAssetStore
	FFmpegPath, PreviewDir string
}

func (w *LocalThumbnailWorker) Stage(ctx context.Context, task Task) (imagethumb.StagedThumbnail, error) {
	if w == nil || w.DB == nil {
		return imagethumb.StagedThumbnail{}, fmt.Errorf("thumbnail worker: database is not configured")
	}
	if task.RunID == nil || task.StepID == nil {
		return imagethumb.StagedThumbnail{}, fmt.Errorf("thumbnail worker: linked identity required")
	}
	var libraryID int64
	var source string
	if err := w.DB.QueryRowContext(ctx, `SELECT library_id,COALESCE(file_path,'') FROM media WHERE id=?`, task.MediaID).Scan(&libraryID, &source); err != nil {
		return imagethumb.StagedThumbnail{}, err
	}
	source = storage.PreferredFFmpegPath(w.DB, task.MediaID, libraryID, source)
	fp, err := sourceFingerprint(source)
	if err != nil {
		return imagethumb.StagedThumbnail{}, err
	}
	req := publication.StageRequest{MediaID: task.MediaID, RunID: *task.RunID, StepID: *task.StepID, Generation: task.Generation, OwnerToken: task.LeaseOwner, SourcePath: source, SourceFingerprint: fp}
	ctx = imagethumb.WithCommitGuard(ctx, func(c context.Context) error { return validateAdapterLease(c, w.DB, task) })
	staged, err := imagethumb.StageThumbnail(ctx, w.DB, w.Vault, w.Derived, w.FFmpegPath, filepath.Join(w.PreviewDir, "photos"), req)
	if err != nil {
		return staged, err
	}
	hs, _ := json.Marshal(map[string]any{"thumb": variantRef(staged.Thumb), "medium": variantRef(staged.Medium)})
	staged.Stage.HashesSizesJSON = string(hs)
	_, err = store.WithImmediateConnTx(ctx, w.DB, func(tx store.ImmediateConnTx) error {
		var one int
		if guardErr := tx.QueryRowContext(ctx, `SELECT 1 FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_run r ON r.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.media_id=? AND p.task_type='thumbnail' AND p.ingest_run_id=? AND p.ingest_step_id=? AND p.generation=? AND p.status='running' AND p.lease_owner=? AND p.attempts=? AND s.status='running' AND s.lease_owner=p.lease_owner AND s.attempts=p.attempts AND r.status='processing' AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=p.generation`, task.ID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, task.Attempts).Scan(&one); guardErr != nil {
			return guardErr
		}
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'thumbnail','staged',?,?,?)`, staged.Stage.StageID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, source, staged.Stage.StagedPath, string(hs))
		return insertErr
	})
	if err != nil {
		_ = os.RemoveAll(staged.Stage.StagedPath)
		return imagethumb.StagedThumbnail{}, err
	}
	return staged, nil
}
func sourceFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	_, hash, err := hashPath(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%d|%d|sha256:%s", filepath.Clean(canonical), info.Size(), info.ModTime().UnixNano(), hash), nil
}
func hashPath(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

func reconcileThumbnailCommitAuthoritative(ctx context.Context, db *sql.DB, task Task, staged imagethumb.StagedThumbnail) (thumbnailCommitState, error) {
	if err := ctx.Err(); err != nil {
		return thumbnailCommitUnknown, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return thumbnailCommitUnknown, err
	}
	defer conn.Close()
	err = verifyCommittedThumbnailTx(ctx, conn, task, staged)
	if err == nil {
		return thumbnailCommitExact, nil
	}
	var evidence, taskStatus, stepStatus int
	if queryErr := conn.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM media_ingest_evidence WHERE stage_id=?),(SELECT COUNT(*) FROM post_ingest_task WHERE id=? AND status='done'),(SELECT COUNT(*) FROM media_ingest_step WHERE id=? AND status='done')`, staged.Stage.StageID, task.ID, *task.StepID).Scan(&evidence, &taskStatus, &stepStatus); queryErr != nil {
		return thumbnailCommitUnknown, queryErr
	}
	if evidence == 0 && taskStatus == 0 && stepStatus == 0 {
		return thumbnailCommitAbsent, nil
	}
	return thumbnailCommitUnknown, err
}
