package photogeocode

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"knox-media/internal/keystore"
	"knox-media/internal/photoparse"
)

// Worker resolves GPS locations for photo library images asynchronously.
type Worker struct {
	DB    *sql.DB
	Vault *keystore.Vault
	Geo   *Service
	mu   sync.Mutex
	busy map[int64]bool
}

func NewWorker(db *sql.DB, vault *keystore.Vault, geo *Service) *Worker {
	return &Worker{DB: db, Vault: vault, Geo: geo, busy: map[int64]bool{}}
}

func (w *Worker) Enqueue(mediaID, libraryID int64) error {
	if w == nil || w.DB == nil || mediaID <= 0 || libraryID <= 0 {
		return fmt.Errorf("invalid enqueue args")
	}
	_, err := w.DB.Exec(`
		INSERT INTO photo_location_task (media_id, library_id, status, created_at, updated_at)
		VALUES (?, ?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
			library_id = excluded.library_id,
			status = 'pending',
			message = NULL,
			started_at = NULL,
			finished_at = NULL,
			updated_at = CURRENT_TIMESTAMP
	`, mediaID, libraryID)
	return err
}

// EnqueueLibraryAll queues every active image in a library for GPS/location parsing.
func (w *Worker) EnqueueLibraryAll(libraryID int64) (int64, error) {
	rows, err := w.DB.Query(`
		SELECT id FROM media
		WHERE library_id = ? AND file_type = 'image' AND status = 'active'
	`, libraryID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil || id <= 0 {
			continue
		}
		if e := w.Enqueue(id, libraryID); e == nil {
			n++
		}
	}
	return n, nil
}

// LibraryProgress returns location parsing stats for a photo library.
func (w *Worker) LibraryProgress(libraryID int64) (total, located, pending int64, err error) {
	if w == nil || w.DB == nil {
		return 0, 0, 0, fmt.Errorf("worker unavailable")
	}
	if err = w.DB.QueryRow(`
		SELECT COUNT(1) FROM media WHERE library_id = ? AND file_type = 'image' AND status = 'active'
	`, libraryID).Scan(&total); err != nil {
		return
	}
	if err = w.DB.QueryRow(`
		SELECT COUNT(1) FROM media
		WHERE library_id = ? AND file_type = 'image' AND status = 'active'
		  AND NULLIF(json_extract(meta_json, '$.photo.place_id'), '') IS NOT NULL
	`, libraryID).Scan(&located); err != nil {
		return
	}
	err = w.DB.QueryRow(`
		SELECT COUNT(1) FROM photo_location_task
		WHERE library_id = ? AND status IN ('pending', 'running', 'failed')
	`, libraryID).Scan(&pending)
	return
}

func (w *Worker) RunBatch(ctx context.Context, limit int) (done, failed int) {
	if w == nil || w.DB == nil || limit <= 0 {
		return 0, 0
	}
	rows, err := w.DB.Query(`
		SELECT media_id FROM photo_location_task
		WHERE status IN ('pending', 'failed', 'running')
		ORDER BY updated_at ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return done, failed
		default:
		}
		if err := w.Process(ctx, id); err != nil {
			failed++
		} else {
			done++
		}
	}
	return done, failed
}

func (w *Worker) Process(ctx context.Context, mediaID int64) error {
	if w == nil || w.DB == nil || mediaID <= 0 {
		return fmt.Errorf("invalid media id")
	}
	w.mu.Lock()
	if w.busy[mediaID] {
		w.mu.Unlock()
		return nil
	}
	w.busy[mediaID] = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.busy, mediaID)
		w.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	_, _ = w.DB.Exec(`
		UPDATE photo_location_task SET status='running', started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE media_id = ?`, mediaID)

	var path, metaJSON string
	if err := w.DB.QueryRow(`
		SELECT file_path, COALESCE(meta_json, '') FROM media WHERE id = ?
	`, mediaID).Scan(&path, &metaJSON); err != nil {
		w.fail(mediaID, err.Error())
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		w.fail(mediaID, "empty file path")
		return fmt.Errorf("empty file path")
	}

	meta := photoparse.ParseForMedia(w.DB, w.Vault, mediaID, path)
	if !meta.HasGPS {
		w.complete(mediaID, "no gps")
		return nil
	}
	if w.Geo != nil {
		w.Geo.EnrichMeta(&meta)
	}
	merged := photoparse.MergePhotoMetaJSON(metaJSON, meta)
	if _, err := w.DB.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, merged, mediaID); err != nil {
		w.fail(mediaID, err.Error())
		return err
	}
	w.complete(mediaID, "")
	return nil
}

func (w *Worker) complete(mediaID int64, msg string) {
	var m any
	if msg != "" {
		m = msg
	}
	_, _ = w.DB.Exec(`
		UPDATE photo_location_task SET status='done', message=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE media_id = ?`, m, mediaID)
}

func (w *Worker) fail(mediaID int64, msg string) {
	_, _ = w.DB.Exec(`
		UPDATE photo_location_task SET status='failed', message=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE media_id = ?`, msg, mediaID)
}
