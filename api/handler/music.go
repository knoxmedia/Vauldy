package handler

import (
	"database/sql"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/musicparse"
	"knox-media/internal/musicstore"
	"knox-media/internal/textencoding"
)

// ListLibraryAlbums returns albums grouped for a music library.
func (h *Handler) ListLibraryAlbums(c *gin.Context) {
	libID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	items, err := h.queryLibraryAlbums(libID, "", "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(items) == 0 {
		var mediaCount int
		_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM media WHERE library_id = ? AND file_type = 'audio' AND status = 'active'`, libID).Scan(&mediaCount)
		if mediaCount > 0 {
			_, _ = musicstore.BackfillLibraryMusic(h.App.DB, libID)
			_ = musicstore.MergeUnknownAlbums(h.App.DB, libID)
			items, err = h.queryLibraryAlbums(libID, "", "")
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
	} else {
		var trackTotal int
		for _, it := range items {
			if tc, ok := it["track_count"].(int64); ok && tc > 0 {
				trackTotal += int(tc)
			}
		}
		if trackTotal == 0 {
			_, _ = musicstore.BackfillLibraryMusic(h.App.DB, libID)
			_ = musicstore.MergeUnknownAlbums(h.App.DB, libID)
			items, err = h.queryLibraryAlbums(libID, "", "")
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
	}
	// Drop stale empty unknown-album shells left from earlier versions.
	_, _ = h.App.DB.Exec(`
		DELETE FROM music_album
		WHERE library_id = ? AND is_unknown = 1
		  AND id NOT IN (
		    SELECT DISTINCT mt.album_id FROM music_track mt
		    JOIN media m ON m.id = mt.media_id AND m.status = 'active'
		    WHERE m.library_id = ?
		  )
		  AND id NOT IN (
		    SELECT MIN(id) FROM music_album WHERE library_id = ? AND is_unknown = 1
		  )
	`, libID, libID, libID)
	items, err = h.queryLibraryAlbums(libID, "", "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) queryLibraryAlbums(libID int64, artistFilter, genreFilter string) ([]gin.H, error) {
	args := []any{libID}
	where := `WHERE a.library_id = ?`
	if strings.TrimSpace(artistFilter) != "" {
		where += ` AND ar.name_norm = ?`
		args = append(args, musicparse.NormKey(artistFilter))
	}
	if strings.TrimSpace(genreFilter) != "" {
		if strings.EqualFold(strings.TrimSpace(genreFilter), "未知流派") {
			where += ` AND TRIM(COALESCE(a.genre, '')) = ''`
		} else {
			where += ` AND LOWER(COALESCE(a.genre, '')) = LOWER(?)`
			args = append(args, strings.TrimSpace(genreFilter))
		}
	}
	rows, err := h.App.DB.Query(`
		SELECT a.id, a.title, a.title_norm, COALESCE(a.year, 0), COALESCE(a.genre, ''),
			COALESCE(a.artwork_path, ''), COALESCE(a.is_unknown, 0), COALESCE(a.rating, 0),
			COALESCE(ar.name, ''), COALESCE(ar.id, 0),
			(SELECT COUNT(1) FROM music_track mt JOIN media m ON m.id = mt.media_id AND m.status = 'active' WHERE mt.album_id = a.id) AS track_count,
			(SELECT COALESCE(SUM(m.duration), 0) FROM music_track mt JOIN media m ON m.id = mt.media_id AND m.status = 'active' WHERE mt.album_id = a.id) AS total_duration,
			a.created_at, a.updated_at
		FROM music_album a
		LEFT JOIN music_artist ar ON ar.id = a.album_artist_id
		`+where+`
		ORDER BY a.is_unknown ASC, a.title COLLATE NOCASE ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var id, year, trackCount, totalDur, artistID, rating, isUnknown int64
		var title, titleNorm, genre, artwork, artistName, created, updated sql.NullString
		if err := rows.Scan(&id, &title, &titleNorm, &year, &genre, &artwork, &isUnknown, &rating, &artistName, &artistID, &trackCount, &totalDur, &created, &updated); err != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id, "library_id": libID, "title": textencoding.FixMetadataString(title.String), "title_norm": titleNorm.String,
			"year": year, "genre": textencoding.FixMetadataString(genre.String), "artwork_path": artwork.String,
			"album_artist": textencoding.FixMetadataString(artistName.String), "album_artist_id": artistID,
			"track_count": trackCount, "total_duration": totalDur,
			"is_unknown": isUnknown > 0, "rating": rating,
			"created_at": created.String, "updated_at": updated.String,
		})
	}
	return items, nil
}

// ListLibraryArtists returns artists for a music library.
func (h *Handler) ListLibraryArtists(c *gin.Context) {
	libID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	rows, err := h.App.DB.Query(`
		SELECT ar.id, ar.name, ar.name_norm, COALESCE(ar.artwork_path, ''),
			(SELECT COUNT(DISTINCT a.id) FROM music_album a WHERE a.album_artist_id = ar.id) AS album_count,
			(SELECT COUNT(1) FROM music_track mt JOIN music_album a ON a.id = mt.album_id WHERE a.album_artist_id = ar.id) AS track_count
		FROM music_artist ar
		WHERE ar.library_id = ?
		ORDER BY ar.name COLLATE NOCASE ASC
	`, libID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var id, albumCount, trackCount int64
		var name, nameNorm, artwork sql.NullString
		if rows.Scan(&id, &name, &nameNorm, &artwork, &albumCount, &trackCount) != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id, "library_id": libID, "name": name.String, "name_norm": nameNorm.String,
			"artwork_path": artwork.String, "album_count": albumCount, "track_count": trackCount,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ListLibraryGenres returns distinct genres for a music library.
func (h *Handler) ListLibraryGenres(c *gin.Context) {
	libID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	rows, err := h.App.DB.Query(`
		SELECT COALESCE(NULLIF(TRIM(genre), ''), '未知流派') AS genre,
			COUNT(DISTINCT id) AS album_count,
			(SELECT COUNT(1) FROM music_track mt WHERE mt.album_id IN (
				SELECT id FROM music_album a2 WHERE a2.library_id = ? AND COALESCE(NULLIF(TRIM(a2.genre), ''), '未知流派') = COALESCE(NULLIF(TRIM(music_album.genre), ''), '未知流派')
			)) AS track_count
		FROM music_album
		WHERE library_id = ?
		GROUP BY COALESCE(NULLIF(TRIM(genre), ''), '未知流派')
		ORDER BY genre COLLATE NOCASE ASC
	`, libID, libID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var albumCount, trackCount int64
		var genre sql.NullString
		if rows.Scan(&genre, &albumCount, &trackCount) != nil {
			continue
		}
		items = append(items, gin.H{
			"genre": genre.String, "album_count": albumCount, "track_count": trackCount,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ListLibraryTracks returns flat track list for a music library.
func (h *Handler) ListLibraryTracks(c *gin.Context) {
	libID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	items, err := h.queryLibraryTracks(libID, 0, 0, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) queryLibraryTracks(libID, albumID, artistID int64, genre string) ([]gin.H, error) {
	args := []any{libID}
	where := `WHERE m.library_id = ? AND m.file_type = 'audio' AND m.status = 'active'`
	if albumID > 0 {
		where += ` AND mt.album_id = ?`
		args = append(args, albumID)
	}
	if artistID > 0 {
		where += ` AND a.album_artist_id = ?`
		args = append(args, artistID)
	}
	if strings.TrimSpace(genre) != "" {
		where += ` AND LOWER(COALESCE(a.genre, '')) = LOWER(?)`
		args = append(args, strings.TrimSpace(genre))
	}
	rows, err := h.App.DB.Query(`
		SELECT mt.id, mt.media_id, COALESCE(mt.track_number, 0), mt.title, COALESCE(mt.artist_display, ''),
			COALESCE(m.duration, 0), COALESCE(m.bitrate, 0), COALESCE(m.format, ''),
			a.id, a.title, COALESCE(ar.name, ''), COALESCE(ar.id, 0), COALESCE(a.year, 0), COALESCE(a.artwork_path, ''),
			m.file_path, m.created_at
		FROM music_track mt
		JOIN media m ON m.id = mt.media_id
		JOIN music_album a ON a.id = mt.album_id
		LEFT JOIN music_artist ar ON ar.id = a.album_artist_id
		`+where+`
		ORDER BY a.title COLLATE NOCASE ASC, mt.sort_order ASC, COALESCE(mt.track_number, 0) ASC, mt.id ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var trackID, mediaID, trackNum, duration, bitrate, albumIDVal, artistIDVal, year int64
		var title, artist, albumTitle, albumArtist, format, artwork, filePath, created sql.NullString
		if rows.Scan(&trackID, &mediaID, &trackNum, &title, &artist, &duration, &bitrate, &format,
			&albumIDVal, &albumTitle, &albumArtist, &artistIDVal, &year, &artwork, &filePath, &created) != nil {
			continue
		}
		items = append(items, gin.H{
			"id": trackID, "media_id": mediaID, "track_number": trackNum,
			"title": textencoding.FixMetadataString(title.String), "artist": textencoding.FixMetadataString(artist.String),
			"duration": duration, "bitrate": bitrate, "format": format.String,
			"album_id": albumIDVal, "album_title": textencoding.FixMetadataString(albumTitle.String),
			"album_artist": textencoding.FixMetadataString(albumArtist.String), "artist_id": artistIDVal, "year": year,
			"artwork_path": artwork.String, "file_path": filePath.String,
			"created_at": created.String,
		})
	}
	return items, nil
}

// GetAlbum returns album detail with tracks.
func (h *Handler) GetAlbum(c *gin.Context) {
	albumID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || albumID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid album id"})
		return
	}
	row := h.App.DB.QueryRow(`
		SELECT a.id, a.library_id, a.title, a.title_norm, COALESCE(a.year, 0), COALESCE(a.genre, ''),
			COALESCE(a.artwork_path, ''), COALESCE(a.is_unknown, 0), COALESCE(a.rating, 0),
			COALESCE(ar.name, ''), COALESCE(ar.id, 0), COALESCE(a.meta_json, ''),
			a.created_at, a.updated_at
		FROM music_album a
		LEFT JOIN music_artist ar ON ar.id = a.album_artist_id
		WHERE a.id = ?
	`, albumID)
	var id, libID, year, artistID, rating, isUnknown int64
	var title, titleNorm, genre, artwork, artistName, metaJSON, created, updated sql.NullString
	if err := row.Scan(&id, &libID, &title, &titleNorm, &year, &genre, &artwork, &isUnknown, &rating, &artistName, &artistID, &metaJSON, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	musicstore.RefreshAlbumEncoding(h.App.DB, albumID)
	_, _ = h.App.DB.Exec(`UPDATE music_track SET track_number = 0 WHERE track_number IS NULL`)
	_, _ = musicstore.SyncAlbumTracks(h.App.DB, albumID)
	tracks, err := h.queryLibraryTracks(libID, albumID, 0, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(tracks) == 0 {
		_, _ = musicstore.BackfillLibraryMusic(h.App.DB, libID)
		_ = musicstore.MergeUnknownAlbums(h.App.DB, libID)
		_, _ = musicstore.SyncAlbumTracks(h.App.DB, albumID)
		tracks, err = h.queryLibraryTracks(libID, albumID, 0, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	var totalDur int64
	for _, t := range tracks {
		if d, ok := t["duration"].(int64); ok {
			totalDur += d
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"id": id, "library_id": libID, "title": textencoding.FixMetadataString(title.String), "title_norm": titleNorm.String,
		"year": year, "genre": textencoding.FixMetadataString(genre.String), "artwork_path": artwork.String,
		"album_artist": textencoding.FixMetadataString(artistName.String), "album_artist_id": artistID,
		"is_unknown": isUnknown > 0, "rating": rating, "meta_json": metaJSON.String,
		"track_count": len(tracks), "total_duration": totalDur,
		"tracks": tracks, "created_at": created.String, "updated_at": updated.String,
	})
}

// GetAlbumPlayTarget returns the first track media_id for album playback.
func (h *Handler) GetAlbumPlayTarget(c *gin.Context) {
	albumID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || albumID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid album id"})
		return
	}
	var libID, mediaID int64
	err = h.App.DB.QueryRow(`
		SELECT a.library_id, mt.media_id
		FROM music_album a
		JOIN music_track mt ON mt.album_id = a.id
		WHERE a.id = ?
		ORDER BY mt.sort_order ASC, mt.track_number ASC, mt.id ASC
		LIMIT 1
	`, albumID).Scan(&libID, &mediaID)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "no tracks"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	var position int64
	_ = h.App.DB.QueryRow(`
		SELECT COALESCE(pp.position, 0) FROM play_progress pp
		JOIN media m ON m.file_id = pp.file_id
		WHERE m.id = ? ORDER BY pp.update_at DESC LIMIT 1
	`, mediaID).Scan(&position)
	c.JSON(http.StatusOK, gin.H{"media_id": mediaID, "position": position})
}

// ServeAlbumArtwork serves album cover from cache path or folder cover.
func (h *Handler) ServeAlbumArtwork(c *gin.Context) {
	albumID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || albumID <= 0 {
		c.Status(http.StatusBadRequest)
		return
	}
	var libID int64
	var artworkPath sql.NullString
	if err := h.App.DB.QueryRow(`SELECT library_id, artwork_path FROM music_album WHERE id = ?`, albumID).Scan(&libID, &artworkPath); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	if artworkPath.Valid && strings.TrimSpace(artworkPath.String) != "" {
		if _, statErr := os.Stat(artworkPath.String); statErr == nil {
			c.File(artworkPath.String)
			return
		}
	}
	// Try first track's folder cover
	var filePath sql.NullString
	_ = h.App.DB.QueryRow(`
		SELECT m.file_path FROM music_track mt
		JOIN media m ON m.id = mt.media_id
		WHERE mt.album_id = ? ORDER BY mt.sort_order ASC, mt.track_number ASC LIMIT 1
	`, albumID).Scan(&filePath)
	if filePath.Valid {
		for _, candidate := range musicstore.AlbumArtworkCandidates(filePath.String) {
			if _, statErr := os.Stat(candidate); statErr == nil {
				c.File(candidate)
				return
			}
		}
	}
	c.Status(http.StatusNotFound)
}

// ListArtistAlbums returns albums for a specific artist.
func (h *Handler) ListArtistAlbums(c *gin.Context) {
	artistID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || artistID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid artist id"})
		return
	}
	var libID int64
	if err := h.App.DB.QueryRow(`SELECT library_id FROM music_artist WHERE id = ?`, artistID).Scan(&libID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	var artistName sql.NullString
	_ = h.App.DB.QueryRow(`SELECT name FROM music_artist WHERE id = ?`, artistID).Scan(&artistName)
	items, err := h.queryLibraryAlbums(libID, artistName.String, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "artist_id": artistID, "artist_name": artistName.String})
}

// ListGenreAlbums returns albums for a specific genre in a music library.
func (h *Handler) ListGenreAlbums(c *gin.Context) {
	libID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	genre := strings.TrimSpace(c.Query("genre"))
	if genre == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "genre required"})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	items, err := h.queryLibraryAlbums(libID, "", genre)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "library_id": libID, "genre": genre})
}
