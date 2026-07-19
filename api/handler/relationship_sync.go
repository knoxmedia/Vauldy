package handler

import (
	"context"
	"database/sql"
	"knox-media/internal/relationshipsync"
	"knox-media/internal/store"
)

func (h *Handler) syncMediaRelationship(ctx context.Context, mediaID int64) error {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 {
		return nil
	}
	return store.WithBusyRetry(ctx, nil, func() error {
		tx, err := h.App.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = relationshipsync.SyncTx(ctx, tx, mediaID); err != nil {
			return err
		}
		return tx.Commit()
	})
}
func pruneRelationshipShells(ctx context.Context, tx *sql.Tx, episodeID, albumID int64) error {
	return nil
}
