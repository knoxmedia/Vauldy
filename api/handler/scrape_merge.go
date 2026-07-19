package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"knox-media/internal/relationshipsync"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
)

func cloneScrapeResult(res *scraper.ScrapeResult) (*scraper.ScrapeResult, error) {
	if res == nil {
		return nil, fmt.Errorf("scrape result is required")
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var cloned scraper.ScrapeResult
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

// mergeScrapeResultTx merges against the latest media metadata and atomically updates title and meta.
func (h *Handler) mergeScrapeResultTx(ctx context.Context, mediaID int64, res *scraper.ScrapeResult, title string) (merged string, committed *scraper.ScrapeResult, err error) {
	if h == nil || h.App == nil || h.App.DB == nil {
		return "", nil, fmt.Errorf("scrape merge database is not configured")
	}
	if mediaID <= 0 {
		return "", nil, fmt.Errorf("invalid media id")
	}
	source, err := cloneScrapeResult(res)
	if err != nil {
		return "", nil, err
	}
	err = store.WithBusyRetry(ctx, nil, func() error {
		tx, err := h.App.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var current sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&current); err != nil {
			return err
		}
		candidate, err := cloneScrapeResult(source)
		if err != nil {
			return err
		}
		scraper.PreserveScrapeImagesFromExisting(candidate, current.String)
		var root map[string]any
		_ = json.Unmarshal([]byte(current.String), &root)
		existingExtra := map[string]any{}
		if scrapeMap, ok := root["scrape"].(map[string]any); ok {
			if extra, ok := scrapeMap["extra"].(map[string]any); ok {
				for key, value := range extra {
					existingExtra[key] = value
				}
			}
		}
		for key, value := range candidate.Extra {
			existingExtra[key] = value
		}
		candidate.Extra = existingExtra
		next, err := scraper.MergeMetaJSON(current.String, map[string]any{"scrape": candidate})
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE media SET title=? WHERE id=?`, title, mediaID)
		if err != nil {
			return err
		}
		err = store.UpdateMediaMetaAndPhotoTime(ctx, tx, mediaID, next)
		if err != nil {
			return err
		}
		if err = relationshipsync.SyncTx(ctx, tx, mediaID); err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return sql.ErrNoRows
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		merged, committed = next, candidate
		return nil
	})
	return merged, committed, err
}

func updateMediaTitleAndMetaTx(ctx context.Context, db *sql.DB, mediaID int64, title, metaJSON string) error {
	return store.WithBusyRetry(ctx, nil, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if title != "" {
			res, err := tx.ExecContext(ctx, `UPDATE media SET title=? WHERE id=?`, title, mediaID)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n != 1 {
				return sql.ErrNoRows
			}
		}
		if err := store.UpdateMediaMetaAndPhotoTime(ctx, tx, mediaID, metaJSON); err != nil {
			return err
		}
		if err := relationshipsync.SyncTx(ctx, tx, mediaID); err != nil {
			return err
		}
		return tx.Commit()
	})
}
