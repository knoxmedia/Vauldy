package musicstore

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"

	"knox-media/internal/musicparse"
)

// LinkTrack associates a scanned audio media file with artist/album/track records.
func LinkTrack(db *sql.DB, libraryID, mediaID int64, meta musicparse.TrackMeta) error {
	if db == nil || libraryID <= 0 || mediaID <= 0 {
		return nil
	}
	artistID, err := findOrCreateArtist(db, libraryID, meta.AlbumArtist)
	if err != nil {
		return err
	}
	albumID, err := findOrCreateAlbum(db, libraryID, artistID, meta)
	if err != nil {
		return err
	}
	return linkTrackMedia(db, albumID, mediaID, meta)
}

func findOrCreateArtist(db *sql.DB, libraryID int64, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = musicparse.VariousArtists
	}
	norm := musicparse.NormKey(name)
	var id int64
	err := db.QueryRow(`
		SELECT id FROM music_artist WHERE library_id = ? AND name_norm = ? LIMIT 1
	`, libraryID, norm).Scan(&id)
	if err == nil && id > 0 {
		return id, nil
	}
	res, err := db.Exec(`
		INSERT INTO music_artist (library_id, name, name_norm)
		VALUES (?, ?, ?)
	`, libraryID, name, norm)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func findOrCreateAlbum(db *sql.DB, libraryID, artistID int64, meta musicparse.TrackMeta) (int64, error) {
	title := strings.TrimSpace(meta.Album)
	if title == "" {
		title = musicparse.UnknownAlbum
	}
	norm := musicparse.NormKey(title)
	isUnknown := 0
	if title == musicparse.UnknownAlbum {
		isUnknown = 1
	}
	var id int64
	if isUnknown == 1 {
		// One shared unknown-album bucket per library (Plex-style).
		err := db.QueryRow(`
			SELECT id FROM music_album
			WHERE library_id = ? AND is_unknown = 1
			LIMIT 1
		`, libraryID).Scan(&id)
		if err == nil && id > 0 {
			_, _ = db.Exec(`UPDATE music_album SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
			return id, nil
		}
	} else {
		err := db.QueryRow(`
			SELECT id FROM music_album
			WHERE library_id = ? AND title_norm = ? AND COALESCE(album_artist_id, 0) = ?
			LIMIT 1
		`, libraryID, norm, artistID).Scan(&id)
		if err == nil && id > 0 {
			if meta.Year > 0 {
				_, _ = db.Exec(`UPDATE music_album SET year = COALESCE(NULLIF(year, 0), ?), updated_at = CURRENT_TIMESTAMP WHERE id = ?`, meta.Year, id)
			}
			if strings.TrimSpace(meta.Genre) != "" {
				_, _ = db.Exec(`UPDATE music_album SET genre = COALESCE(NULLIF(TRIM(genre), ''), ?), updated_at = CURRENT_TIMESTAMP WHERE id = ?`, meta.Genre, id)
			}
			return id, nil
		}
	}
	var year any
	if meta.Year > 0 {
		year = meta.Year
	}
	genre := strings.TrimSpace(meta.Genre)
	res, err := db.Exec(`
		INSERT INTO music_album (library_id, title, title_norm, album_artist_id, year, genre, is_unknown)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, libraryID, title, norm, nullIfZero64(artistID), year, nullIfEmpty(genre), isUnknown)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func linkTrackMedia(db *sql.DB, albumID, mediaID int64, meta musicparse.TrackMeta) error {
	sortOrder := meta.TrackNumber
	if sortOrder <= 0 {
		sortOrder = 9999
	}
	trackNum := meta.TrackNumber
	if trackNum < 0 {
		trackNum = 0
	}
	discNum := meta.DiscNumber
	if discNum < 0 {
		discNum = 0
	}
	_, err := db.Exec(`
		INSERT INTO music_track (album_id, media_id, track_number, disc_number, title, artist_display, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(media_id) DO UPDATE SET
			album_id = excluded.album_id,
			track_number = excluded.track_number,
			disc_number = excluded.disc_number,
			title = excluded.title,
			artist_display = excluded.artist_display,
			sort_order = excluded.sort_order
	`, albumID, mediaID, trackNum, discNum, meta.Title, meta.Artist, sortOrder)
	return err
}

// BackfillAlbumTracks re-links audio files whose metadata matches a specific album.
func BackfillAlbumTracks(db *sql.DB, albumID int64) (linked int, err error) {
	if db == nil || albumID <= 0 {
		return 0, nil
	}
	var libID int64
	var titleNorm string
	var artistID sql.NullInt64
	var isUnknown int
	if err := db.QueryRow(`
		SELECT library_id, title_norm, album_artist_id, COALESCE(is_unknown, 0)
		FROM music_album WHERE id = ?
	`, albumID).Scan(&libID, &titleNorm, &artistID, &isUnknown); err != nil {
		return 0, err
	}
	titleNorm = strings.TrimSpace(titleNorm)
	if titleNorm == "" {
		return 0, nil
	}
	var artistNorm string
	if isUnknown == 0 && artistID.Valid && artistID.Int64 > 0 {
		var ns sql.NullString
		if db.QueryRow(`SELECT name_norm FROM music_artist WHERE id = ?`, artistID.Int64).Scan(&ns) == nil && ns.Valid {
			artistNorm = strings.TrimSpace(ns.String)
		}
	}
	rows, err := db.Query(`
		SELECT id, COALESCE(file_path,''), COALESCE(meta_json,'')
		FROM media
		WHERE library_id = ? AND file_type = 'audio' AND status = 'active'
	`, libID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var mediaID int64
		var path, metaJSON string
		if rows.Scan(&mediaID, &path, &metaJSON) != nil {
			continue
		}
		meta := DecodeMusicMeta(metaJSON, path)
		if isUnknown == 1 {
			if musicparse.NormKey(meta.Album) != titleNorm {
				continue
			}
		} else if !albumMetaMatches(titleNorm, artistNorm, meta) {
			continue
		}
		if linkErr := linkTrackMedia(db, albumID, mediaID, meta); linkErr == nil {
			linked++
		}
	}
	return linked, nil
}

func albumMetaMatches(titleNorm, artistNorm string, meta musicparse.TrackMeta) bool {
	if musicparse.NormKey(meta.Album) != titleNorm {
		return false
	}
	if artistNorm == "" {
		return true
	}
	return musicparse.NormKey(meta.AlbumArtist) == artistNorm
}

// SyncAlbumTracks ensures an album has music_track rows by scanning library audio files.
func SyncAlbumTracks(db *sql.DB, albumID int64) (linked int, err error) {
	var count int
	_ = db.QueryRow(`SELECT COUNT(1) FROM music_track mt JOIN media m ON m.id = mt.media_id AND m.status = 'active' WHERE mt.album_id = ?`, albumID).Scan(&count)
	if count > 0 {
		return count, nil
	}
	return BackfillAlbumTracks(db, albumID)
}

// MergeUnknownAlbums collapses duplicate unknown-album rows into one canonical album per library.
func MergeUnknownAlbums(db *sql.DB, libraryID int64) error {
	if db == nil || libraryID <= 0 {
		return nil
	}
	var canonicalID int64
	err := db.QueryRow(`
		SELECT id FROM music_album WHERE library_id = ? AND is_unknown = 1 ORDER BY id ASC LIMIT 1
	`, libraryID).Scan(&canonicalID)
	if err != nil || canonicalID <= 0 {
		return nil
	}
	rows, err := db.Query(`
		SELECT id FROM music_album WHERE library_id = ? AND is_unknown = 1 AND id != ?
	`, libraryID, canonicalID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var dupID int64
		if rows.Scan(&dupID) != nil || dupID <= 0 {
			continue
		}
		_, _ = db.Exec(`UPDATE music_track SET album_id = ? WHERE album_id = ?`, canonicalID, dupID)
		_, _ = db.Exec(`DELETE FROM music_album WHERE id = ?`, dupID)
	}
	return nil
}

// BackfillLibraryMusic links all audio media in a music library to album/track records.
func BackfillLibraryMusic(db *sql.DB, libraryID int64) (linked int, err error) {
	if db == nil || libraryID <= 0 {
		return 0, nil
	}
	rows, err := db.Query(`
		SELECT id, COALESCE(file_path,''), COALESCE(meta_json,'')
		FROM media
		WHERE library_id = ? AND file_type = 'audio' AND status = 'active'
	`, libraryID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var mediaID int64
		var path, metaJSON string
		if rows.Scan(&mediaID, &path, &metaJSON) != nil {
			continue
		}
		meta := DecodeMusicMeta(metaJSON, path)
		if linkErr := LinkTrack(db, libraryID, mediaID, meta); linkErr == nil {
			linked++
		}
	}
	return linked, nil
}

// DecodeMusicMeta reads stored media.meta_json music block, falling back to ffprobe/filename parsing.
func storedMusicMeta(meta musicparse.TrackMeta) bool {
	return strings.TrimSpace(meta.Title) != "" ||
		strings.TrimSpace(meta.Album) != "" ||
		strings.TrimSpace(meta.Artist) != "" ||
		strings.TrimSpace(meta.AlbumArtist) != ""
}

func DecodeMusicMeta(metaJSON, filePath string) musicparse.TrackMeta {
	var root struct {
		Music  musicparse.TrackMeta `json:"music"`
		Title  string               `json:"title"`
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if strings.TrimSpace(metaJSON) != "" {
		_ = json.Unmarshal([]byte(metaJSON), &root)
	}
	if storedMusicMeta(root.Music) {
		return musicparse.RepairTrackMeta(root.Music)
	}
	ffprobeRaw := metaJSON
	if strings.TrimSpace(ffprobeRaw) == "" {
		ffprobeRaw = "{}"
	}
	meta := musicparse.ParseFromSources(filePath, ffprobeRaw, 0, 0)
	if strings.TrimSpace(root.Title) != "" && meta.Title == "" {
		meta.Title = root.Title
	}
	return musicparse.RepairTrackMeta(meta)
}

// CleanupMedia removes music track rows for a deleted media item.
func CleanupMedia(db *sql.DB, mediaID int64) {
	if db == nil || mediaID <= 0 {
		return
	}
	_, _ = db.Exec(`DELETE FROM music_track WHERE media_id = ?`, mediaID)
}

// PruneOrphansForLibrary removes albums/artists with no tracks in the library.
func PruneOrphansForLibrary(db *sql.DB, libraryID int64) {
	if db == nil || libraryID <= 0 {
		return
	}
	_, _ = db.Exec(`
		DELETE FROM music_album
		WHERE library_id = ?
		  AND id NOT IN (SELECT DISTINCT album_id FROM music_track mt JOIN media m ON m.id = mt.media_id WHERE m.library_id = ?)
	`, libraryID, libraryID)
	_, _ = db.Exec(`
		DELETE FROM music_artist
		WHERE library_id = ?
		  AND id NOT IN (SELECT DISTINCT album_artist_id FROM music_album WHERE library_id = ? AND album_artist_id IS NOT NULL)
	`, libraryID, libraryID)
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func nullIfZero64(n int64) any {
	if n <= 0 {
		return nil
	}
	return n
}

func nullIfZero(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}

// MergeMusicMetaJSON stores parsed music metadata inside media.meta_json under "music".
func MergeMusicMetaJSON(raw string, meta musicparse.TrackMeta) string {
	var root map[string]any
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	root["music"] = map[string]any{
		"title":        meta.Title,
		"artist":       meta.Artist,
		"album_artist": meta.AlbumArtist,
		"album":        meta.Album,
		"track_number": meta.TrackNumber,
		"disc_number":  meta.DiscNumber,
		"year":         meta.Year,
		"genre":        meta.Genre,
		"sample_rate":  meta.SampleRate,
	}
	b, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(b)
}

// AlbumArtworkCandidates returns embedded/folder artwork search paths for an audio file.
func AlbumArtworkCandidates(filePath string) []string {
	dir := strings.TrimSpace(filepath.Dir(filePath))
	if dir == "" {
		return nil
	}
	names := []string{"cover.jpg", "cover.jpeg", "cover.png", "folder.jpg", "folder.jpeg", "folder.png", "Front.jpg", "front.jpg"}
	var out []string
	for _, n := range names {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}
