package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"knox-media/internal/metadatalib"
	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

// recoverStartupTasks performs durable queue recovery before workers claim work.
type StartupRecoveryRoots struct {
	Encryption postingest.EncryptionRecoveryRoots

	Thumbnail     postingest.ThumbnailRecoveryRoots
	Poster        postingest.PosterRecoveryRoots
	ScrapeArtwork string
}

func recoverStartupArtifacts(ctx context.Context, db *sql.DB, roots StartupRecoveryRoots) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("startup recovery: database is required")
	}
	if _, _, err := postingest.ReconcileEncryptionStages(ctx, db, roots.Encryption, 100); err != nil {
		return fmt.Errorf("startup recovery: encryption stages: %w", err)
	}

	if _, err := metadatalib.ReconcileScrapeArtworkStages(ctx, db, roots.ScrapeArtwork, 100); err != nil {
		return fmt.Errorf("startup recovery: scrape artwork stages: %w", err)
	}
	if _, _, err := postingest.ReconcilePosterStages(ctx, db, roots.Poster, 100); err != nil {
		return fmt.Errorf("startup recovery: poster stages: %w", err)
	}
	if _, _, err := postingest.ReconcilePosterObjects(ctx, db, roots.Poster.Upload, 100, 2*time.Hour); err != nil {
		return fmt.Errorf("startup recovery: poster objects: %w", err)
	}
	if _, _, err := postingest.ReconcileThumbnailStages(ctx, db, roots.Thumbnail, 100); err != nil {
		return fmt.Errorf("startup recovery: thumbnail stages: %w", err)
	}
	return nil
}

func recoverStartupLeases(ctx context.Context, db *sql.DB, postIngest *postingest.Queue) error {
	if db == nil {
		return fmt.Errorf("startup recovery: database is required")
	}
	store.ResetInterruptedTasks(db)
	if _, err := postIngest.RecoverAllInterrupted(ctx); err != nil {
		return fmt.Errorf("startup recovery: post-ingest: %w", err)
	}
	return nil
}

func recoverStartupTasks(ctx context.Context, db *sql.DB, postIngest *postingest.Queue, roots StartupRecoveryRoots) error {
	if err := recoverStartupArtifacts(ctx, db, roots); err != nil {
		return err
	}
	return recoverStartupLeases(ctx, db, postIngest)
}
