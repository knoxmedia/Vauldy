package mediastore

import "database/sql"

// DeleteCatalog removes a media row and its dependent catalog records.
// This mirrors the admin delete path so library scan sync can remove stale
// entries without hitting foreign-key constraint failures.
func DeleteCatalog(db *sql.DB, mediaID int64, fileID string) error {
	if db == nil || mediaID <= 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM favorite WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM favorite_folder_item WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM playlist_item WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM scrape_task WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM scrape_history WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM media_subtitle WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM subtitle_task WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM lyric_task WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM atrack_task WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM keyframe_task WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM preview_task WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM media_derived_assets WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM package_task WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM drm_license_audit WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM drm_key_material WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM drm_asset WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM library_node WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM music_track WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM episode_media WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM photo_face WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM photo_face_task WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM photo_classify_task WHERE media_id = ?`, []any{mediaID}},
		{`DELETE FROM photo_location_task WHERE media_id = ?`, []any{mediaID}},
	}
	if fileID != "" {
		stmts = append(stmts, struct {
			q    string
			args []any
		}{`DELETE FROM transcode_task WHERE file_id = ?`, []any{fileID}})
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s.q, s.args...); err != nil {
			return err
		}
	}
	if fileID != "" {
		if _, err := tx.Exec(`DELETE FROM play_progress WHERE file_id = ?`, fileID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM media WHERE id = ?`, mediaID); err != nil {
		return err
	}
	return tx.Commit()
}
