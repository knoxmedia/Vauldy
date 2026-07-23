package main

import (
	"context"
	"database/sql"
	"fmt"

	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

// recoverStartupTasks performs durable queue recovery before workers claim work.
func recoverStartupTasks(ctx context.Context, db *sql.DB, postIngest *postingest.Queue) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil || postIngest == nil {
		return fmt.Errorf("startup recovery: database and post-ingest queue are required")
	}
	if _, _, err := postingest.ReconcileThumbnailStages(ctx, db, 100); err != nil {
		return fmt.Errorf("startup recovery: thumbnail stages: %w", err)
	}
	store.ResetInterruptedTasks(db)
	if _, err := postIngest.RecoverExpired(ctx); err != nil {
		return fmt.Errorf("startup recovery: post-ingest: %w", err)
	}
	return nil
}
