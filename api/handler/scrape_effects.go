package handler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"knox-media/internal/caststore"
	"knox-media/internal/metadatalib"
	"knox-media/internal/publication"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
	"sort"
	"strings"
)

type scrapeCompletionEffects struct {
	PosterFallback bool
	Credits        []scraper.CreditMember
	AvatarBaseURL  string
	LibraryID      int64
	Artwork        metadatalib.StagedScrapeArtwork
	BeforeTerminal func() error
	PreparedSeries *PreparedSeriesEffects
}

func applyScrapeCompletionEffectsTx(ctx context.Context, tx store.SQLExecutor, c scrapeClaim, result *scraper.ScrapeResult, e scrapeCompletionEffects) (string, error) {
	if len(e.Artwork.Images) > 0 {
		if err := metadatalib.VerifyStagedScrapeArtwork(e.Artwork); err != nil {
			return "", err
		}
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM media_asset_stage_journal WHERE stage_id=? AND media_id=? AND run_id=? AND step_id=? AND generation=? AND owner_token=? AND scrape_task_id=? AND scrape_attempt=? AND scrape_retry_round=? AND artifact_kind='scrape_artwork' AND state='staged'`, e.Artwork.StageID, c.MediaID, c.RunID.Int64, c.StepID.Int64, c.Generation.Int64, c.Owner, c.ID, c.Attempts, c.RetryRound).Scan(&one); err != nil {
			return "", err
		}
		metadatalib.SelectStagedScrapeArtwork(result, e.Artwork)
		raw, _ := json.Marshal(map[string]any{"producer": "scrape/stage-v1", "variants": e.Artwork.Images})
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,'scrape_artwork',?,?, 'scrape',CURRENT_TIMESTAMP,?)`, c.RunID.Int64, c.StepID.Int64, c.MediaID, c.Generation.Int64, "scrape_artwork:"+e.Artwork.StageID, string(raw), e.Artwork.StageID); err != nil {
			return "", err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE media_asset_stage_journal SET state='committed',updated_at=CURRENT_TIMESTAMP WHERE stage_id=? AND state='staged' AND scrape_task_id=? AND scrape_attempt=? AND scrape_retry_round=?`, e.Artwork.StageID, c.ID, c.Attempts, c.RetryRound)
		if err != nil {
			return "", err
		}
		if n, _ := updated.RowsAffected(); n != 1 {
			return "", ErrScrapeClaimLost
		}
	}
	if e.PreparedSeries != nil {
		if err := applyPreparedSeriesEffectsTx(ctx, tx, *e.PreparedSeries); err != nil {
			return "", err
		}
	} else if e.LibraryID > 0 {
		if err := syncSeriesCollectionMetaExecutor(ctx, tx, e.LibraryID, c.MediaID, result); err != nil {
			return "", err
		}
	}
	if len(e.Credits) > 0 {
		if _, err := caststore.ImportCreditsExecutor(ctx, tx, c.MediaID, e.Credits, e.AvatarBaseURL); err != nil {
			return "", err
		}
	}
	if e.PosterFallback && c.Generation.Valid {
		_, err := tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,max_attempts,last_error) VALUES(?,?,NULL,?,'poster_repair','waiting',?,'scrape_poster_repair') ON CONFLICT(media_id,generation,task_type) DO NOTHING`, c.MediaID, c.RunID.Int64, c.Generation.Int64, publication.DefaultMaxAttempts("poster_repair"))
		if err != nil {
			return "", err
		}
	}
	if e.BeforeTerminal != nil {
		return "", e.BeforeTerminal()
	}
	manifestRaw, digest, err := canonicalScrapeEffectManifest(c, result, e)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scrape_effect_commit(task_id,attempt,retry_round,generation,stage_id,manifest_json,manifest_digest) VALUES(?,?,?,?,?,?,?)`, c.ID, c.Attempts, c.RetryRound, c.Generation.Int64, e.Artwork.StageID, string(manifestRaw), digest)
	return digest, err
}

func syncSeriesCollectionMetaExecutor(ctx context.Context, q store.SQLExecutor, libraryID, mediaID int64, res *scraper.ScrapeResult) error {
	if q == nil || res == nil || libraryID <= 0 || mediaID <= 0 {
		return nil
	}
	var seriesID int64
	err := q.QueryRowContext(ctx, `
		SELECT sr.id FROM episode_media em
		JOIN episode ep ON ep.id = em.episode_id
		JOIN season se ON se.id = ep.season_id
		JOIN series sr ON sr.id = se.tv_id
		WHERE em.media_id = ? AND sr.library_id = ?
		LIMIT 1`, mediaID, libraryID).Scan(&seriesID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if seriesID <= 0 {
		return sql.ErrNoRows
	}
	var existingTitle sql.NullString
	var linkedEpisodeCount int64
	if err := q.QueryRowContext(ctx, `
		SELECT COALESCE(sr.title, ''),
			(SELECT COUNT(DISTINCT em2.media_id)
			 FROM season se2
			 JOIN episode ep2 ON ep2.season_id = se2.id
			 JOIN episode_media em2 ON em2.episode_id = ep2.id
			 WHERE se2.tv_id = sr.id)
		FROM series sr WHERE sr.id = ?`, seriesID).Scan(&existingTitle, &linkedEpisodeCount); err != nil {
		return err
	}

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
	result, err := q.ExecContext(ctx, `
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
		return err
	}
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
	type sibling struct {
		id   int64
		meta string
	}
	var siblings []sibling
	for rows.Next() {
		var v sibling
		if err := rows.Scan(&v.id, &v.meta); err != nil {
			_ = rows.Close()
			return err
		}
		if v.id <= 0 {
			_ = rows.Close()
			return sql.ErrNoRows
		}
		siblings = append(siblings, v)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, v := range siblings {
		merged, err := scraper.MergeSeriesFieldsPreservingEpisode(v.meta, sharedPatch)
		if err != nil {
			return err
		}
		if err := store.UpdateMediaMetaAndPhotoTime(ctx, q, v.id, merged); err != nil {
			return err
		}
	}
	return nil
}

func mustScrapeJSON(res *scraper.ScrapeResult) string {
	raw, _ := json.Marshal(map[string]any{"scrape": res})
	return string(raw)
}

func canonicalScrapeEffectManifest(c scrapeClaim, result *scraper.ScrapeResult, e scrapeCompletionEffects) ([]byte, string, error) {
	mediaRaw := mustScrapeJSON(result)
	mediaSum := sha256.Sum256([]byte(mediaRaw))
	creditKeys := make([]string, 0, len(e.Credits))
	for _, v := range e.Credits {
		creditKeys = append(creditKeys, strings.Join([]string{strings.TrimSpace(v.TMDBPersonID), strings.TrimSpace(v.Name), strings.TrimSpace(v.Occupation), strings.TrimSpace(v.CharacterName), strings.TrimSpace(v.RoleType), fmt.Sprint(v.SortOrder)}, "\x00"))
	}
	sort.Strings(creditKeys)
	creditSum := sha256.Sum256([]byte(strings.Join(creditKeys, "\n")))
	seriesSum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%x", e.LibraryID, c.MediaID, mediaSum)))
	raw, err := json.Marshal(map[string]any{"v": 2, "task": c.ID, "attempt": c.Attempts, "generation": c.Generation.Int64, "stage": e.Artwork.StageID, "media_sha256": hex.EncodeToString(mediaSum[:]), "series_sha256": hex.EncodeToString(seriesSum[:]), "credit_sha256": hex.EncodeToString(creditSum[:]), "credit_count": len(creditKeys), "repair": e.PosterFallback, "artwork_count": len(e.Artwork.Images)})
	if err != nil {
		return nil, "", err
	}
	if len(raw) > 64<<10 {
		return nil, "", fmt.Errorf("scrape effect manifest exceeds 64KiB")
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}
