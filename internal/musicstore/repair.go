package musicstore

import (
	"context"
	"database/sql"
	"strings"

	"knox-media/internal/musicparse"
	"knox-media/internal/textencoding"
)

// RefreshAlbumEncoding re-parses track metadata and updates garbled album/track/media titles.
func RefreshAlbumEncoding(db *sql.DB, albumID int64) error {
	if db == nil || albumID <= 0 {
		return nil
	}
	rows, err := db.Query(`
		SELECT m.id, COALESCE(m.file_path,''), COALESCE(m.meta_json,'')
		FROM music_track mt
		JOIN media m ON m.id = mt.media_id AND m.status = 'active'
		WHERE mt.album_id = ?
	`, albumID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var firstMeta *musicparse.TrackMeta
	for rows.Next() {
		var mediaID int64
		var path, metaJSON string
		if rows.Scan(&mediaID, &path, &metaJSON) != nil {
			continue
		}
		meta := DecodeMusicMeta(metaJSON, path)
		if firstMeta == nil {
			cp := meta
			firstMeta = &cp
		}
		fixedTitle := textencoding.FixMetadataString(meta.Title)
		if fixedTitle == "" {
			fixedTitle = meta.Title
		}
		_, _ = db.Exec(`UPDATE media SET title = ? WHERE id = ?`, fixedTitle, mediaID)
		if _, err := db.ExecContext(context.Background(), `UPDATE media SET meta_json=? WHERE id=?`, MergeMusicMetaJSON(metaJSON, meta), mediaID); err != nil {
			return err
		}
		_ = linkTrackMedia(context.Background(), db, albumID, mediaID, meta)
	}
	if firstMeta == nil {
		return nil
	}
	albumTitle := textencoding.FixMetadataString(firstMeta.Album)
	if albumTitle == "" || albumTitle == musicparse.UnknownAlbum {
		return nil
	}
	artistName := textencoding.FixMetadataString(firstMeta.AlbumArtist)
	if artistName == "" {
		artistName = musicparse.VariousArtists
	}
	var libID int64
	if db.QueryRow(`SELECT library_id FROM music_album WHERE id = ?`, albumID).Scan(&libID) != nil || libID <= 0 {
		return nil
	}
	artistID, err := findOrCreateArtist(context.Background(), db, libID, artistName)
	if err != nil || artistID <= 0 {
		return nil
	}
	_, _ = db.Exec(`
		UPDATE music_album
		SET title = ?, title_norm = ?, album_artist_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, albumTitle, musicparse.NormKey(albumTitle), artistID, albumID)
	if strings.TrimSpace(firstMeta.Genre) != "" {
		_, _ = db.Exec(`UPDATE music_album SET genre = ? WHERE id = ?`, firstMeta.Genre, albumID)
	}
	if firstMeta.Year > 0 {
		_, _ = db.Exec(`UPDATE music_album SET year = COALESCE(NULLIF(year, 0), ?) WHERE id = ?`, firstMeta.Year, albumID)
	}
	return nil
}
