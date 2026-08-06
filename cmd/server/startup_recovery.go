package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"knox-media/internal/metadatalib"
	"knox-media/internal/postingest"
	"knox-media/internal/retirement"
	"knox-media/internal/store"
	"knox-media/internal/taskalign"
)

// recoverStartupTasks performs durable queue recovery before workers claim work.
type StartupRecoveryRoots struct {
	Encryption postingest.EncryptionRecoveryRoots

	Thumbnail     postingest.ThumbnailRecoveryRoots
	Poster        postingest.PosterRecoveryRoots
	ScrapeArtwork string
	Retirement    retirement.RecoveryOptions
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
	if err := recoverStartupRetirement(ctx, db, roots.Retirement); err != nil {
		return err
	}
	return nil
}

func recoverStartupRetirement(ctx context.Context, db *sql.DB, opts retirement.RecoveryOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("startup recovery: database is required")
	}
	rctx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		rctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	if err := retirement.ReconcileStartup(rctx, db, opts); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("startup recovery: retirement reconcile exceeded %s; continuing startup", opts.Timeout)
			return nil
		}
		return fmt.Errorf("startup recovery: retirement: %w", err)
	}
	return nil
}

func recoverStartupLeases(ctx context.Context, db *sql.DB, postIngest *postingest.Queue) error {
	if db == nil {
		return fmt.Errorf("startup recovery: database is required")
	}
	store.ResetInterruptedTasks(db)
	recovered, err := postIngest.RecoverAllInterrupted(ctx)
	if err != nil {
		return fmt.Errorf("startup recovery: post-ingest: %w", err)
	}
	log.Printf("startup recovery: recovered %d interrupted post-ingest task(s)", recovered)
	return nil
}

func recoverStartupDomainAlignment(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("startup recovery: database is required")
	}
	result, err := taskalign.BackfillMissingDomainTasks(ctx, db)
	if err != nil {
		return fmt.Errorf("startup recovery: domain task backfill: %w", err)
	}
	if result.Created > 0 {
		log.Printf("startup recovery: domain task backfill created=%d by_type=%v", result.Created, result.ByType)
	}
	return nil
}

func recoverStartupTasks(ctx context.Context, db *sql.DB, postIngest *postingest.Queue, roots StartupRecoveryRoots) error {
	if err := recoverStartupArtifacts(ctx, db, roots); err != nil {
		return err
	}
	if err := recoverStartupLeases(ctx, db, postIngest); err != nil {
		return err
	}
	return recoverStartupDomainAlignment(ctx, db)
}
