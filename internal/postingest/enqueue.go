package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"knox-media/internal/config"
	"knox-media/internal/publication"
	"knox-media/internal/store"
	"knox-media/internal/taskalign"
)

type Enqueuer struct {
	DB      *sql.DB
	Config  *config.Config
	Metrics *store.SQLiteMetrics
}

func NewEnqueuer(db *sql.DB, cfg *config.Config, metrics *store.SQLiteMetrics) *Enqueuer {
	return &Enqueuer{DB: db, Config: cfg, Metrics: metrics}
}

func (e *Enqueuer) EnqueueMedia(ctx context.Context, mediaID int64, scanTaskID *int64, fileType string) ([]TaskType, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil || e.DB == nil {
		return nil, permanentEnqueueError("database is not configured")
	}
	if e.Config == nil {
		return nil, permanentEnqueueError("config is not configured")
	}
	if mediaID <= 0 {
		return nil, permanentEnqueueError("invalid media id")
	}

	var dbFileType string
	var duration int64
	var previewExtract, encryptedAssets int
	err := e.DB.QueryRowContext(ctx, `
SELECT COALESCE(m.file_type,''), COALESCE(m.duration,0),
       COALESCE(l.preview_extract,0), COALESCE(l.encrypted_assets_enabled,0)
FROM media m
JOIN library l ON l.id=m.library_id
WHERE m.id=?`, mediaID).Scan(&dbFileType, &duration, &previewExtract, &encryptedAssets)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, permanentEnqueueError("media not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load media enqueue capabilities: %w", err)
	}
	dbFileType = strings.TrimSpace(dbFileType)
	hint := strings.TrimSpace(fileType)
	if hint != "" && hint != dbFileType {
		return nil, permanentEnqueueError(fmt.Sprintf("file type hint %q does not match database file type %q", hint, dbFileType))
	}

	var planned []TaskType
	if dbFileType == "video" {
		planned = append(planned, TaskPoster)
		if previewExtract == 1 {
			planned = append(planned, TaskPreview)
		}
		planned = append(planned, TaskKeyframe)
		if e.Config.SubtitleAutoOnScan() {
			planned = append(planned, TaskSubtitle)
		}
		if e.Config.ATrackAutoOnScan() {
			planned = append(planned, TaskAtrack)
		}
		if e.Config.EncryptedAssetsEnabled() && encryptedAssets == 1 {
			planned = append(planned, TaskEncrypt)
		}
	}
	_ = duration

	err = store.WithBusyRetry(ctx, e.Metrics, func() error {
		tx, err := e.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		if scanTaskID != nil {
			var exists int
			err := tx.QueryRowContext(ctx, `
SELECT 1
FROM scan_task s
JOIN media m ON m.id=? AND m.library_id=s.library_id
WHERE s.id=?`, mediaID, *scanTaskID).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				return permanentEnqueueError("scan task not found for media library")
			}
			if err != nil {
				return err
			}
		}
		for _, typ := range planned {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO post_ingest_task (media_id,scan_task_id,task_type,max_attempts)
VALUES (?,?,?,?)
ON CONFLICT(media_id,generation,task_type) DO NOTHING`, mediaID, scanTaskID, typ, publication.DefaultMaxAttempts(string(typ))); err != nil {
				return err
			}
			if err := taskalign.EnsureDomainWaiting(ctx, tx, string(typ), mediaID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		var classified ClassifiedError
		if errors.As(err, &classified) {
			return nil, err
		}
		if store.IsSQLiteConstraint(err) {
			return nil, ClassifiedError{Kind: FailurePermanent, Err: fmt.Errorf("enqueue media %d: %w", mediaID, err)}
		}
		return nil, fmt.Errorf("enqueue media %d: %w", mediaID, err)
	}
	return planned, nil
}

func permanentEnqueueError(message string) error {
	return ClassifiedError{Kind: FailurePermanent, Err: errors.New("post-ingest enqueue: " + message)}
}
