package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const canonicalIngestItemSchema = `CREATE TABLE ingest_item (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    submission_key TEXT NOT NULL,
    source TEXT NOT NULL CHECK(source IN ('filesystem_event','upload')),
    library_id INTEGER NOT NULL,
    canonical_path TEXT NOT NULL CHECK(length(trim(canonical_path))>0),
    path_key TEXT NOT NULL CHECK(length(trim(path_key))>0),
    upload_id TEXT,
    size_bytes INTEGER CHECK(size_bytes IS NULL OR size_bytes>=0),
    mtime_ns INTEGER,
    sha256 TEXT CHECK(sha256 IS NULL OR (length(sha256)=64 AND sha256 NOT GLOB '*[^0-9a-f]*')),
    state TEXT NOT NULL DEFAULT 'waiting' CHECK(state IN ('waiting','running','done','failed','superseded')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
    retry_round INTEGER NOT NULL DEFAULT 0 CHECK(retry_round>=0),
    available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner TEXT,
    lease_until TIMESTAMP,
    superseded_owner TEXT,
    superseded_lease_until TIMESTAMP,
    transition_token TEXT CHECK(transition_token IS NULL OR (length(transition_token)=32 AND transition_token NOT GLOB '*[^0-9a-f]*')),
    expected_generation INTEGER NOT NULL DEFAULT 0 CHECK(expected_generation>=0),
    media_generation INTEGER CHECK(media_generation IS NULL OR media_generation>0),
    media_id INTEGER,
    ingest_run_id INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    UNIQUE(submission_key),
    CHECK((state='running' AND lease_owner IS NOT NULL AND length(trim(lease_owner))>0 AND lease_until IS NOT NULL) OR (state<>'running' AND lease_owner IS NULL AND lease_until IS NULL)),
    CHECK(source='upload' OR upload_id IS NULL),
    FOREIGN KEY(library_id) REFERENCES library(id) ON DELETE RESTRICT,
    FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE SET NULL,
    FOREIGN KEY(ingest_run_id) REFERENCES media_ingest_run(id) ON DELETE SET NULL
)`

var task1IngestItemSchema = strings.Replace(strings.Replace(strings.Replace(canonicalIngestItemSchema,
	"    superseded_owner TEXT,\n", "", 1),
	"    superseded_lease_until TIMESTAMP,\n", "", 1),
	"    transition_token TEXT CHECK(transition_token IS NULL OR (length(transition_token)=32 AND transition_token NOT GLOB '*[^0-9a-f]*')),\n", "", 1)

func task3AlteredIngestItemSchema() string {
	return strings.Replace(task1IngestItemSchema,
		"    finished_at TIMESTAMP,\n",
		"    finished_at TIMESTAMP, superseded_owner TEXT, superseded_lease_until TIMESTAMP, transition_token TEXT CHECK(transition_token IS NULL OR (length(transition_token)=32 AND transition_token NOT GLOB '*[^0-9a-f]*')),\n", 1)
}
func upgradeTask1IngestItem(ctx context.Context, tx SQLExecutor, stored string) (bool, error) {
	if normalizeSQLiteStoredSQL(stored) != normalizeSQLiteStoredSQL(task1IngestItemSchema) {
		return false, nil
	}
	for _, stmt := range []string{
		`ALTER TABLE ingest_item ADD COLUMN superseded_owner TEXT`,
		`ALTER TABLE ingest_item ADD COLUMN superseded_lease_until TIMESTAMP`,
		`ALTER TABLE ingest_item ADD COLUMN transition_token TEXT CHECK(transition_token IS NULL OR (length(transition_token)=32 AND transition_token NOT GLOB '*[^0-9a-f]*'))`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return true, err
		}
	}
	return true, nil
}

const canonicalFilesystemEventInboxSchema = `CREATE TABLE filesystem_event_inbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER NOT NULL,
    raw_path TEXT NOT NULL,
    event_ops TEXT NOT NULL,
    observed_at TIMESTAMP NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','consumed','ignored')),
    available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
    last_error TEXT NOT NULL DEFAULT '',
    consumed_ingest_item_id INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK((status='consumed')=(consumed_ingest_item_id IS NOT NULL)),
    FOREIGN KEY(library_id) REFERENCES library(id) ON DELETE RESTRICT,
    FOREIGN KEY(consumed_ingest_item_id) REFERENCES ingest_item(id) ON DELETE RESTRICT
)`

var ingestEntryManagedIndexes = map[string]map[string]string{
	"ingest_item": {
		"idx_ingest_item_active_path": `CREATE UNIQUE INDEX idx_ingest_item_active_path ON ingest_item(library_id,path_key) WHERE state IN ('waiting','running')`,
		"idx_ingest_item_claim":       `CREATE INDEX idx_ingest_item_claim ON ingest_item(state,available_at,lease_until,id)`,
		"idx_ingest_item_media":       `CREATE INDEX idx_ingest_item_media ON ingest_item(media_id,media_generation)`,
	},
	"filesystem_event_inbox": {
		"idx_filesystem_event_inbox_consume": `CREATE INDEX idx_filesystem_event_inbox_consume ON filesystem_event_inbox(status,available_at,observed_at,id)`,
		"idx_filesystem_event_inbox_item":    `CREATE INDEX idx_filesystem_event_inbox_item ON filesystem_event_inbox(consumed_ingest_item_id)`,
	},
}

var ingestEntryMigrationHook = func(string) error { return nil }

func migrateIngestEntry(ctx context.Context, db *sql.DB) (err error) {
	publicationMigrationMu.Lock()
	defer publicationMigrationMu.Unlock()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("ingest entry begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()
	for _, spec := range []struct{ name, ddl string }{{"ingest_item", canonicalIngestItemSchema}, {"filesystem_event_inbox", canonicalFilesystemEventInboxSchema}} {
		var stored string
		scanErr := conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, spec.name).Scan(&stored)
		switch scanErr {
		case sql.ErrNoRows:
			if _, err = conn.ExecContext(ctx, spec.ddl); err != nil {
				return fmt.Errorf("create %s: %w", spec.name, err)
			}
		case nil:
			if normalizeSQLiteStoredSQL(stored) != normalizeSQLiteStoredSQL(spec.ddl) && !(spec.name == "ingest_item" && normalizeSQLiteStoredSQL(stored) == normalizeSQLiteStoredSQL(task3AlteredIngestItemSchema())) {
				if spec.name == "ingest_item" {
					upgraded, upgradeErr := upgradeTask1IngestItem(ctx, conn, stored)
					if upgradeErr != nil {
						return fmt.Errorf("upgrade ingest_item: %w", upgradeErr)
					}
					if upgraded {
						break
					}
				}
				return fmt.Errorf("incompatible %s: stored SQL drift", spec.name)
			}
		default:
			return scanErr
		}
	}
	for table, indexes := range ingestEntryManagedIndexes {
		for name, ddl := range indexes {
			var stored sql.NullString
			scanErr := conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&stored)
			if scanErr == sql.ErrNoRows {
				if _, err = conn.ExecContext(ctx, ddl); err != nil {
					return fmt.Errorf("create %s: %w", name, err)
				}
			} else if scanErr != nil {
				return scanErr
			} else if normalizeSQLiteStoredSQL(stored.String) != normalizeSQLiteStoredSQL(ddl) {
				return fmt.Errorf("incompatible index %s on %s: stored SQL drift", name, table)
			}
		}
	}
	if err = ingestEntryMigrationHook("after-create"); err != nil {
		return err
	}
	if err = foreignKeyCheckExecutor(ctx, conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("ingest entry commit: %w", err)
	}
	committed = true
	return nil
}

// normalizeSQLiteStoredSQL removes only storage-irrelevant outer whitespace and
// an optional trailing semicolon. Internal bytes remain exact, so clause order,
// constraint spelling, case, and formatting drift are all rejected.
func normalizeSQLiteStoredSQL(s string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";"))
}
