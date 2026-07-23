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

	"knox-media/internal/imagethumb"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

type thumbnailWorker interface {
	Ensure(context.Context, int64) (imagethumb.Paths, error)
}

type thumbnailAdapter struct {
	db     *sql.DB
	worker thumbnailWorker
}

func NewThumbnailAdapter(db *sql.DB, worker interface {
	Ensure(context.Context, int64) (imagethumb.Paths, error)
}) Adapter {
	return &thumbnailAdapter{db: db, worker: worker}
}

func (a *thumbnailAdapter) Execute(ctx context.Context, task Task) error {
	if a == nil || a.db == nil {
		return permanentAdapterError(TaskThumbnail, "database is not configured")
	}
	if err := validateBasicAdapterTask(task, TaskThumbnail); err != nil {
		return err
	}
	if a.worker == nil {
		return permanentAdapterError(TaskThumbnail, "worker is not configured")
	}
	var fileType string
	if err := a.db.QueryRowContext(ctx, `SELECT COALESCE(file_type,'') FROM media WHERE id=?`, task.MediaID).Scan(&fileType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return permanentAdapterError(TaskThumbnail, "media not found")
		}
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(fileType), "image") {
		return permanentAdapterError(TaskThumbnail, "thumbnail requires image media")
	}
	if err := validateAdapterLease(ctx, a.db, task); err != nil {
		return err
	}
	paths, ready, err := usablePhotoVariants(ctx, a.db, task.MediaID)
	if err != nil {
		return err
	}
	if !ready {
		ctx = imagethumb.WithCommitGuard(ctx, func(guardCtx context.Context) error {
			return validateAdapterLease(guardCtx, a.db, task)
		})
		ctx = storage.WithDerivedCommitGuardTx(ctx, func(guardCtx context.Context, tx *sql.Tx) error {
			return validateAdapterLeaseTx(guardCtx, tx, task)
		})
		paths, err = a.worker.Ensure(ctx, task.MediaID)
		if err != nil {
			return ClassifiedError{Kind: FailureRetryable, Err: err}
		}
	}
	if err = commitThumbnailMetadata(ctx, a.db, task, paths); err != nil {
		return err
	}
	return validateAdapterLease(ctx, a.db, task)
}

func usablePhotoVariants(ctx context.Context, db *sql.DB, mediaID int64) (imagethumb.Paths, bool, error) {
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(meta_json,'') FROM media WHERE id=?`, mediaID).Scan(&raw); err != nil {
		return imagethumb.Paths{}, false, err
	}
	var root map[string]any
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &root)
	}
	photo, _ := root["photo"].(map[string]any)
	paths := imagethumb.Paths{Thumb: stringField(photo, "thumb_path"), Medium: stringField(photo, "medium_path")}
	return paths, usableFile(paths.Thumb) && usableFile(paths.Medium), nil
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, _ := m[key].(string)
	return strings.TrimSpace(value)
}
func usableFile(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	return err == nil && !info.IsDir() && info.Size() > 0
}

func commitThumbnailMetadata(ctx context.Context, db *sql.DB, task Task, paths imagethumb.Paths) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateAdapterLeaseTx(ctx, tx, task); err != nil {
		return err
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(meta_json,'') FROM media WHERE id=?`, task.MediaID).Scan(&raw); err != nil {
		return err
	}
	var root map[string]any
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	photo, _ := root["photo"].(map[string]any)
	if photo == nil {
		photo = map[string]any{}
	}
	photo["thumb_path"], photo["medium_path"] = paths.Thumb, paths.Medium
	root["photo"] = photo
	merged, err := json.Marshal(root)
	if err != nil {
		return err
	}
	if err = store.UpdateMediaMetaAndPhotoTime(ctx, tx, task.MediaID, string(merged)); err != nil {
		return err
	}
	return tx.Commit()
}

type LocalThumbnailWorker struct {
	DB         *sql.DB
	Vault      *keystore.Vault
	Derived    *storage.DerivedAssetStore
	FFmpegPath string
	PreviewDir string
}

func (w *LocalThumbnailWorker) Ensure(ctx context.Context, mediaID int64) (imagethumb.Paths, error) {
	if w == nil || w.DB == nil {
		return imagethumb.Paths{}, fmt.Errorf("thumbnail worker: database is not configured")
	}
	var libraryID int64
	var source string
	if err := w.DB.QueryRowContext(ctx, `SELECT library_id,COALESCE(file_path,'') FROM media WHERE id=?`, mediaID).Scan(&libraryID, &source); err != nil {
		return imagethumb.Paths{}, err
	}
	source = storage.PreferredFFmpegPath(w.DB, mediaID, libraryID, source)
	return imagethumb.Ensure(ctx, w.DB, w.Vault, w.Derived, w.FFmpegPath, source, filepath.Join(w.PreviewDir, "photos"), mediaID)
}
