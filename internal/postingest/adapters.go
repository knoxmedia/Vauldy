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

	"knox-media/internal/atrack"
	"knox-media/internal/keyframe"
	"knox-media/internal/preview"
	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"knox-media/internal/store"
	"knox-media/internal/subtitle"
)

type Adapter interface {
	Execute(context.Context, Task) error
}

type AdapterSet struct {
	Poster    Adapter
	Thumbnail Adapter
	Preview   Adapter
	Keyframe  Adapter
	Subtitle  Adapter
	Atrack    Adapter
	Encrypt   Adapter
}

func (s AdapterSet) Execute(ctx context.Context, task Task) error {
	var adapter Adapter
	switch task.Type {
	case TaskPoster, TaskPosterRepair:
		adapter = s.Poster
	case TaskThumbnail:
		adapter = s.Thumbnail
	case TaskPreview:
		adapter = s.Preview
	case TaskKeyframe:
		adapter = s.Keyframe
	case TaskSubtitle:
		adapter = s.Subtitle
	case TaskAtrack:
		adapter = s.Atrack
	case TaskEncrypt:
		adapter = s.Encrypt
	default:
		return ClassifiedError{Kind: FailurePermanent, Err: fmt.Errorf("post-ingest adapter: unsupported task type %q", task.Type)}
	}
	if adapter == nil {
		return ClassifiedError{Kind: FailurePermanent, Err: fmt.Errorf("post-ingest adapter: adapter for task type %q is not configured", task.Type)}
	}
	return adapter.Execute(ctx, task)
}

func (s AdapterSet) ExecuteWithResult(ctx context.Context, task Task) (ExecutionResult, error) {
	var adapter Adapter
	switch task.Type {
	case TaskPoster, TaskPosterRepair:
		adapter = s.Poster
	case TaskThumbnail:
		adapter = s.Thumbnail
	case TaskPreview:
		adapter = s.Preview
	case TaskKeyframe:
		adapter = s.Keyframe
	case TaskSubtitle:
		adapter = s.Subtitle
	case TaskAtrack:
		adapter = s.Atrack
	case TaskEncrypt:
		adapter = s.Encrypt
	default:
		return ExecutionResult{Completion: CompleteThroughQueue}, ClassifiedError{Kind: FailurePermanent, Err: fmt.Errorf("post-ingest adapter: unsupported task type %q", task.Type)}
	}
	if adapter == nil {
		return ExecutionResult{Completion: CompleteThroughQueue}, ClassifiedError{Kind: FailurePermanent, Err: fmt.Errorf("post-ingest adapter: adapter for task type %q is not configured", task.Type)}
	}
	if atomic, ok := adapter.(resultExecutor); ok {
		return atomic.ExecuteWithResult(ctx, task)
	}
	return ExecutionResult{Completion: CompleteThroughQueue}, adapter.Execute(ctx, task)
}

type runOneWorker interface {
	RunOne(context.Context, int64) error
}

type domainAdapter struct {
	db     *sql.DB
	typ    TaskType
	worker runOneWorker
}

func NewPreviewAdapter(db *sql.DB, worker interface {
	RunOne(context.Context, int64) error
}) Adapter {
	return &domainAdapter{db: db, typ: TaskPreview, worker: worker}
}

func NewKeyframeAdapter(db *sql.DB, worker interface {
	RunOne(context.Context, int64) error
}) Adapter {
	return &domainAdapter{db: db, typ: TaskKeyframe, worker: worker}
}

func (a *domainAdapter) Execute(ctx context.Context, task Task) error {
	if a == nil || a.db == nil {
		return permanentAdapterError(a.typeName(), "database is not configured")
	}
	if task.Type != a.typ {
		return permanentAdapterError(a.typ, fmt.Sprintf("unsupported task type %q", task.Type))
	}
	if task.ID <= 0 {
		return permanentAdapterError(a.typ, "invalid task id")
	}
	if task.MediaID <= 0 {
		return permanentAdapterError(a.typ, "invalid media id")
	}
	if a.worker == nil {
		return permanentAdapterError(a.typ, "worker is not configured")
	}
	var duration int64
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(duration,0) FROM media WHERE id=?`, task.MediaID).Scan(&duration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return permanentAdapterError(a.typ, "media not found")
		}
		return err
	}
	if err := validateAdapterLease(ctx, a.db, task); err != nil {
		return err
	}
	switch a.typ {
	case TaskPreview:
		if err := preview.EnsureWaitingTask(ctx, a.db, task.MediaID, duration); err != nil {
			return err
		}
	case TaskKeyframe:
		if _, err := a.db.ExecContext(ctx, `INSERT INTO keyframe_task(media_id,status,updated_at) VALUES(?,'waiting',CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO NOTHING`, task.MediaID); err != nil {
			return err
		}
	}
	guard := func(guardCtx context.Context) error { return validateAdapterLease(guardCtx, a.db, task) }
	txGuard := func(guardCtx context.Context, tx *sql.Tx) error { return validateAdapterLeaseTx(guardCtx, tx, task) }
	switch a.typ {
	case TaskPreview:
		ctx = preview.WithCommitGuard(ctx, guard)
		ctx = preview.WithCommitGuardTx(ctx, txGuard)
	case TaskKeyframe:
		ctx = keyframe.WithCommitGuard(ctx, guard)
		ctx = keyframe.WithCommitGuardTx(ctx, txGuard)
	}
	if err := a.worker.RunOne(ctx, task.MediaID); err != nil {
		return err
	}
	return validateAdapterLease(ctx, a.db, task)
}

func (a *domainAdapter) typeName() TaskType {
	if a == nil {
		return "domain"
	}
	return a.typ
}
func permanentAdapterError(typ TaskType, message string) error {
	return ClassifiedError{Kind: FailurePermanent, Err: fmt.Errorf("%s adapter: %s", typ, message)}
}
func validateAdapterLease(ctx context.Context, db *sql.DB, task Task) error {
	if strings.TrimSpace(task.LeaseOwner) == "" {
		return nil
	}
	var one int
	query := `SELECT 1 FROM post_ingest_task p JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.media_id=? AND p.task_type=? AND p.status='running' AND p.lease_owner=? AND p.generation=m.ingest_generation AND (?<=0 OR p.generation=?) AND (p.ingest_run_id IS NULL OR EXISTS (SELECT 1 FROM media_ingest_run r WHERE r.id=p.ingest_run_id AND r.media_id=p.media_id AND r.generation=p.generation AND r.superseded_by_generation IS NULL AND r.superseded_at IS NULL))`
	args := []any{task.ID, task.MediaID, task.Type, task.LeaseOwner, task.Generation, task.Generation}
	if task.Attempts > 0 {
		query += ` AND attempts=?`
		args = append(args, task.Attempts)
	}
	err := db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ClassifiedError{Kind: FailureShutdown, Err: fmt.Errorf("%s adapter: stale lease for task %d", task.Type, task.ID)}
	}
	return err
}

func validateAdapterLeaseTx(ctx context.Context, tx *sql.Tx, task Task) error {
	if strings.TrimSpace(task.LeaseOwner) == "" {
		return nil
	}
	query := `SELECT 1 FROM post_ingest_task p JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.media_id=? AND p.task_type=? AND p.status='running' AND p.lease_owner=? AND p.generation=m.ingest_generation AND (?<=0 OR p.generation=?) AND (p.ingest_run_id IS NULL OR EXISTS (SELECT 1 FROM media_ingest_run r WHERE r.id=p.ingest_run_id AND r.media_id=p.media_id AND r.generation=p.generation AND r.superseded_by_generation IS NULL AND r.superseded_at IS NULL))`
	args := []any{task.ID, task.MediaID, task.Type, task.LeaseOwner, task.Generation, task.Generation}
	if task.Attempts > 0 {
		query += ` AND attempts=?`
		args = append(args, task.Attempts)
	}
	var one int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ClassifiedError{Kind: FailureShutdown, Err: fmt.Errorf("%s adapter: stale lease for task %d", task.Type, task.ID)}
	}
	return err
}

type subtitleService interface {
	EnsurePendingSubtitleTask(int64) error
	ProcessMedia(context.Context, int64) error
}

type subtitleAdapter struct {
	db  *sql.DB
	svc subtitleService
}

func NewSubtitleAdapter(db *sql.DB, svc interface {
	EnsurePendingSubtitleTask(int64) error
	ProcessMedia(context.Context, int64) error
}) Adapter {
	return &subtitleAdapter{db: db, svc: svc}
}

func (a *subtitleAdapter) Execute(ctx context.Context, task Task) error {
	if a == nil || a.db == nil {
		return permanentAdapterError(TaskSubtitle, "database is not configured")
	}
	if err := validateBasicAdapterTask(task, TaskSubtitle); err != nil {
		return err
	}
	if a.svc == nil {
		return permanentAdapterError(TaskSubtitle, "service is not configured")
	}
	var fileType string
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(file_type,'') FROM media WHERE id=?`, task.MediaID).Scan(&fileType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return permanentAdapterError(TaskSubtitle, "media not found")
		}
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(fileType), "video") {
		return permanentAdapterError(TaskSubtitle, "subtitle requires video media")
	}
	if err := validateAdapterLease(ctx, a.db, task); err != nil {
		return err
	}
	ready, err := usableSubtitleOutput(ctx, a.db, task.MediaID)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}
	if err := a.svc.EnsurePendingSubtitleTask(task.MediaID); err != nil {
		return err
	}
	guard := func(guardCtx context.Context) error { return validateAdapterLease(guardCtx, a.db, task) }
	ctx = subtitle.WithCommitGuard(ctx, guard)
	txGuard := func(guardCtx context.Context, tx *sql.Tx) error { return validateAdapterLeaseTx(guardCtx, tx, task) }
	ctx = subtitle.WithCommitGuardTx(ctx, txGuard)
	if err := a.svc.ProcessMedia(ctx, task.MediaID); err != nil {
		return err
	}
	return validateAdapterLease(ctx, a.db, task)
}

type atrackRunner interface {
	Run(context.Context, int64, string) error
}
type atrackAdapter struct {
	db     *sql.DB
	worker atrackRunner
}

func NewAtrackAdapter(db *sql.DB, worker interface {
	Run(context.Context, int64, string) error
}) Adapter {
	return &atrackAdapter{db: db, worker: worker}
}

func (a *atrackAdapter) Execute(ctx context.Context, task Task) error {
	if a == nil || a.db == nil {
		return permanentAdapterError(TaskAtrack, "database is not configured")
	}
	if err := validateBasicAdapterTask(task, TaskAtrack); err != nil {
		return err
	}
	if a.worker == nil {
		return permanentAdapterError(TaskAtrack, "worker is not configured")
	}
	var libraryID int64
	var catalog, fileType string
	if err := a.db.QueryRowContext(ctx, `SELECT library_id,COALESCE(file_path,''),COALESCE(file_type,'') FROM media WHERE id=?`, task.MediaID).Scan(&libraryID, &catalog, &fileType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return permanentAdapterError(TaskAtrack, "media not found")
		}
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(fileType), "video") {
		return permanentAdapterError(TaskAtrack, "atrack requires video media")
	}
	if err := validateAdapterLease(ctx, a.db, task); err != nil {
		return err
	}
	ready, err := usableAtrackOutput(ctx, a.db, task.MediaID)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}
	if _, err = a.db.ExecContext(ctx, `INSERT INTO atrack_task(media_id,status,updated_at) VALUES(?,'waiting',CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO NOTHING`, task.MediaID); err != nil {
		return err
	}
	var domainStatus string
	var domainError sql.NullString
	if err = a.db.QueryRowContext(ctx, `SELECT status,error_message FROM atrack_task WHERE media_id=?`, task.MediaID).Scan(&domainStatus, &domainError); err != nil {
		return err
	}
	if domainStatus == "failed" {
		return fmt.Errorf("atrack task failed: %s", domainError.String)
	}
	input := storage.PreferredFFmpegPath(a.db, task.MediaID, libraryID, catalog)
	if strings.TrimSpace(input) == "" {
		return permanentAdapterError(TaskAtrack, "source file is unavailable")
	}
	guard := func(guardCtx context.Context) error { return validateAdapterLease(guardCtx, a.db, task) }
	ctx = atrack.WithCommitGuard(ctx, guard)
	txGuard := func(guardCtx context.Context, tx *sql.Tx) error { return validateAdapterLeaseTx(guardCtx, tx, task) }
	ctx = atrack.WithCommitGuardTx(ctx, txGuard)
	if err = a.worker.Run(ctx, task.MediaID, input); err != nil {
		return err
	}
	return validateAdapterLease(ctx, a.db, task)
}

func validateBasicAdapterTask(task Task, typ TaskType) error {
	if task.Type != typ {
		return permanentAdapterError(typ, fmt.Sprintf("unsupported task type %q", task.Type))
	}
	if task.ID <= 0 {
		return permanentAdapterError(typ, "invalid task id")
	}
	if task.MediaID <= 0 {
		return permanentAdapterError(typ, "invalid media id")
	}
	return nil
}

func usableSubtitleOutput(ctx context.Context, db *sql.DB, mediaID int64) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT COALESCE(vtt_path,'') FROM media_subtitle WHERE media_id=? AND status='ready'`, mediaID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return false, err
		}
		if nonEmptyFile(p) {
			return true, nil
		}
	}
	return false, rows.Err()
}
func usableAtrackOutput(ctx context.Context, db *sql.DB, mediaID int64) (bool, error) {
	var dir string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(output_dir,'') FROM atrack_task WHERE media_id=? AND status='done'`, mediaID).Scan(&dir)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	valid, err := validPlainAtrackOutput(strings.TrimSpace(dir))
	if err != nil {
		return false, err
	}
	if valid {
		return true, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT artifact_kind,enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind IN ('atrack_playlist','atrack_segment')`, mediaID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var playlist, segment bool
	for rows.Next() {
		var kind, p string
		if err := rows.Scan(&kind, &p); err != nil {
			return false, err
		}
		info, statErr := os.Stat(strings.TrimSpace(p))
		if statErr != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		if kind == "atrack_playlist" {
			playlist = true
		} else {
			segment = true
		}
	}
	return playlist && segment, rows.Err()
}

func validPlainAtrackOutput(root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) || strings.TrimSpace(root) == "" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, stream := range entries {
		if !stream.IsDir() {
			continue
		}
		dir := filepath.Join(root, stream.Name())
		manifest := filepath.Join(dir, "index.m3u8")
		data, err := os.ReadFile(manifest)
		if err != nil || len(data) == 0 {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(line)))
			if err == nil && !info.IsDir() && info.Size() > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

// NewEncryptAdapter runs staged media encryption under dispatcher ownership.
type mediaEncryptionStager interface {
	StageMediaEncryption(context.Context, int64) (storage.StagedMediaEncryption, error)
}

func NewEncryptAdapterWithSeams(enc interface {
	EncryptMedia(context.Context, int64) error
}, seams EncryptionStateMachineSeams) Adapter {
	return &encryptAdapter{enc: enc, seams: seams}
}

func NewEncryptAdapter(enc interface {
	EncryptMedia(context.Context, int64) error
}) Adapter {
	return NewEncryptAdapterWithSeams(enc, EncryptionStateMachineSeams{})
}

type encryptAdapter struct {
	seams EncryptionStateMachineSeams
	enc   interface {
		EncryptMedia(context.Context, int64) error
	}
}
type encryptionDBProvider interface{ EncryptionDB() *sql.DB }

type encryptionPrivateRootProvider interface{ EncryptionPrivateRoot() string }

func (a *encryptAdapter) Execute(ctx context.Context, task Task) error {
	_, err := a.ExecuteWithResult(ctx, task)
	return err
}
func (a *encryptAdapter) ExecuteWithResult(ctx context.Context, task Task) (ExecutionResult, error) {
	ordinary := ExecutionResult{Completion: CompleteThroughQueue}
	if err := validateBasicAdapterTask(task, TaskEncrypt); err != nil {
		return ordinary, err
	}
	if a == nil || a.enc == nil {
		return ordinary, permanentAdapterError(TaskEncrypt, "encryptor is not configured")
	}
	dbp, hasDB := a.enc.(encryptionDBProvider)
	stager, canStage := a.enc.(mediaEncryptionStager)
	if !hasDB || dbp.EncryptionDB() == nil {
		err := a.enc.EncryptMedia(ctx, task.MediaID)
		if errors.Is(err, storage.ErrAlreadyEncrypted) {
			return ordinary, nil
		}
		return ordinary, classifyEncryptError(err)
	}
	db := dbp.EncryptionDB()
	if task.RunID == nil || task.StepID == nil || task.Generation <= 0 {
		var one int
		if err := db.QueryRowContext(ctx, `SELECT 1 FROM media WHERE id=?`, task.MediaID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ordinary, permanentAdapterError(TaskEncrypt, "media not found")
			}
			return ordinary, err
		}
		if err := validateAdapterLease(ctx, db, task); err != nil {
			return ordinary, err
		}
		ready, err := usableEncryptedOutput(ctx, db, task.MediaID)
		if err != nil {
			return ordinary, err
		}
		if ready {
			return ordinary, nil
		}
		err = a.enc.EncryptMedia(ctx, task.MediaID)
		if errors.Is(err, storage.ErrAlreadyEncrypted) {
			return ordinary, nil
		}
		return ordinary, classifyEncryptError(err)
	}
	if err := validateAdapterLease(ctx, db, task); err != nil {
		return ordinary, err
	}
	staged, err := loadSelectedEncryptionStage(ctx, db, task)
	if errors.Is(err, sql.ErrNoRows) {
		if !canStage {
			return ordinary, permanentAdapterError(TaskEncrypt, "staged encryption is not configured")
		}
		staged, err = stager.StageMediaEncryption(ctx, task.MediaID)
		if err == nil {
			err = insertEncryptionStageJournal(ctx, db, task, staged)
		}
	}
	if err != nil {
		return ordinary, classifyEncryptError(err)
	}
	rootProvider, ok := a.enc.(encryptionPrivateRootProvider)
	quarantineRoot := ""
	if ok {
		quarantineRoot = rootProvider.EncryptionPrivateRoot()
	}
	if strings.TrimSpace(quarantineRoot) == "" {
		quarantineRoot = filepath.Join(filepath.Dir(staged.OriginalPath), ".quarantine", "encryption")
	}

	if err = commitEncryptionStage(ctx, db, task, staged, quarantineRoot, a.seams); err != nil {
		var uncertain *store.ImmediateCommitError
		if errors.As(err, &uncertain) {
			return ExecutionResult{Completion: FinalizationOutcomeUncertain}, err
		}
		return ordinary, err
	}
	return ExecutionResult{Completion: AlreadyCommittedAtomically}, nil
}
func classifyEncryptError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ClassifiedError{Kind: FailureCancelled, Err: err}
	}
	if errors.Is(err, sql.ErrNoRows) || isPermanentEncryptError(err) {
		return ClassifiedError{Kind: FailurePermanent, Err: err}
	}
	return ClassifiedError{Kind: FailureRetryable, Err: err}
}
func insertEncryptionStageJournal(ctx context.Context, db *sql.DB, task Task, s storage.StagedMediaEncryption) error {
	_, err := db.ExecContext(ctx, `INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,cleanup_plaintext,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'staged')`, s.StageID, task.ID, task.Attempts, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, s.OriginalPath, s.SourceFingerprint, s.EncPath, s.WrappedDEK, s.IV, s.SHA256, s.Size, boolInt(s.CleanupPlaintext))
	return err
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func loadSelectedEncryptionStage(ctx context.Context, db *sql.DB, task Task) (storage.StagedMediaEncryption, error) {
	var s storage.StagedMediaEncryption
	var cleanup int
	err := db.QueryRowContext(ctx, `SELECT 'selected-'||?||'-'||?,m.id,a.plain_path,'',a.enc_path,a.wrapped_dek,a.iv,COALESCE(l.encrypted_assets_cleanup_plaintext,0) FROM media m JOIN library l ON l.id=m.library_id JOIN media_encrypted_assets a ON a.media_id=m.id AND a.status='encrypted' AND a.enc_path=m.file_path WHERE m.id=?`, task.ID, task.Attempts, task.MediaID).Scan(&s.StageID, &s.MediaID, &s.OriginalPath, &s.SourceFingerprint, &s.EncPath, &s.WrappedDEK, &s.IV, &cleanup)
	if err != nil {
		return s, err
	}
	s.SourceFingerprint, err = storage.EncryptionSourceFingerprint(s.OriginalPath)
	if err != nil {
		return s, err
	}
	s.Size, s.SHA256, err = storage.EncryptionPathHash(s.EncPath)
	s.CleanupPlaintext = cleanup == 1
	return s, err
}
func commitEncryptionStage(ctx context.Context, db *sql.DB, task Task, s storage.StagedMediaEncryption, quarantineRoot string, seams EncryptionStateMachineSeams) error {
	alreadySelected, preflightErr := selectedEncryptionStage(ctx, db, task, s)
	if preflightErr != nil {
		return preflightErr
	}
	quarantinePath := ""
	if !alreadySelected {
		quarantinePath, preflightErr = reserveEncryptionQuarantine(ctx, db, task, s, quarantineRoot, seams)
		if preflightErr != nil {
			return preflightErr
		}
		if preflightErr = moveReservedEncryptionQuarantine(ctx, db, task, s, quarantineRoot, quarantinePath, seams); preflightErr != nil {
			return preflightErr
		}
	}
	if seams.BeforeFinalCommit != nil {
		if hookErr := seams.BeforeFinalCommit(); hookErr != nil {
			return hookErr
		}
	}

	_, err := seams.immediate(ctx, db, func(tx store.ImmediateConnTx) error {
		var selected string
		guard := `SELECT m.file_path FROM post_ingest_task p JOIN media_ingest_step step ON step.id=p.ingest_step_id JOIN media_ingest_run r ON r.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.task_type='encrypt' AND p.media_id=? AND p.generation=? AND p.ingest_run_id=? AND p.ingest_step_id=? AND p.status='running' AND p.lease_owner=? AND p.attempts=? AND step.status='running' AND step.lease_owner=p.lease_owner AND step.attempts=p.attempts AND r.status='processing' AND r.superseded_at IS NULL AND COALESCE(r.superseded_by_generation,0)=0 AND m.ingest_generation=p.generation AND NOT EXISTS(SELECT 1 FROM media_ingest_step_dependency d JOIN media_ingest_step dep ON dep.id=d.depends_on_step_id WHERE d.step_id=step.id AND d.dependency_kind='step_done' AND dep.status NOT IN ('done','skipped'))`
		if err := tx.QueryRowContext(ctx, guard, task.ID, task.MediaID, task.Generation, *task.RunID, *task.StepID, task.LeaseOwner, task.Attempts).Scan(&selected); err != nil {
			return ClassifiedError{Kind: FailureShutdown, Err: fmt.Errorf("encrypt commit stale fence: %w", err)}
		}
		if !alreadySelected {
			if err := verifyDurableQuarantine(ctx, tx, task, s, quarantinePath); err != nil {
				return err
			}
		}
		size, hash, err := hashPath(s.EncPath)
		if err != nil || size != s.Size || !strings.EqualFold(hash, s.SHA256) || s.WrappedDEK == "" || s.IV == "" {
			return errors.New("encrypt commit staged identity invalid")
		}
		if !alreadySelected {
			if _, err = tx.ExecContext(ctx, `INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status,updated_at) VALUES(?,?,?,?,?,'encrypted',CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO UPDATE SET enc_path=excluded.enc_path,wrapped_dek=excluded.wrapped_dek,iv=excluded.iv,plain_path=excluded.plain_path,status='encrypted',updated_at=CURRENT_TIMESTAMP`, task.MediaID, s.EncPath, s.WrappedDEK, s.IV, s.OriginalPath); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `UPDATE media SET file_path=? WHERE id=? AND file_path=?`, s.EncPath, task.MediaID, s.OriginalPath)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			if n != 1 {
				return errors.New("encrypt commit source fence lost")
			}
		}
		var fileType string
		if err = tx.QueryRowContext(ctx, `SELECT file_type FROM media WHERE id=?`, task.MediaID).Scan(&fileType); err != nil {
			return err
		}
		refs := map[string]any{"path": s.EncPath, "size": s.Size, "sha256": s.SHA256, "wrapped_dek": s.WrappedDEK, "iv": s.IV}
		if fileType == "image" {
			variants, e := validatedEncryptedPhotoVariantsTx(ctx, tx, task.MediaID)
			if e != nil {
				return e
			}
			refs["variants"] = variants
		}
		raw, _ := json.Marshal(refs)
		if _, err = tx.ExecContext(ctx, `INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,'encrypt',?,?,'generated',CURRENT_TIMESTAMP,?)`, *task.RunID, *task.StepID, task.MediaID, task.Generation, s.SourceFingerprint, string(raw), s.StageID); err != nil {
			return err
		}
		_, _ = tx.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='committed',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state IN ('staged','quarantined')`, s.StageID)
		if err = finishEncryptionLifecycleTx(ctx, tx, task); err != nil {
			return err
		}
		return publication.AggregateTx(ctx, tx, *task.RunID)
	})
	if err == nil {
		if quarantinePath != "" {
			if removeErr := os.Remove(quarantinePath); removeErr != nil && !os.IsNotExist(removeErr) {
				return removeErr
			}
		}
		return nil
	}
	var uncertain *store.ImmediateCommitError
	if !errors.As(err, &uncertain) {
		if quarantinePath != "" {
			if restoreErr := restoreQuarantinedPlaintext(quarantinePath, s.OriginalPath, quarantineRoot); restoreErr != nil {
				_, _ = db.ExecContext(context.Background(), `UPDATE media SET publication_state='failed',status='active',last_error=? WHERE id=?`, "encryption restore failed: "+restoreErr.Error(), task.MediaID)
				_, _ = db.ExecContext(context.Background(), `UPDATE media_encryption_stage_journal SET state='failed_closed',recovery_error=? WHERE stage_id=?`, restoreErr.Error(), s.StageID)
				return errors.Join(err, restoreErr)
			}
			_, _ = db.ExecContext(context.Background(), `UPDATE media_encryption_stage_journal SET state='restored',quarantine_path='',recovery_error='definite_rollback_restored' WHERE stage_id=?`, s.StageID)
		}
		return err
	}
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if reconcileEncryptionFinalization(rctx, db, task, s.StageID) == nil {
		return nil
	}
	return uncertain
}
func selectedEncryptionStage(ctx context.Context, db *sql.DB, task Task, s storage.StagedMediaEncryption) (bool, error) {
	var selected string
	err := db.QueryRowContext(ctx, `SELECT m.file_path FROM post_ingest_task p JOIN media_ingest_step step ON step.id=p.ingest_step_id JOIN media_ingest_run r ON r.id=p.ingest_run_id JOIN media m ON m.id=p.media_id WHERE p.id=? AND p.media_id=? AND p.generation=? AND p.ingest_run_id=? AND p.ingest_step_id=? AND p.status='running' AND p.lease_owner=? AND p.attempts=? AND step.status='running' AND step.lease_owner=p.lease_owner AND step.attempts=p.attempts AND r.status='processing' AND r.superseded_at IS NULL AND COALESCE(r.superseded_by_generation,0)=0 AND m.ingest_generation=p.generation`, task.ID, task.MediaID, task.Generation, *task.RunID, *task.StepID, task.LeaseOwner, task.Attempts).Scan(&selected)
	if err != nil {
		return false, err
	}
	if samePathForEvidence(selected, s.EncPath) {
		return true, nil
	}
	if !samePathForEvidence(selected, s.OriginalPath) {
		return false, errors.New("encrypt source selection changed")
	}
	fp, err := publication.SourceFingerprint(selected)
	return false, errOrMismatch(err, fp, s.SourceFingerprint)
}
func errOrMismatch(err error, got, want string) error {
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("encrypt source fingerprint changed")
	}
	return nil
}
func reconcileEncryptionFinalization(ctx context.Context, db *sql.DB, task Task, stageID string) error {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_evidence e JOIN post_ingest_task p ON p.id=? JOIN media_ingest_step s ON s.id=? JOIN media_ingest_run r ON r.id=? JOIN media m ON m.id=? JOIN media_encrypted_assets a ON a.media_id=m.id AND a.enc_path=m.file_path WHERE e.step_id=s.id AND e.kind='encrypt' AND e.stage_id=? AND e.run_id=r.id AND e.media_id=m.id AND e.generation=? AND p.status='done' AND s.status='done' AND r.status IN ('published','degraded') AND m.ingest_generation=?`, task.ID, *task.StepID, *task.RunID, task.MediaID, stageID, task.Generation, task.Generation).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("encrypt reconciliation mismatch")
	}
	return nil
}
func cleanupUnreferencedEncryptionStage(ctx context.Context, db *sql.DB, s storage.StagedMediaEncryption) error {
	var refs int
	_ = db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM media m WHERE m.file_path=?)+(SELECT COUNT(*) FROM media_encrypted_assets a WHERE a.enc_path=? AND a.status='encrypted')+(SELECT COUNT(*) FROM media_ingest_evidence e WHERE e.stage_id=?)`, s.EncPath, s.EncPath, s.StageID).Scan(&refs)
	if refs > 0 {
		return nil
	}
	if err := os.Remove(s.EncPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, _ = db.ExecContext(ctx, `UPDATE media_encryption_stage_journal SET state='quarantined',recovery_error='cleaned_unreferenced',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state IN ('staged','quarantined')`, s.StageID)
	return nil
}
func cleanupPlaintextAfterCommittedEncryption(db *sql.DB, s storage.StagedMediaEncryption) {
	var n int
	if db.QueryRow(`SELECT COUNT(*) FROM media m JOIN media_encrypted_assets a ON a.media_id=m.id AND a.enc_path=m.file_path AND a.enc_path=? WHERE m.id=?`, s.EncPath, s.MediaID).Scan(&n) == nil && n == 1 {
		_ = os.Remove(s.OriginalPath)
	}
}
func finishEncryptionLifecycleTx(ctx context.Context, tx store.SQLExecutor, task Task) error {
	res, err := tx.ExecContext(ctx, `UPDATE post_ingest_task SET status='done',lease_owner=NULL,lease_until=NULL,last_error='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND attempts=?`, task.ID, task.LeaseOwner, task.Attempts)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("encrypt queue fence lost")
	}
	res, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='done',lease_owner=NULL,lease_until=NULL,last_error='',finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='running' AND lease_owner=? AND attempts=?`, *task.StepID, task.LeaseOwner, task.Attempts)
	if err != nil {
		return err
	}
	n, _ = res.RowsAffected()
	if n != 1 {
		return errors.New("encrypt step fence lost")
	}
	return nil
}
func usableEncryptedOutput(ctx context.Context, db *sql.DB, mediaID int64) (bool, error) {
	return storage.IsEncryptedAssetRecordValid(ctx, db, mediaID)
}

func isPermanentEncryptError(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"not configured", "no library", "empty file path", "plain file missing", "file does not exist", "cannot find the file"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// EnqueuePendingMediaEncryption adds eligible missing/invalid encrypted assets to the unified queue.
func EnqueuePendingMediaEncryption(ctx context.Context, db *sql.DB, enqueue func(context.Context, int64, *int64, TaskType) (bool, error)) error {
	if db == nil {
		return errors.New("pending media encryption: database is not configured")
	}
	if enqueue == nil {
		return errors.New("pending media encryption: enqueue is not configured")
	}
	var after int64
	for {
		rows, err := db.QueryContext(ctx, `
SELECT m.id
FROM media m
JOIN library l ON l.id=m.library_id
WHERE COALESCE(l.encrypted_assets_enabled,0)=1
  AND COALESCE(m.status,'active')='active'
  AND m.id>?
ORDER BY m.id
LIMIT 100`, after)
		if err != nil {
			return err
		}
		ids := make([]int64, 0, 100)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			ready, err := usableEncryptedOutput(ctx, db, id)
			if err != nil {
				return err
			}
			if !ready {
				if _, err := enqueue(ctx, id, nil, TaskEncrypt); err != nil {
					return err
				}
			}
		}
		after = ids[len(ids)-1]
		if len(ids) < 100 {
			return nil
		}
	}
}

func samePathForEvidence(a, b string) bool {
	aa, ea := filepath.Abs(a)
	bb, eb := filepath.Abs(b)
	return ea == nil && eb == nil && strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}
func validatedEncryptedPhotoVariantsTx(ctx context.Context, tx store.SQLExecutor, mediaID int64) ([]any, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&raw); err != nil {
		return nil, err
	}
	var root map[string]any
	if json.Unmarshal([]byte(raw), &root) != nil {
		return nil, errors.New("invalid photo metadata")
	}
	photo, _ := root["photo"].(map[string]any)
	var out []any
	for _, v := range []struct{ kind, logical, key string }{{"photo_thumb", "thumb.jpg", "thumb_path"}, {"photo_medium", "medium.jpg", "medium_path"}} {
		path, _ := photo[v.key].(string)
		var selected, wrapped, iv string
		if err := tx.QueryRowContext(ctx, `SELECT enc_path,wrapped_dek,iv FROM media_derived_assets WHERE media_id=? AND artifact_kind=? AND logical_name=?`, mediaID, v.kind, v.logical).Scan(&selected, &wrapped, &iv); err != nil {
			return nil, err
		}
		if !samePathForEvidence(path, selected) || wrapped == "" || iv == "" {
			return nil, errors.New("photo derivative selection mismatch")
		}
		size, hash, err := hashPath(path)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"kind": v.kind, "logical_name": v.logical, "path": path, "size": size, "sha256": hash, "wrapped_dek": wrapped, "iv": iv})
	}
	return out, nil
}
