package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// migrateCommunityTranscodeTaskColumns backfills the transcode_task linkage
// columns that main's publication-v2 code assumes. In the commercial build
// these columns are added by internal/store/migrations_pretranscode.go and by
// rebuildPublicationTranscodeTask (both excluded from the community build).
// The community build keeps the columns so post-ingest/prepare query paths
// that reference them work against existing commercial databases and fresh
// community databases alike, while the pretranscode_* tables themselves are
// never created.
func migrateCommunityTranscodeTaskColumns(ctx context.Context, db *sql.DB) error {
	columns := []string{
		`task_type TEXT NOT NULL DEFAULT 'batch'`,
		`started_at TIMESTAMP`,
		`completed_at TIMESTAMP`,
		`preset_id INTEGER`,
		`ingest_run_id INTEGER`,
		`ingest_step_id INTEGER`,
		`generation INTEGER`,
		`retry_round INTEGER NOT NULL DEFAULT 0`,
		`media_id INTEGER`,
		`lease_owner TEXT`,
		`lease_until TIMESTAMP`,
	}
	for _, def := range columns {
		name := strings.Fields(def)[0]
		if _, err := db.ExecContext(ctx, `ALTER TABLE transcode_task ADD COLUMN `+def); err != nil {
			if isDuplicateColumnError(err) {
				continue
			}
			return fmt.Errorf("transcode_task community column %s: %w", name, err)
		}
	}
	return nil
}
