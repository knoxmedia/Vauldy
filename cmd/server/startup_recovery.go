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
type StartupRecoveryRoots struct {
	Thumbnail     postingest.ThumbnailRecoveryRoots
	Poster        postingest.PosterRecoveryRoots
	ScrapeArtwork string
}

func recoverStartupTasks(ctx context.Context, db *sql.DB, postIngest *postingest.Queue, roots StartupRecoveryRoots) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil || postIngest == nil {
		return fmt.Errorf("startup recovery: database and post-ingest queue are required")
	}
	if _, err := metadatalib.ReconcileScrapeArtworkStages(ctx, db, roots.ScrapeArtwork, 100); err != nil {
		return fmt.Errorf("startup recovery: scrape artwork stages: %w", err)
	}
	if _, _, err := postingest.ReconcilePosterStages(ctx, db, roots.Poster, 100); err != nil {
		return fmt.Errorf("startup recovery: poster stages: %w", err)
	}
	if _, _, err := postingest.ReconcileThumbnailStages(ctx, db, roots.Thumbnail, 100); err != nil {
		return fmt.Errorf("startup recovery: thumbnail stages: %w", err)
	}
	store.ResetInterruptedTasks(db)
	if _, err := postIngest.RecoverExpired(ctx); err != nil {
		return fmt.Errorf("startup recovery: post-ingest: %w", err)
	}
	return nil
}
