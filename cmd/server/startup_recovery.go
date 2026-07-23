package main

import (
	"context"
	"database/sql"
	"fmt"

	"knox-media/internal/metadatalib"
	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

// recoverStartupTasks performs durable queue recovery before workers claim work.
func recoverStartupTasks(ctx context.Context, db *sql.DB, postIngest *postingest.Queue, roots ...postingest.ThumbnailRecoveryRoots) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil || postIngest == nil {
		return fmt.Errorf("startup recovery: database and post-ingest queue are required")
	}
	metadataRoot := ""
	if len(roots) > 1 {
		metadataRoot = roots[1].Preview
	}
	if _, err := metadatalib.ReconcileScrapeArtworkStages(ctx, db, metadataRoot, 100); err != nil {
		return fmt.Errorf("startup recovery: scrape artwork stages: %w", err)
	}
	if _, _, err := postingest.ReconcileThumbnailStages(ctx, db, firstRoots(roots), 100); err != nil {
		return fmt.Errorf("startup recovery: thumbnail stages: %w", err)
	}
	store.ResetInterruptedTasks(db)
	if _, err := postIngest.RecoverExpired(ctx); err != nil {
		return fmt.Errorf("startup recovery: post-ingest: %w", err)
	}
	return nil
}

func firstRoots(v []postingest.ThumbnailRecoveryRoots) postingest.ThumbnailRecoveryRoots {
	if len(v) > 0 {
		return v[0]
	}
	return postingest.ThumbnailRecoveryRoots{}
}
