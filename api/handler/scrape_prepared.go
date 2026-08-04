package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
	"strings"
)

type PreparedSeriesSibling struct {
	ID                        int64
	OriginalMeta, DesiredMeta string
}
type PreparedSeriesEffects struct {
	ID                                                                      int64
	OriginalTitle, OriginalPoster, OriginalTMDB, OriginalTVDB, OriginalMeta string
	DesiredTitle, DesiredPoster, DesiredTMDB, DesiredTVDB, DesiredMeta      string
	Siblings                                                                []PreparedSeriesSibling
}

func prepareSeriesEffects(ctx context.Context, q store.SQLExecutor, libraryID, mediaID int64, res *scraper.ScrapeResult) (*PreparedSeriesEffects, error) {
	if q == nil || res == nil || libraryID <= 0 {
		return nil, nil
	}
	var x PreparedSeriesEffects
	var count int64
	err := q.QueryRowContext(ctx, `SELECT sr.id,COALESCE(sr.title,''),COALESCE(sr.poster,''),COALESCE(sr.tmdb_id,''),COALESCE(sr.tvdb_id,''),COALESCE(sr.meta_json,''),(SELECT COUNT(DISTINCT em2.media_id) FROM season se2 JOIN episode ep2 ON ep2.season_id=se2.id JOIN episode_media em2 ON em2.episode_id=ep2.id WHERE se2.tv_id=sr.id) FROM episode_media em JOIN episode ep ON ep.id=em.episode_id JOIN season se ON se.id=ep.season_id JOIN series sr ON sr.id=se.tv_id WHERE em.media_id=? AND sr.library_id=? LIMIT 1`, mediaID, libraryID).Scan(&x.ID, &x.OriginalTitle, &x.OriginalPoster, &x.OriginalTMDB, &x.OriginalTVDB, &x.OriginalMeta, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	x.DesiredTitle = x.OriginalTitle
	if strings.TrimSpace(res.Title) != "" {
		x.DesiredTitle = res.Title
	}
	x.DesiredPoster = x.OriginalPoster
	if strings.TrimSpace(res.Poster) != "" {
		x.DesiredPoster = res.Poster
	}
	x.DesiredTMDB = x.OriginalTMDB
	x.DesiredTVDB = x.OriginalTVDB
	overview := res.Overview
	backdrop := res.Backdrop
	if strings.TrimSpace(overview) == "" || strings.TrimSpace(backdrop) == "" {
		var old map[string]any
		if json.Unmarshal([]byte(x.OriginalMeta), &old) == nil {
			if sm, _ := old["scrape"].(map[string]any); sm != nil {
				if strings.TrimSpace(overview) == "" {
					overview = stringScrapeField(sm["overview"])
				}
				if strings.TrimSpace(backdrop) == "" {
					backdrop = stringScrapeField(sm["backdrop"])
				}
			}
		}
	}
	if res.Extra != nil {
		if v := stringScrapeField(res.Extra["series_title"]); v != "" {
			x.DesiredTitle = v
		}
		if v := stringScrapeField(res.Extra["series_poster"]); v != "" {
			x.DesiredPoster = v
		}
		if v := stringScrapeField(res.Extra["series_overview"]); v != "" {
			overview = v
		}
		if v := stringScrapeField(res.Extra["series_backdrop"]); v != "" {
			backdrop = v
		}
		if v := stringScrapeField(res.Extra["tmdb_id"]); v != "" && x.DesiredTMDB == "" {
			x.DesiredTMDB = v
		}
		if v := stringScrapeField(res.Extra["tvdb_id"]); v != "" && x.DesiredTVDB == "" {
			x.DesiredTVDB = v
		}
	}
	if shouldPreserveSeriesTitle(x.OriginalTitle, count) {
		x.DesiredTitle = x.OriginalTitle
	}
	raw, err := json.Marshal(map[string]any{"scrape": map[string]any{"title": x.DesiredTitle, "overview": overview, "poster": x.DesiredPoster, "backdrop": backdrop, "source": res.Source, "release_date": res.ReleaseDate, "rating": res.Rating, "genres": res.Genres, "extra": map[string]any{"tmdb_id": x.DesiredTMDB, "tmdb_type": "tv", "tvdb_id": x.DesiredTVDB}}})
	if err != nil {
		return nil, err
	}
	x.DesiredMeta = string(raw)
	patch := map[string]any{"scrape": map[string]any{"series_title": x.DesiredTitle, "series_overview": overview, "series_poster": x.DesiredPoster, "series_backdrop": backdrop, "extra": map[string]any{"series_title": x.DesiredTitle, "series_overview": overview, "series_poster": x.DesiredPoster, "series_backdrop": backdrop, "tmdb_id": x.DesiredTMDB, "tmdb_type": "tv", "tvdb_id": x.DesiredTVDB}}}
	rows, err := q.QueryContext(ctx, `SELECT m.id,COALESCE(m.meta_json,'') FROM episode_media em JOIN episode ep ON ep.id=em.episode_id JOIN season se ON se.id=ep.season_id JOIN media m ON m.id=em.media_id WHERE se.tv_id=? AND m.id!=?`, x.ID, mediaID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v PreparedSeriesSibling
		if err := rows.Scan(&v.ID, &v.OriginalMeta); err != nil {
			rows.Close()
			return nil, err
		}
		v.DesiredMeta, err = scraper.MergeSeriesFieldsPreservingEpisode(v.OriginalMeta, patch)
		if err != nil {
			rows.Close()
			return nil, err
		}
		x.Siblings = append(x.Siblings, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return &x, nil
}
func applyPreparedSeriesEffectsTx(ctx context.Context, q store.SQLExecutor, x PreparedSeriesEffects) error {
	r, err := q.ExecContext(ctx, `UPDATE series SET title=?,poster=?,tmdb_id=?,tvdb_id=?,meta_json=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND COALESCE(title,'')=? AND COALESCE(poster,'')=? AND COALESCE(tmdb_id,'')=? AND COALESCE(tvdb_id,'')=? AND COALESCE(meta_json,'')=?`, x.DesiredTitle, x.DesiredPoster, x.DesiredTMDB, x.DesiredTVDB, x.DesiredMeta, x.ID, x.OriginalTitle, x.OriginalPoster, x.OriginalTMDB, x.OriginalTVDB, x.OriginalMeta)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	stmt, err := prepareSQLExecutor(ctx, q, `UPDATE media SET meta_json=? WHERE id=? AND COALESCE(meta_json,'')=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range x.Siblings {
		r, err = stmt.ExecContext(ctx, v.DesiredMeta, v.ID, v.OriginalMeta)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return sql.ErrNoRows
		}
	}
	return nil
}

type sqlPreparer interface {
	PrepareContext(context.Context, string) (*sql.Stmt, error)
}

func prepareSQLExecutor(ctx context.Context, q store.SQLExecutor, query string) (*sql.Stmt, error) {
	p, ok := q.(sqlPreparer)
	if !ok {
		return nil, errors.New("sql executor does not support prepared statements")
	}
	return p.PrepareContext(ctx, query)
}
