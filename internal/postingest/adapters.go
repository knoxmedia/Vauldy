package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/atrack"
	"knox-media/internal/keyframe"
	"knox-media/internal/preview"
	"knox-media/internal/storage"
	"knox-media/internal/subtitle"
)

type Adapter interface {
	Execute(context.Context, Task) error
}

type AdapterSet struct {
	Poster   Adapter
	Preview  Adapter
	Keyframe Adapter
	Subtitle Adapter
	Atrack   Adapter
}

func (s AdapterSet) Execute(ctx context.Context, task Task) error {
	var adapter Adapter
	switch task.Type {
	case TaskPoster:
		adapter = s.Poster
	case TaskPreview:
		adapter = s.Preview
	case TaskKeyframe:
		adapter = s.Keyframe
	case TaskSubtitle:
		adapter = s.Subtitle
	case TaskAtrack:
		adapter = s.Atrack
	default:
		return ClassifiedError{Kind: FailurePermanent, Err: fmt.Errorf("post-ingest adapter: unsupported task type %q", task.Type)}
	}
	if adapter == nil {
		return ClassifiedError{Kind: FailurePermanent, Err: fmt.Errorf("post-ingest adapter: adapter for task type %q is not configured", task.Type)}
	}
	return adapter.Execute(ctx, task)
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
	query := `SELECT 1 FROM post_ingest_task WHERE id=? AND media_id=? AND task_type=? AND status='running' AND lease_owner=?`
	args := []any{task.ID, task.MediaID, task.Type, task.LeaseOwner}
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
	query := `SELECT 1 FROM post_ingest_task WHERE id=? AND media_id=? AND task_type=? AND status='running' AND lease_owner=?`
	args := []any{task.ID, task.MediaID, task.Type, task.LeaseOwner}
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
