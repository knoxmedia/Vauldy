package mediastore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"

	"knox-media/internal/photoface"
)

// DeleteCatalog removes a media row and its dependent catalog records.
// This mirrors the admin delete path so library scan sync can remove stale
// entries without hitting foreign-key constraint failures.
type PersonStatsRefresher func(context.Context, *sql.Tx, []int64) error

type CleanupInfo struct {
	FaceIDs      []int64
	DerivedPaths []string
	Paths        []string
}

func DeleteCatalog(db *sql.DB, mediaID int64, fileID string) error {
	_, err := DeleteCatalogAndCollect(context.Background(), db, mediaID, fileID, "")
	return err
}

func DeleteCatalogAndCollect(ctx context.Context, db *sql.DB, mediaID int64, fileID, photoCacheDir string) (CleanupInfo, error) {
	var cleanup CleanupInfo
	if db == nil || mediaID <= 0 {
		return cleanup, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return cleanup, err
	}
	defer tx.Rollback()
	cleanup, err = DeleteCatalogAndCollectTx(ctx, tx, mediaID, fileID, photoCacheDir)
	if err != nil {
		return CleanupInfo{}, err
	}
	if err = tx.Commit(); err != nil {
		return CleanupInfo{}, err
	}
	return cleanup, nil
}

func DeleteCatalogAndCollectTx(ctx context.Context, tx *sql.Tx, mediaID int64, fileID, photoCacheDir string) (CleanupInfo, error) {
	var cleanup CleanupInfo
	if tx == nil || mediaID <= 0 {
		return cleanup, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,person_id FROM photo_face WHERE media_id=?`, mediaID)
	if err != nil {
		return cleanup, err
	}
	var personIDs []int64
	for rows.Next() {
		var faceID int64
		var person sql.NullInt64
		if err = rows.Scan(&faceID, &person); err != nil {
			rows.Close()
			return CleanupInfo{}, err
		}
		cleanup.FaceIDs = append(cleanup.FaceIDs, faceID)
		if person.Valid {
			personIDs = append(personIDs, person.Int64)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return CleanupInfo{}, err
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT enc_path FROM media_derived_assets WHERE media_id=?`, mediaID)
	if err != nil {
		return CleanupInfo{}, err
	}
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			rows.Close()
			return CleanupInfo{}, err
		}
		cleanup.DerivedPaths = append(cleanup.DerivedPaths, path)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return CleanupInfo{}, err
	}
	rows.Close()
	cleanup.Paths = append(cleanup.Paths, cleanup.DerivedPaths...)
	for _, faceID := range cleanup.FaceIDs {
		if strings.TrimSpace(photoCacheDir) != "" {
			cleanup.Paths = append(cleanup.Paths, photoface.ExpectedFaceThumbnailPath(photoCacheDir, faceID))
		}
	}
	for _, path := range cleanup.Paths {
		if _, err = tx.ExecContext(ctx, `INSERT INTO media_file_cleanup_task(path,status,attempts,next_retry_at,last_error,created_at,updated_at) VALUES(?,'pending',0,CURRENT_TIMESTAMP,NULL,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP) ON CONFLICT(path) DO UPDATE SET status='pending',next_retry_at=CURRENT_TIMESTAMP,last_error=NULL,updated_at=CURRENT_TIMESTAMP`, filepath.Clean(path)); err != nil {
			return CleanupInfo{}, err
		}
	}
	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM favorite WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM favorite_folder_item WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM playlist_item WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM scrape_task WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM scrape_history WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM media_subtitle WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM subtitle_task WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM lyric_task WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM atrack_task WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM keyframe_task WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM preview_task WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM media_derived_assets WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM package_task WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM library_node WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM music_track WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM episode_media WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM photo_face WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM photo_face_task WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM photo_classify_task WHERE media_id=?`, []any{mediaID}}, {`DELETE FROM photo_location_task WHERE media_id=?`, []any{mediaID}},
	}
	if fileID != "" {
		stmts = append(stmts, struct {
			q    string
			args []any
		}{`DELETE FROM transcode_task WHERE file_id=?`, []any{fileID}})
	}
	for _, stmt := range stmts {
		if _, err = tx.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			return CleanupInfo{}, err
		}
	}
	if err = deleteDRMRowsIgnoringMissingTables(ctx, tx, mediaID); err != nil {
		return CleanupInfo{}, err
	}
	if err = photoface.RefreshPersonStatsTx(ctx, tx, personIDs); err != nil {
		return CleanupInfo{}, err
	}
	if fileID != "" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM play_progress WHERE file_id=?`, fileID); err != nil {
			return CleanupInfo{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM media WHERE id=?`, mediaID); err != nil {
		return CleanupInfo{}, err
	}
	return cleanup, nil
}

func DeleteLibraryAndCollect(ctx context.Context, db *sql.DB, libraryID int64, photoCacheDir string) (CleanupInfo, error) {
	var cleanup CleanupInfo
	if db == nil || libraryID <= 0 {
		return cleanup, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return cleanup, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(file_id,'') FROM media WHERE library_id=? ORDER BY id`, libraryID)
	if err != nil {
		return cleanup, err
	}
	type item struct {
		id     int64
		fileID string
	}
	var items []item
	for rows.Next() {
		var x item
		if err = rows.Scan(&x.id, &x.fileID); err != nil {
			rows.Close()
			return CleanupInfo{}, err
		}
		items = append(items, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return CleanupInfo{}, err
	}
	rows.Close()
	for _, x := range items {
		part, e := DeleteCatalogAndCollectTx(ctx, tx, x.id, x.fileID, photoCacheDir)
		if e != nil {
			return CleanupInfo{}, e
		}
		cleanup.FaceIDs = append(cleanup.FaceIDs, part.FaceIDs...)
		cleanup.DerivedPaths = append(cleanup.DerivedPaths, part.DerivedPaths...)
		cleanup.Paths = append(cleanup.Paths, part.Paths...)
	}
	for _, q := range []string{`DELETE FROM library_folder WHERE library_id=?`, `DELETE FROM library_node WHERE library_id=?`, `DELETE FROM library WHERE id=?`} {
		if _, err = tx.ExecContext(ctx, q, libraryID); err != nil {
			return CleanupInfo{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return CleanupInfo{}, err
	}
	return cleanup, nil
}

// deleteDRMRowsIgnoringMissingTables removes DRM rows for a media item. The
// drm_* tables are commercial-only and absent from community builds, so the
// deletes are skipped when the tables do not exist while still failing on any
// real database error in commercial deployments.
func deleteDRMRowsIgnoringMissingTables(ctx context.Context, tx *sql.Tx, mediaID int64) error {
	for _, table := range []string{"drm_license_audit", "drm_key_material", "drm_asset"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE media_id=?`, mediaID); err != nil && !isMissingTableErr(err) {
			return err
		}
	}
	return nil
}

func isMissingTableErr(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "no such table")
}
