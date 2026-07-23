package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"knox-media/internal/caststore"
	"knox-media/internal/metadatalib"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
	"log"
	"strings"
)

type scrapeCompletionEffects struct {
	PosterFallback bool
	Credits        []scraper.CreditMember
	AvatarBaseURL  string
	LibraryID      int64
	Artwork        metadatalib.StagedScrapeArtwork
	BeforeTerminal func() error
}

func applyScrapeCompletionEffectsTx(ctx context.Context, tx store.SQLExecutor, c scrapeClaim, result *scraper.ScrapeResult, e scrapeCompletionEffects) error {
	if len(e.Artwork.Images) > 0 {
		if err := metadatalib.VerifyStagedScrapeArtwork(e.Artwork); err != nil {
			return err
		}
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM media_asset_stage_journal WHERE stage_id=? AND media_id=? AND run_id=? AND step_id=? AND generation=? AND owner_token=? AND artifact_kind='scrape_artwork' AND state='staged'`, e.Artwork.StageID, c.MediaID, c.RunID.Int64, c.StepID.Int64, c.Generation.Int64, c.Owner).Scan(&one); err != nil {
			return err
		}
		metadatalib.SelectStagedScrapeArtwork(result, e.Artwork)
		raw, _ := json.Marshal(map[string]any{"producer": "scrape/stage-v1", "variants": e.Artwork.Images})
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,'scrape_artwork',?,?, 'scrape',CURRENT_TIMESTAMP,?)`, c.RunID.Int64, c.StepID.Int64, c.MediaID, c.Generation.Int64, "scrape_artwork:"+e.Artwork.StageID, string(raw), e.Artwork.StageID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='committed',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged'`, e.Artwork.StageID); err != nil {
			return err
		}
	}
	if e.LibraryID > 0 {
		if err := syncSeriesCollectionMetaExecutor(ctx, tx, e.LibraryID, c.MediaID, result); err != nil {
			return err
		}
	}
	if len(e.Credits) > 0 {
		if _, err := caststore.ImportCreditsExecutor(ctx, tx, c.MediaID, e.Credits, e.AvatarBaseURL); err != nil {
			return err
		}
	}
	if e.PosterFallback && c.Generation.Valid {
		_, err := tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,last_error) VALUES(?,?,NULL,?,'poster_repair','waiting','scrape_poster_repair') ON CONFLICT(media_id,generation,task_type) DO NOTHING`, c.MediaID, c.RunID.Int64, c.Generation.Int64)
		if err != nil {
			return err
		}
	}
	if e.BeforeTerminal != nil {
		return e.BeforeTerminal()
	}
	return nil
}

func syncSeriesCollectionMetaExecutor(ctx context.Context, q store.SQLExecutor, libraryID, mediaID int64, res *scraper.ScrapeResult) error {
	if q == nil || res == nil || libraryID <= 0 || mediaID <= 0 {
		return nil
	}
	var seriesID int64
	if err := q.QueryRowContext(ctx, `
		SELECT sr.id FROM episode_media em
		JOIN episode ep ON ep.id = em.episode_id
		JOIN season se ON se.id = ep.season_id
		JOIN series sr ON sr.id = se.tv_id
		WHERE em.media_id = ? AND sr.library_id = ?
		LIMIT 1`, mediaID, libraryID).Scan(&seriesID); err != nil || seriesID <= 0 {
		return nil
	}
	var existingTitle sql.NullString
	var linkedEpisodeCount int64
	_ = q.QueryRowContext(ctx, `
		SELECT COALESCE(sr.title, ''),
			(SELECT COUNT(DISTINCT em2.media_id)
			 FROM season se2
			 JOIN episode ep2 ON ep2.season_id = se2.id
			 JOIN episode_media em2 ON em2.episode_id = ep2.id
			 WHERE se2.tv_id = sr.id)
		FROM series sr WHERE sr.id = ?`, seriesID).Scan(&existingTitle, &linkedEpisodeCount)

	seriesTitle := res.Title
	seriesOverview := res.Overview
	seriesPoster := res.Poster
	seriesBackdrop := res.Backdrop
	tmdbID := ""
	tvdbID := ""
	if res.Extra != nil {
		if v := stringScrapeField(res.Extra["series_title"]); v != "" {
			seriesTitle = v
		}
		if v := stringScrapeField(res.Extra["series_overview"]); v != "" {
			seriesOverview = v
		}
		if v := stringScrapeField(res.Extra["series_poster"]); v != "" {
			seriesPoster = v
		}
		if v := stringScrapeField(res.Extra["series_backdrop"]); v != "" {
			seriesBackdrop = v
		}
		tmdbID = stringScrapeField(res.Extra["tmdb_id"])
		tvdbID = stringScrapeField(res.Extra["tvdb_id"])
	}
	preserveTitle := shouldPreserveSeriesTitle(existingTitle.String, linkedEpisodeCount)
	if preserveTitle {
		seriesTitle = strings.TrimSpace(existingTitle.String)
	}
	seriesMeta, _ := json.Marshal(map[string]any{
		"scrape": map[string]any{
			"title":        seriesTitle,
			"overview":     seriesOverview,
			"poster":       seriesPoster,
			"backdrop":     seriesBackdrop,
			"source":       res.Source,
			"release_date": res.ReleaseDate,
			"rating":       res.Rating,
			"genres":       res.Genres,
			"extra": map[string]any{
				"tmdb_id":   tmdbID,
				"tmdb_type": "tv",
				"tvdb_id":   tvdbID,
			},
		},
	})
	_, _ = q.ExecContext(ctx, `
		UPDATE series SET
			title = CASE WHEN ? THEN title WHEN ? != '' THEN ? ELSE title END,
			poster = COALESCE(NULLIF(?, ''), poster),
			tmdb_id = COALESCE(NULLIF(tmdb_id, ''), NULLIF(?, '')),
			tvdb_id = COALESCE(NULLIF(tvdb_id, ''), NULLIF(?, '')),
			meta_json = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		preserveTitle, seriesTitle, seriesTitle, seriesPoster, tmdbID, tvdbID, string(seriesMeta), seriesID,
	)
	// Propagate shared show fields to sibling episodes without overwriting episode titles/posters.
	rows, err := q.QueryContext(ctx, `
		SELECT m.id, COALESCE(m.meta_json, '')
		FROM episode_media em
		JOIN episode ep ON ep.id = em.episode_id
		JOIN season se ON se.id = ep.season_id
		JOIN media m ON m.id = em.media_id
		WHERE se.tv_id = ? AND m.id != ?
	`, seriesID, mediaID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	sharedPatch := map[string]any{
		"scrape": map[string]any{
			"series_title":    seriesTitle,
			"series_overview": seriesOverview,
			"series_poster":   seriesPoster,
			"series_backdrop": seriesBackdrop,
			"extra": map[string]any{
				"series_title":    seriesTitle,
				"series_overview": seriesOverview,
				"series_poster":   seriesPoster,
				"series_backdrop": seriesBackdrop,
				"tmdb_id":         tmdbID,
				"tmdb_type":       "tv",
				"tvdb_id":         tvdbID,
			},
		},
	}
	for rows.Next() {
		var siblingID int64
		var siblingMeta string
		if rows.Scan(&siblingID, &siblingMeta) != nil || siblingID <= 0 {
			continue
		}
		merged, mErr := scraper.MergeMetaJSON(siblingMeta, sharedPatch)
		if mErr != nil {
			continue
		}
		if err := store.UpdateMediaMetaAndPhotoTime(ctx, q, siblingID, merged); err != nil {
			log.Printf("series sibling metadata media=%d: %v", siblingID, err)
		}
	}

	return nil
}

func mustScrapeJSON(res *scraper.ScrapeResult) string {
	raw, _ := json.Marshal(map[string]any{"scrape": res})
	return string(raw)
}
