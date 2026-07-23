package handler

import (
	"context"
	"encoding/json"
	"knox-media/internal/metadatalib"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
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
		metadatalib.SelectStagedScrapeArtwork(result, e.Artwork)
		raw, _ := json.Marshal(e.Artwork.Images)
		_, err := tx.ExecContext(ctx, `INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','committed',?,?) ON CONFLICT(stage_id) DO NOTHING`, e.Artwork.StageID, c.MediaID, c.RunID.Int64, c.StepID.Int64, c.Generation.Int64, c.Owner, "scrape_artwork", e.Artwork.Images[0].Path, string(raw))
		if err != nil {
			return err
		}
	}
	if e.LibraryID > 0 {
		if err := syncSeriesCollectionMetaExecutor(ctx, tx, e.LibraryID, c.MediaID, result); err != nil {
			return err
		}
	}
	if len(e.Credits) > 0 {
		if _, err := importCreditsExecutor(ctx, tx, c.MediaID, e.Credits, e.AvatarBaseURL); err != nil {
			return err
		}
	}
	if e.PosterFallback && c.Generation.Valid {
		_, err := tx.ExecContext(ctx, `INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,last_error) VALUES(?,?,NULL,?,'poster','waiting','scrape_poster_repair') ON CONFLICT(media_id,generation,task_type) DO NOTHING`, c.MediaID, c.RunID.Int64, c.Generation.Int64)
		if err != nil {
			return err
		}
	}
	if e.BeforeTerminal != nil {
		return e.BeforeTerminal()
	}
	return nil
}

func importCreditsExecutor(ctx context.Context, db store.SQLExecutor, mediaID int64, credits []scraper.CreditMember, avatarBase string) (int, error) {
	n := 0
	for _, c := range credits {
		if c.Name == "" {
			continue
		}
		var id int64
		_ = db.QueryRowContext(ctx, `SELECT id FROM cast_person WHERE tmdb_id=? AND deleted_at IS NULL LIMIT 1`, c.TMDBPersonID).Scan(&id)
		if id == 0 {
			r, e := db.ExecContext(ctx, `INSERT INTO cast_person(name,name_norm,tmdb_id,occupation_json) VALUES(?,lower(?),NULLIF(?,''),'[]')`, c.Name, c.Name, c.TMDBPersonID)
			if e != nil {
				return n, e
			}
			id, _ = r.LastInsertId()
		}
		occ := c.Occupation
		if occ == "" {
			occ = "actor"
		}
		_, e := db.ExecContext(ctx, `INSERT INTO media_person(media_id,person_id,occupation,character_name,role_type,sort_order) VALUES(?,?,?,?,?,?) ON CONFLICT(media_id,person_id,occupation) DO UPDATE SET character_name=excluded.character_name,role_type=excluded.role_type,sort_order=excluded.sort_order,updated_at=CURRENT_TIMESTAMP`, mediaID, id, occ, c.CharacterName, c.RoleType, c.SortOrder)
		if e != nil {
			return n, e
		}
		n++
	}
	return n, nil
}

func syncSeriesCollectionMetaExecutor(ctx context.Context, q store.SQLExecutor, libraryID, mediaID int64, res *scraper.ScrapeResult) error {
	if q == nil || res == nil {
		return nil
	}
	var seriesID int64
	if err := q.QueryRowContext(ctx, `SELECT sr.id FROM episode_media em JOIN episode ep ON ep.id=em.episode_id JOIN season se ON se.id=ep.season_id JOIN series sr ON sr.id=se.tv_id WHERE em.media_id=? AND sr.library_id=? LIMIT 1`, mediaID, libraryID).Scan(&seriesID); err != nil {
		if false {
			return nil
		}
		return nil
	}
	_, err := q.ExecContext(ctx, `UPDATE series SET title=CASE WHEN ?!='' THEN ? ELSE title END,poster=COALESCE(NULLIF(?,''),poster),updated_at=CURRENT_TIMESTAMP WHERE id=?`, res.Title, res.Title, res.Poster, seriesID)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT m.id,COALESCE(m.meta_json,'') FROM episode_media em JOIN episode ep ON ep.id=em.episode_id JOIN season se ON se.id=ep.season_id JOIN media m ON m.id=em.media_id WHERE se.tv_id=? AND m.id!=?`, seriesID, mediaID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var meta string
		if rows.Scan(&id, &meta) != nil {
			continue
		}
		merged, e := scraper.MergeMetaJSON(meta, map[string]any{"scrape": map[string]any{"series_title": res.Title, "series_overview": res.Overview, "series_poster": res.Poster, "series_backdrop": res.Backdrop}})
		if e != nil {
			return e
		}
		if e = store.UpdateMediaMetaAndPhotoTime(ctx, q, id, merged); e != nil {
			return e
		}
	}
	return rows.Err()
}

func mustScrapeJSON(res *scraper.ScrapeResult) string {
	raw, _ := json.Marshal(map[string]any{"scrape": res})
	return string(raw)
}
