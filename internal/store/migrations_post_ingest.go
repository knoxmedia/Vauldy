package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const postIngestTaskTable = "post_ingest_task"

var postIngestTaskCreateSQL = `
CREATE TABLE post_ingest_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    scan_task_id INTEGER,
    task_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'waiting',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner TEXT,
    lease_until TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    FOREIGN KEY (scan_task_id) REFERENCES scan_task(id) ON DELETE SET NULL,
    UNIQUE(media_id, task_type),
    CHECK (task_type IN ('poster','preview','keyframe','subtitle','atrack','encrypt')),
    CHECK (status IN ('waiting','running','done','failed','cancelled'))
)`

var postIngestTaskIndexStatements = []string{
	`CREATE INDEX IF NOT EXISTS idx_post_ingest_claim ON post_ingest_task(status, available_at, lease_until, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_post_ingest_scan ON post_ingest_task(scan_task_id, status)`,
}

func postIngestTaskSchemaAllowsEncrypt(ctx context.Context, db *sql.DB) (bool, error) {
	var tableSQL sql.NullString
	err := db.QueryRowContext(ctx, `
SELECT sql FROM sqlite_master WHERE type='table' AND name=? COLLATE NOCASE`, postIngestTaskTable).Scan(&tableSQL)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !tableSQL.Valid || strings.TrimSpace(tableSQL.String) == "" {
		return false, fmt.Errorf("post_ingest_task exists without schema SQL")
	}
	return strings.Contains(strings.ToLower(tableSQL.String), "'encrypt'"), nil
}

// MigratePostIngestTaskEncryptType rebuilds post_ingest_task when legacy CHECK
// constraints omitted the encrypt task type.
func MigratePostIngestTaskEncryptType(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	allows, err := postIngestTaskSchemaAllowsEncrypt(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect post_ingest_task schema: %w", err)
	}
	if allows {
		return nil
	}
	return WithBusyRetry(ctx, nil, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		if _, err := tx.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, strings.Replace(postIngestTaskCreateSQL, postIngestTaskTable, postIngestTaskTable+"__new", 1)); err != nil {
			return fmt.Errorf("create rebuilt post_ingest_task: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO post_ingest_task__new (
    id, media_id, scan_task_id, task_type, status, attempts, max_attempts,
    available_at, lease_owner, lease_until, last_error, created_at, updated_at,
    started_at, finished_at
)
SELECT
    id, media_id, scan_task_id, task_type, status, attempts, max_attempts,
    available_at, lease_owner, lease_until, last_error, created_at, updated_at,
    started_at, finished_at
FROM post_ingest_task`); err != nil {
			return fmt.Errorf("copy post_ingest_task rows: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE post_ingest_task`); err != nil {
			return fmt.Errorf("drop legacy post_ingest_task: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE post_ingest_task__new RENAME TO post_ingest_task`); err != nil {
			return fmt.Errorf("rename rebuilt post_ingest_task: %w", err)
		}
		for _, stmt := range postIngestTaskIndexStatements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("recreate post_ingest_task index: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
}
