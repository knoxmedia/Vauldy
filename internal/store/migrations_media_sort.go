package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"knox-media/internal/phototags"
)

const mediaSortBatchSize = 250
const mediaSortTimeLayout = "2006-01-02T15:04:05.000000Z"

var mediaSortIndexNames = []string{
	"idx_media_library_created_id",
	"idx_media_library_type_created_id",
	"idx_media_library_type_photo_taken_id",
	"idx_media_library_type_photo_timeline_id",
	"idx_media_library_type_photo_place_taken_id",
	"idx_progress_file_update",
	"idx_progress_user_file_update_completed",
	"idx_progress_user_file_completed",
	"idx_photo_face_media_person",
}

// Test hook: called after each committed backfill batch.
var migrateMediaSortAfterBatch func()
var migrateMediaTagsAfterBatch func()
var migrateMediaSortBeforeFinalize func()
var migrateMediaSortAfterSelect func()
var migrateMediaSortCompletedAfterLock func()

// NormalizeMediaTime returns fixed-width UTC microseconds. Invalid values use a
// stable ID-derived timestamp; ascending IDs produce ascending fallback values.
func NormalizeMediaTime(raw string, fallbackID int64) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if seconds >= 0 && seconds <= 253402300799 {
				return time.Unix(seconds, 0).UTC().Format(mediaSortTimeLayout), false
			}
		}
		layouts := []string{
			time.RFC3339Nano,
			"2006-01-02 15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05",
			"2006-01-02",
		}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed.UTC().Format(mediaSortTimeLayout), false
			}
		}
	}
	// 1970 plus ID microseconds is deterministic, bounded, and preserves the
	// original ascending media-id creation order among invalid timestamps.
	if fallbackID < 0 {
		fallbackID = 0
	}
	const maxFallbackID = int64(253402300799000000)
	if fallbackID > maxFallbackID {
		fallbackID = maxFallbackID
	}
	return time.Unix(0, fallbackID*int64(time.Microsecond)).UTC().Format(mediaSortTimeLayout), true
}

func decodePhotoSortFields(metaJSON string) (takenAt, placeID string) {
	var root struct {
		Photo struct {
			TakenAt any `json:"taken_at"`
			PlaceID any `json:"place_id"`
		} `json:"photo"`
	}
	if json.Unmarshal([]byte(metaJSON), &root) != nil {
		return "", ""
	}
	if value, ok := root.Photo.TakenAt.(string); ok {
		takenAt = strings.TrimSpace(value)
	}
	switch value := root.Photo.PlaceID.(type) {
	case string:
		placeID = strings.TrimSpace(value)
	case float64:
		placeID = strconv.FormatFloat(value, 'f', -1, 64)
	}
	return takenAt, placeID
}

func PhotoPlaceID(metaJSON string) string {
	_, place := decodePhotoSortFields(metaJSON)
	return place
}

func PhotoTimelineTime(metaJSON, createdSort string, fallbackID int64) (string, bool) {
	taken, _ := decodePhotoSortFields(metaJSON)
	if taken != "" {
		if value, fallback := NormalizeMediaTime(taken, fallbackID); !fallback {
			return value, false
		}
		return createdSort, true
	}
	return createdSort, false
}

type MediaSortInsertFields struct{ CreatedAt, PhotoTakenAt, PhotoPlaceID string }

func MediaSortInsertValues(now time.Time, metaJSON string, isPhoto bool) MediaSortInsertFields {
	created := now.UTC().Format(mediaSortTimeLayout)
	fields := MediaSortInsertFields{CreatedAt: created}
	if !isPhoto {
		return fields
	}
	fields.PhotoTakenAt, _ = PhotoTimelineTime(metaJSON, created, 0)
	fields.PhotoPlaceID = PhotoPlaceID(metaJSON)
	return fields
}

type mediaSortExec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// UpdateMediaMetaAndPhotoTime updates metadata and its materialized photo sort
// fields through the supplied DB or transaction executor.
func UpdateMediaMetaAndPhotoTime(ctx context.Context, exec mediaSortExec, mediaID int64, metaJSON string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	taken, place := decodePhotoSortFields(metaJSON)
	result, err := exec.ExecContext(ctx, `
        UPDATE media
        SET meta_json = ?,
            photo_taken_at = CASE
                WHEN file_type = 'image' THEN COALESCE(?, created_at_sort)
                ELSE photo_taken_at
            END,
            photo_place_id = CASE WHEN file_type = 'image' THEN NULLIF(?, '') ELSE photo_place_id END
        WHERE id = ?`, metaJSON, normalizedPhotoValue(taken, mediaID), place, mediaID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func normalizedPhotoValue(raw string, id int64) any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	value, fallback := NormalizeMediaTime(raw, id)
	if fallback {
		return nil
	}
	return value
}

func mediaTableColumns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(media)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func ensureMediaSortColumns(ctx context.Context, db *sql.DB) error {
	columns, err := mediaTableColumns(ctx, db)
	if err != nil {
		return err
	}
	for _, column := range []string{"created_at_sort", "photo_taken_at", "photo_place_id"} {
		if columns[column] {
			continue
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE media ADD COLUMN `+column+` TEXT`); err != nil {
			return fmt.Errorf("add media.%s: %w", column, err)
		}
	}
	return nil
}

type mediaSortBackfillRow struct {
	id                            int64
	fileType, createdAt, metaJSON string
}

type mediaTagBackfillRow struct {
	id       int64
	metaJSON string
}

func normalizePhotoTagsJSON(raw string) (string, bool) {
	normalized := phototags.NormalizeMetaJSON(raw)
	return normalized, normalized != raw
}

func migratePhotoTags(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO media_sort_migration_state(version,last_id,completed) VALUES(2,0,0)`); err != nil {
		return err
	}
	var lastID int64
	var completed int
	if err := db.QueryRowContext(ctx, `SELECT last_id,completed FROM media_sort_migration_state WHERE version=2`).Scan(&lastID, &completed); err != nil {
		return err
	}
	if completed == 1 {
		var maxID int64
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM media WHERE file_type='image'`).Scan(&maxID); err != nil {
			return err
		}
		if maxID <= lastID {
			return nil
		}
		if _, err := db.ExecContext(ctx, `UPDATE media_sort_migration_state SET completed=0,completed_at=NULL WHERE version=2`); err != nil {
			return err
		}
		completed = 0
	}
	for completed == 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_sort_migration_state SET last_id=last_id WHERE version=2`); err != nil {
			_ = tx.Rollback()
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(meta_json,'') FROM media WHERE file_type='image' AND id>? ORDER BY id LIMIT ?`, lastID, mediaSortBatchSize)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		batch := make([]mediaTagBackfillRow, 0, mediaSortBatchSize)
		for rows.Next() {
			var row mediaTagBackfillRow
			if err := rows.Scan(&row.id, &row.metaJSON); err != nil {
				rows.Close()
				_ = tx.Rollback()
				return err
			}
			batch = append(batch, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			_ = tx.Rollback()
			return err
		}
		if len(batch) == 0 {
			if _, err = tx.ExecContext(ctx, `UPDATE media_sort_migration_state SET completed=1,completed_at=CURRENT_TIMESTAMP WHERE version=2`); err != nil {
				_ = tx.Rollback()
				return err
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			completed = 1
			continue
		}
		for _, row := range batch {
			if normalized, changed := normalizePhotoTagsJSON(row.metaJSON); changed {
				if err = UpdateMediaMetaAndPhotoTime(ctx, tx, row.id, normalized); err != nil {
					_ = tx.Rollback()
					return err
				}
			}
			lastID = row.id
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_sort_migration_state SET last_id=? WHERE version=2`, lastID); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if migrateMediaTagsAfterBatch != nil {
			migrateMediaTagsAfterBatch()
		}
	}
	return nil
}

func ensureMediaSortIndexes(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_media_library_created_id ON media(library_id, created_at_sort DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_type_created_id ON media(library_id, file_type, created_at_sort DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_type_photo_taken_id ON media(library_id, file_type, photo_taken_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_type_photo_timeline_id ON media(library_id, file_type, COALESCE(photo_taken_at,created_at_sort) DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_media_library_type_photo_place_taken_id ON media(library_id, file_type, photo_place_id, photo_taken_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_progress_file_update ON play_progress(file_id, update_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_progress_user_file_update_completed ON play_progress(user_id, file_id, update_at DESC, completed ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_progress_user_file_completed ON play_progress(user_id, file_id, completed)`,
		`CREATE INDEX IF NOT EXISTS idx_photo_face_media_person ON photo_face(media_id, person_id)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create media sort index: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_progress_user_file_update`); err != nil {
		return fmt.Errorf("drop redundant progress index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_media_encrypted_media_status`); err != nil {
		return fmt.Errorf("drop redundant media encrypted index: %w", err)
	}
	return nil
}

func MigrateMediaSortColumns(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("media sort migration: nil database")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureMediaSortColumns(ctx, db); err != nil {
		return err
	}
	if err := ensureMediaSortIndexes(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS media_sort_migration_state (version INTEGER PRIMARY KEY, last_id INTEGER NOT NULL DEFAULT 0, completed INTEGER NOT NULL DEFAULT 0, completed_at TEXT)`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO media_sort_migration_state(version,last_id,completed) VALUES(1,0,0)`); err != nil {
		return err
	}
	var lastID int64
	var completed int
	if err := db.QueryRowContext(ctx, `SELECT last_id,completed FROM media_sort_migration_state WHERE version=1`).Scan(&lastID, &completed); err != nil {
		return err
	}
	if completed == 1 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `UPDATE media_sort_migration_state SET last_id=last_id WHERE version=1`); err != nil {
			return err
		}
		if migrateMediaSortCompletedAfterLock != nil {
			migrateMediaSortCompletedAfterLock()
		}
		var violations int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media WHERE created_at_sort IS NULL OR (file_type='image' AND photo_taken_at IS NULL)`).Scan(&violations); err != nil {
			return err
		}
		if violations == 0 {
			if err = tx.Commit(); err != nil {
				return err
			}
			return migratePhotoTags(ctx, db)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_sort_migration_state SET last_id=0,completed=0,completed_at=NULL WHERE version=1`); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		lastID, completed = 0, 0
	}
	fallbackWarnings, photoWarnings := 0, 0
	const warningLimit = 10
	for completed == 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if migrateMediaSortBeforeFinalize != nil {
			migrateMediaSortBeforeFinalize()
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// Acquire SQLite's write lock before the batch snapshot. This prevents a
		// deferred transaction from failing with BUSY_SNAPSHOT and prevents stale
		// source values from overwriting a concurrent metadata writer.
		if _, err = tx.ExecContext(ctx, `UPDATE media_sort_migration_state SET last_id=last_id WHERE version=1`); err != nil {
			_ = tx.Rollback()
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(file_type,''),COALESCE(CAST(created_at AS TEXT),''),COALESCE(meta_json,'') FROM media WHERE id>? ORDER BY id LIMIT ?`, lastID, mediaSortBatchSize)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("select media sort batch: %w", err)
		}
		batch := make([]mediaSortBackfillRow, 0, mediaSortBatchSize)
		for rows.Next() {
			var row mediaSortBackfillRow
			if err := rows.Scan(&row.id, &row.fileType, &row.createdAt, &row.metaJSON); err != nil {
				rows.Close()
				_ = tx.Rollback()
				return err
			}
			batch = append(batch, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			_ = tx.Rollback()
			return err
		}
		rows.Close()
		if migrateMediaSortAfterSelect != nil {
			migrateMediaSortAfterSelect()
		}
		if len(batch) == 0 {
			var violations, newer int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media WHERE created_at_sort IS NULL OR (file_type='image' AND photo_taken_at IS NULL)`).Scan(&violations); err != nil {
				_ = tx.Rollback()
				return err
			}
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media WHERE id>?`, lastID).Scan(&newer); err != nil {
				_ = tx.Rollback()
				return err
			}
			if violations > 0 || newer > 0 {
				if _, err = tx.ExecContext(ctx, `UPDATE media_sort_migration_state SET last_id=0,completed=0,completed_at=NULL WHERE version=1`); err != nil {
					_ = tx.Rollback()
					return err
				}
				if err = tx.Commit(); err != nil {
					return err
				}
				lastID = 0
				continue
			}
			if _, err = tx.ExecContext(ctx, `UPDATE media_sort_migration_state SET completed=1,completed_at=CURRENT_TIMESTAMP WHERE version=1`); err != nil {
				_ = tx.Rollback()
				return err
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			completed = 1
			break
		}
		for _, row := range batch {
			created, usedFallback := NormalizeMediaTime(row.createdAt, row.id)
			if usedFallback && fallbackWarnings < warningLimit {
				log.Printf("media sort migration: invalid created_at for media id %d; using deterministic fallback", row.id)
				fallbackWarnings++
			}
			var taken, place any
			if row.fileType == "image" {
				timeline, parseFailed := PhotoTimelineTime(row.metaJSON, created, row.id)
				if parseFailed && photoWarnings < warningLimit {
					log.Printf("media sort migration: invalid photo taken_at for media id %d; using created sort", row.id)
					photoWarnings++
				}
				taken = timeline
				if value := PhotoPlaceID(row.metaJSON); value != "" {
					place = value
				}
			}
			if _, err = tx.ExecContext(ctx, `UPDATE media SET created_at_sort=?,photo_taken_at=CASE WHEN file_type='image' THEN ? ELSE photo_taken_at END,photo_place_id=CASE WHEN file_type='image' THEN ? ELSE photo_place_id END WHERE id=?`, created, taken, place, row.id); err != nil {
				_ = tx.Rollback()
				return err
			}
			lastID = row.id
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media_sort_migration_state SET last_id=? WHERE version=1`, lastID); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if migrateMediaSortAfterBatch != nil {
			migrateMediaSortAfterBatch()
		}
	}
	if err := ensureMediaSortIndexes(ctx, db); err != nil {
		return err
	}
	return migratePhotoTags(ctx, db)
}
