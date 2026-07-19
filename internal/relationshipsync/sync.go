package relationshipsync

import (
	"context"
	"database/sql"
	"knox-media/internal/musicparse"
	"knox-media/internal/musicstore"
	"knox-media/internal/tvparse"
	"knox-media/internal/tvstore"
	"strings"
)

func SyncTx(ctx context.Context, tx *sql.Tx, mediaID int64) error {
	var libraryID int64
	var libraryType, fileType, path, meta string
	if err := tx.QueryRowContext(ctx, `SELECT m.library_id,COALESCE(l.type,''),COALESCE(m.file_type,''),COALESCE(m.file_path,''),COALESCE(m.meta_json,'') FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(&libraryID, &libraryType, &fileType, &path, &meta); err != nil {
		return err
	}
	var oldEpisodeID, oldSeasonID, oldSeriesID, oldAlbumID int64
	var oldArtistID sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT e.id,e.season_id,s.tv_id FROM episode_media em JOIN episode e ON e.id=em.episode_id JOIN season s ON s.id=e.season_id WHERE em.media_id=?`, mediaID).Scan(&oldEpisodeID, &oldSeasonID, &oldSeriesID)
	_ = tx.QueryRowContext(ctx, `SELECT a.id,a.album_artist_id FROM music_track mt JOIN music_album a ON a.id=mt.album_id WHERE mt.media_id=?`, mediaID).Scan(&oldAlbumID, &oldArtistID)
	switch {
	case fileType == "audio" && musicparse.IsMusicLibraryType(libraryType):
		if _, err := tx.ExecContext(ctx, `DELETE FROM episode_media WHERE media_id=?`, mediaID); err != nil {
			return err
		}
		if err := musicstore.LinkTrackTx(ctx, tx, libraryID, mediaID, musicstore.DecodeMusicMeta(meta, path)); err != nil {
			return err
		}
	case fileType == "video" && tvparse.IsTVLibraryType(libraryType):
		if _, err := tx.ExecContext(ctx, `DELETE FROM music_track WHERE media_id=?`, mediaID); err != nil {
			return err
		}
		info, ok := tvparse.ParseEpisodeFromMedia(path, meta)
		if ok && strings.TrimSpace(info.SeriesTitleNorm) != "" {
			if err := tvstore.LinkEpisodeTx(ctx, tx, libraryID, mediaID, info); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `DELETE FROM episode_media WHERE media_id=?`, mediaID); err != nil {
			return err
		}
	default:
		if _, err := tx.ExecContext(ctx, `DELETE FROM music_track WHERE media_id=?`, mediaID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM episode_media WHERE media_id=?`, mediaID); err != nil {
			return err
		}
	}
	return prune(ctx, tx, oldEpisodeID, oldSeasonID, oldSeriesID, oldAlbumID, oldArtistID)
}
func prune(ctx context.Context, tx *sql.Tx, episodeID, seasonID, seriesID, albumID int64, artist sql.NullInt64) error {
	if episodeID > 0 {
		if _, e := tx.ExecContext(ctx, `DELETE FROM episode WHERE id=? AND NOT EXISTS(SELECT 1 FROM episode_media WHERE episode_id=?)`, episodeID, episodeID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `DELETE FROM season WHERE id=? AND NOT EXISTS(SELECT 1 FROM episode WHERE season_id=?)`, seasonID, seasonID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `DELETE FROM series WHERE id=? AND NOT EXISTS(SELECT 1 FROM season WHERE tv_id=?)`, seriesID, seriesID); e != nil {
			return e
		}
	}
	if albumID > 0 {
		if _, e := tx.ExecContext(ctx, `DELETE FROM music_album WHERE id=? AND NOT EXISTS(SELECT 1 FROM music_track WHERE album_id=?)`, albumID, albumID); e != nil {
			return e
		}
		if artist.Valid {
			if _, e := tx.ExecContext(ctx, `DELETE FROM music_artist WHERE id=? AND NOT EXISTS(SELECT 1 FROM music_album WHERE album_artist_id=?)`, artist.Int64, artist.Int64); e != nil {
				return e
			}
		}
	}
	return nil
}
