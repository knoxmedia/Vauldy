package tvstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"knox-media/internal/tvparse"
)

// LinkEpisode associates a scanned media file with series/season/episode records.
// It merges series across folders by tmdbid, tvdbid, or normalized title.
func LinkEpisode(db *sql.DB, libraryID, mediaID int64, info tvparse.EpisodeInfo) error {
	return linkEpisodeContext(context.Background(), db, libraryID, mediaID, info)
}
func LinkEpisodeExecutor(ctx context.Context, db relationDB, libraryID, mediaID int64, info tvparse.EpisodeInfo) error {
	return linkEpisodeContext(ctx, db, libraryID, mediaID, info)
}

func LinkEpisodeTx(ctx context.Context, tx *sql.Tx, libraryID, mediaID int64, info tvparse.EpisodeInfo) error {
	return linkEpisodeContext(ctx, tx, libraryID, mediaID, info)
}

type relationDB interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func linkEpisodeContext(ctx context.Context, db relationDB, libraryID, mediaID int64, info tvparse.EpisodeInfo) error {
	if db == nil || libraryID <= 0 || mediaID <= 0 || info.SeriesTitleNorm == "" {
		return nil
	}
	seriesID, err := findOrCreateSeries(ctx, db, libraryID, info)
	if err != nil {
		return err
	}
	seasonID, err := findOrCreateSeason(ctx, db, seriesID, info)
	if err != nil {
		return err
	}
	return linkEpisodeMedia(ctx, db, seasonID, mediaID, info)
}

func findOrCreateSeries(ctx context.Context, db relationDB, libraryID int64, info tvparse.EpisodeInfo) (int64, error) {
	if id := lookupSeriesByExternalID(ctx, db, libraryID, info.TMDBID, info.TVDBID); id > 0 {
		_ = appendSeriesFolder(ctx, db, id, info.SourceFolder)
		return id, nil
	}
	if id := lookupSeriesByNormTitle(ctx, db, libraryID, info.SeriesTitleNorm, info.Year); id > 0 {
		_ = mergeSeriesExternalIDs(ctx, db, id, info.TMDBID, info.TVDBID)
		_ = appendSeriesFolder(ctx, db, id, info.SourceFolder)
		return id, nil
	}
	title := info.SeriesTitle
	if title == "" {
		title = info.SeriesTitleNorm
	}
	folders, _ := json.Marshal([]string{info.SourceFolder})
	var res sql.Result
	var err error
	if info.Year > 0 {
		res, err = db.ExecContext(ctx, `
			INSERT INTO series (library_id, title, title_norm, year, tmdb_id, tvdb_id, folder_paths)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			libraryID, title, info.SeriesTitleNorm, info.Year, nullIfEmpty(info.TMDBID), nullIfEmpty(info.TVDBID), string(folders),
		)
	} else {
		res, err = db.ExecContext(ctx, `
			INSERT INTO series (library_id, title, title_norm, year, tmdb_id, tvdb_id, folder_paths)
			VALUES (?, ?, ?, NULL, ?, ?, ?)`,
			libraryID, title, info.SeriesTitleNorm, nullIfEmpty(info.TMDBID), nullIfEmpty(info.TVDBID), string(folders),
		)
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func lookupSeriesByExternalID(ctx context.Context, db relationDB, libraryID int64, tmdbID, tvdbID string) int64 {
	tmdbID = strings.TrimSpace(tmdbID)
	tvdbID = strings.TrimSpace(tvdbID)
	if tmdbID != "" {
		var id int64
		if db.QueryRowContext(ctx, `SELECT id FROM series WHERE library_id = ? AND tmdb_id = ? LIMIT 1`, libraryID, tmdbID).Scan(&id) == nil && id > 0 {
			return id
		}
	}
	if tvdbID != "" {
		var id int64
		if db.QueryRowContext(ctx, `SELECT id FROM series WHERE library_id = ? AND tvdb_id = ? LIMIT 1`, libraryID, tvdbID).Scan(&id) == nil && id > 0 {
			return id
		}
	}
	return 0
}

func lookupSeriesByNormTitle(ctx context.Context, db relationDB, libraryID int64, titleNorm string, year int) int64 {
	titleNorm = strings.TrimSpace(titleNorm)
	if titleNorm == "" {
		return 0
	}
	if year > 0 {
		var id int64
		if db.QueryRowContext(ctx, `SELECT id FROM series WHERE library_id = ? AND title_norm = ? AND COALESCE(year, 0) = ? LIMIT 1`, libraryID, titleNorm, year).Scan(&id) == nil && id > 0 {
			return id
		}
		// Explicit year in folder name: do not fall back to title-only (avoids merging 2005 vs 2022 remakes).
		return 0
	}
	var id int64
	if db.QueryRowContext(ctx, `SELECT id FROM series WHERE library_id = ? AND title_norm = ? AND COALESCE(year, 0) = 0 LIMIT 1`, libraryID, titleNorm).Scan(&id) == nil && id > 0 {
		return id
	}
	return 0
}

func mergeSeriesExternalIDs(ctx context.Context, db relationDB, seriesID int64, tmdbID, tvdbID string) error {
	tmdbID = strings.TrimSpace(tmdbID)
	tvdbID = strings.TrimSpace(tvdbID)
	if tmdbID != "" {
		_, _ = db.ExecContext(ctx, `UPDATE series SET tmdb_id = COALESCE(NULLIF(tmdb_id,''), ?), updated_at = CURRENT_TIMESTAMP WHERE id = ?`, tmdbID, seriesID)
	}
	if tvdbID != "" {
		_, _ = db.ExecContext(ctx, `UPDATE series SET tvdb_id = COALESCE(NULLIF(tvdb_id,''), ?), updated_at = CURRENT_TIMESTAMP WHERE id = ?`, tvdbID, seriesID)
	}
	return nil
}

func appendSeriesFolder(ctx context.Context, db relationDB, seriesID int64, folder string) error {
	folder = strings.TrimSpace(folder)
	if folder == "" || seriesID <= 0 {
		return nil
	}
	var raw sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT folder_paths FROM series WHERE id = ?`, seriesID).Scan(&raw); err != nil {
		return err
	}
	var paths []string
	if raw.Valid && strings.TrimSpace(raw.String) != "" {
		_ = json.Unmarshal([]byte(raw.String), &paths)
	}
	for _, p := range paths {
		if strings.EqualFold(strings.TrimSpace(p), folder) {
			return nil
		}
	}
	paths = append(paths, folder)
	b, _ := json.Marshal(paths)
	_, err := db.ExecContext(ctx, `UPDATE series SET folder_paths = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(b), seriesID)
	return err
}

func findOrCreateSeason(ctx context.Context, db relationDB, seriesID int64, info tvparse.EpisodeInfo) (int64, error) {
	var seasonID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM season WHERE tv_id = ? AND season_num = ? LIMIT 1`, seriesID, info.SeasonNum).Scan(&seasonID)
	if err == nil && seasonID > 0 {
		return seasonID, nil
	}
	name := seasonDisplayName(info.SeasonNum, info.IsSpecial)
	res, err := db.ExecContext(ctx, `INSERT INTO season (tv_id, season_num, name) VALUES (?, ?, ?)`, seriesID, info.SeasonNum, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func seasonDisplayName(num int, special bool) string {
	if special || num == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %02d", num)
}

func linkEpisodeMedia(ctx context.Context, db relationDB, seasonID, mediaID int64, info tvparse.EpisodeInfo) error {
	var episodeID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM episode WHERE season_id = ? AND episode_num = ? LIMIT 1`, seasonID, info.EpisodeNum).Scan(&episodeID)
	if err == sql.ErrNoRows || episodeID == 0 {
		title := info.EpisodeTitle
		res, insErr := db.ExecContext(ctx, `
			INSERT INTO episode (season_id, episode_num, title, file_path)
			VALUES (?, ?, ?, (SELECT file_path FROM media WHERE id = ? LIMIT 1))`,
			seasonID, info.EpisodeNum, nullIfEmpty(title), mediaID,
		)
		if insErr != nil {
			return insErr
		}
		episodeID, _ = res.LastInsertId()
	} else if err != nil {
		return err
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO episode_media (episode_id, media_id, sort_order)
		VALUES (?, ?, COALESCE((SELECT MAX(sort_order) FROM episode_media WHERE episode_id = ?), -1) + 1)
		ON CONFLICT(media_id) DO UPDATE SET episode_id = excluded.episode_id`,
		episodeID, mediaID, episodeID,
	)
	return err
}

// BackfillLibraryTV links all video media in a TV library to series/season/episode records.
// Used after scan or when series list is empty but media files exist (e.g. pre-upgrade libraries).
func BackfillLibraryTV(db *sql.DB, libraryID int64) (linked int, err error) {
	if db == nil || libraryID <= 0 {
		return 0, nil
	}
	rows, err := db.Query(`
		SELECT id, COALESCE(file_path,''), COALESCE(meta_json,'')
		FROM media
		WHERE library_id = ? AND file_type = 'video' AND status = 'active'
	`, libraryID)
	if err != nil {
		return 0, err
	}
	type mediaRow struct {
		id   int64
		path string
		meta string
	}
	pending := make([]mediaRow, 0, 32)
	for rows.Next() {
		var mediaID int64
		var path, meta string
		if rows.Scan(&mediaID, &path, &meta) != nil || mediaID <= 0 || strings.TrimSpace(path) == "" {
			continue
		}
		pending = append(pending, mediaRow{id: mediaID, path: path, meta: meta})
	}
	if err := rows.Close(); err != nil {
		return linked, err
	}
	for _, row := range pending {
		info, ok := tvparse.ParseEpisodeFromMedia(row.path, row.meta)
		if !ok {
			continue
		}
		if linkErr := LinkEpisode(db, libraryID, row.id, info); linkErr == nil {
			linked++
		}
	}
	folderLinked, err := backfillUnlinkedFolderGroups(db, libraryID)
	return linked + folderLinked, err
}

func backfillUnlinkedFolderGroups(db *sql.DB, libraryID int64) (linked int, err error) {
	rows, err := db.Query(`
		SELECT m.id, COALESCE(m.file_path,''), COALESCE(m.meta_json,'')
		FROM media m
		LEFT JOIN episode_media em ON em.media_id = m.id
		WHERE m.library_id = ? AND m.file_type = 'video' AND m.status = 'active' AND em.id IS NULL
	`, libraryID)
	if err != nil {
		return 0, err
	}
	type mediaRow struct {
		id   int64
		path string
		meta string
	}
	pending := make([]mediaRow, 0, 32)
	for rows.Next() {
		var mediaID int64
		var path, meta string
		if rows.Scan(&mediaID, &path, &meta) != nil || mediaID <= 0 || strings.TrimSpace(path) == "" {
			continue
		}
		pending = append(pending, mediaRow{id: mediaID, path: path, meta: meta})
	}
	if err := rows.Close(); err != nil {
		return linked, err
	}
	type folderItem struct {
		row      mediaRow
		baseName string
		epNum    int
	}
	groups := make(map[string][]folderItem)
	for _, row := range pending {
		showFolder := tvparse.ShowFolderName(row.path)
		if !tvparse.IsValidShowFolderName(showFolder) {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(row.path), filepath.Ext(row.path))
		epNum := 0
		if ep, ok := tvparse.ParseLooseEpisodeNumber(base, showFolder); ok {
			epNum = ep
		}
		groups[showFolder] = append(groups[showFolder], folderItem{row: row, baseName: base, epNum: epNum})
	}
	for showFolder, items := range groups {
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool {
			oi, oj := items[i].epNum, items[j].epNum
			if oi <= 0 {
				oi = 100000 + i
			}
			if oj <= 0 {
				oj = 100000 + j
			}
			if oi != oj {
				return oi < oj
			}
			return items[i].baseName < items[j].baseName
		})
		used := make(map[int]struct{})
		nextAuto := 1
		for _, item := range items {
			ep := item.epNum
			if ep <= 0 {
				for {
					if _, exists := used[nextAuto]; !exists {
						ep = nextAuto
						break
					}
					nextAuto++
				}
			}
			for {
				if _, exists := used[ep]; !exists {
					break
				}
				ep++
			}
			used[ep] = struct{}{}
			if ep >= nextAuto {
				nextAuto = ep + 1
			}
			info := tvparse.BuildEpisodeInfoFromFolder(item.row.path, showFolder, ep)
			if linkErr := LinkEpisode(db, libraryID, item.row.id, info); linkErr == nil {
				linked++
			}
		}
	}
	return linked, nil
}

func CleanupMedia(db *sql.DB, mediaID int64) {
	if db == nil || mediaID <= 0 {
		return
	}
	var episodeID int64
	if db.QueryRow(`SELECT episode_id FROM episode_media WHERE media_id = ?`, mediaID).Scan(&episodeID) != nil || episodeID <= 0 {
		return
	}
	_, _ = db.Exec(`DELETE FROM episode_media WHERE media_id = ?`, mediaID)
	var remaining int
	_ = db.QueryRow(`SELECT COUNT(1) FROM episode_media WHERE episode_id = ?`, episodeID).Scan(&remaining)
	if remaining == 0 {
		var seasonID int64
		if db.QueryRow(`SELECT season_id FROM episode WHERE id = ?`, episodeID).Scan(&seasonID) == nil {
			_, _ = db.Exec(`DELETE FROM episode WHERE id = ?`, episodeID)
			_ = db.QueryRow(`SELECT COUNT(1) FROM episode WHERE season_id = ?`, seasonID).Scan(&remaining)
			if remaining == 0 {
				var seriesID int64
				if db.QueryRow(`SELECT tv_id FROM season WHERE id = ?`, seasonID).Scan(&seriesID) == nil {
					_, _ = db.Exec(`DELETE FROM season WHERE id = ?`, seasonID)
					_ = db.QueryRow(`SELECT COUNT(1) FROM season WHERE tv_id = ?`, seriesID).Scan(&remaining)
					if remaining == 0 {
						_, _ = db.Exec(`DELETE FROM series WHERE id = ?`, seriesID)
					}
				}
			}
		}
	}
}

// PruneOrphansForLibrary removes series/season/episode rows with no linked media after scan.
func PruneOrphansForLibrary(db *sql.DB, libraryID int64) {
	_ = PruneOrphansForLibraryContext(context.Background(), db, libraryID)
}
func PruneOrphansForLibraryContext(ctx context.Context, db *sql.DB, libraryID int64) error {
	if db == nil || libraryID <= 0 {
		return nil
	}
	qs := []string{`DELETE FROM episode_media WHERE media_id IN (SELECT id FROM media WHERE library_id=?) AND media_id NOT IN (SELECT id FROM media WHERE library_id=?)`, `DELETE FROM episode WHERE id NOT IN (SELECT episode_id FROM episode_media) AND season_id IN (SELECT s.id FROM season s JOIN series sr ON sr.id=s.tv_id WHERE sr.library_id=?)`, `DELETE FROM season WHERE id NOT IN (SELECT season_id FROM episode) AND tv_id IN (SELECT id FROM series WHERE library_id=?)`, `DELETE FROM series WHERE library_id=? AND id NOT IN (SELECT tv_id FROM season WHERE tv_id IS NOT NULL)`}
	for _, q := range qs {
		if _, err := db.ExecContext(ctx, q, libraryID, libraryID); err != nil {
			return err
		}
	}
	return nil
}

func nullIfEmpty(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}
