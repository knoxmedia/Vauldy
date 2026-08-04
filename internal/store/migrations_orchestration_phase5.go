package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Phase 5 orchestration migration canonical schema for media_ingest_step.
const mediaIngestStepPhase5Schema = `CREATE TABLE media_ingest_step_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL,
    media_id INTEGER NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    step_type TEXT NOT NULL CHECK (step_type IN ('poster','scrape','preview','keyframe','subtitle','atrack','encrypt','prepare','thumbnail','package','pretranscode','metadata','media_visible','subtitle_extract','atrack_extract','subtitle_recognize','keyframe_extract','ai_analysis','lyric_recognize','audio_analysis','photo_classify','photo_geocode','photo_face','image_ocr','document_convert','document_fulltext','person_scrape','artwork_cover')),
    node_key TEXT NOT NULL DEFAULT '',
    capability_subtask TEXT,
    required INTEGER NOT NULL CHECK (required IN (0,1)),
    status TEXT NOT NULL CHECK (status IN ('waiting','running','done','skipped','failed','cancelled')),
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner TEXT,
    lease_until TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retry_round INTEGER NOT NULL DEFAULT 0 CHECK(retry_round >= 0),
    FOREIGN KEY (run_id) REFERENCES media_ingest_run(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id,generation) REFERENCES media_ingest_run(media_id,generation),
    FOREIGN KEY (run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation),
    UNIQUE(run_id,step_type),
    UNIQUE(id,media_id,generation)
)`

// Canonical DDL for new Phase 5 tables.
const documentArtifactSchema = `CREATE TABLE IF NOT EXISTS document_artifact (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    artifact_type TEXT NOT NULL,
    node_key TEXT NOT NULL,
    mime_type TEXT NOT NULL DEFAULT '',
    byte_size INTEGER NOT NULL DEFAULT 0,
    hash TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE
)`

const documentFulltextSchemaPhase5 = `CREATE TABLE IF NOT EXISTS document_fulltext (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    language TEXT NOT NULL DEFAULT 'eng',
    text_content TEXT NOT NULL DEFAULT '',
    text_size INTEGER NOT NULL DEFAULT 0,
    text_hash TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT 'native' CHECK (mode IN ('native','ocr','hybrid')),
    engine_version TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE
)`

const aiAnalysisResultSchema = `CREATE TABLE IF NOT EXISTS ai_analysis_result (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    capability TEXT NOT NULL,
    result_json TEXT NOT NULL DEFAULT '{}',
    model_name TEXT NOT NULL DEFAULT '',
    model_version TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE
)`

// statusMapPhase5 maps legacy status values to the normalized domain.
var statusMapPhase5 = map[string]string{
	"pending":    "waiting",
	"queued":     "waiting",
	"processing": "running",
	"success":    "done",
	"completed":  "done",
	"error":      "failed",
	"abandoned":  "failed",
}

// phase5CurrentDB returns true if the Phase 5 migration has already been applied.
func phase5CurrentDB(ctx context.Context, db *sql.DB) (bool, error) {
	cols, err := publicationColumns(ctx, db, "media_ingest_step")
	if err != nil {
		return false, err
	}
	if !cols["node_key"] || !cols["capability_subtask"] {
		return false, nil
	}
	for _, tbl := range []string{"document_artifact", "document_fulltext", "ai_analysis_result"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n == 0 {
			return false, err
		}
	}
	return true, nil
}

// migrateOrchestrationPhase5 applies the Policy V4 identity and normalized domain queues migration.
func migrateOrchestrationPhase5(ctx context.Context, db *sql.DB) error {
	current, err := phase5CurrentDB(ctx, db)
	if err != nil {
		return fmt.Errorf("phase5 current check: %w", err)
	}
	if !tableExists(ctx, db, "media_ingest_step") {
		// No step table, create tables anyway
		return ensurePhase5SupplementalTables(ctx, db)
	}
	if current {
		if err := ensurePhase5ClaimIndexes(ctx, db); err != nil {
			return err
		}
		return ensurePhase5SupplementalTables(ctx, db)
	}

	// Verify no unknown status values
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT status FROM media_ingest_step WHERE status NOT IN ('waiting','running','done','skipped','failed','cancelled')`)
	if err != nil {
		return fmt.Errorf("phase5 validate status: %w", err)
	}
	var badStatuses []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return fmt.Errorf("phase5 scan status: %w", err)
		}
		if _, ok := statusMapPhase5[s]; !ok {
			badStatuses = append(badStatuses, s)
		}
	}
	rows.Close()
	if len(badStatuses) > 0 {
		return fmt.Errorf("unknown step status values found: %v", badStatuses)
	}

	// Begin transaction for the rebuild
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("phase5 begin tx: %w", err)
	}
	defer tx.Rollback()

	// Map legacy statuses to normalized domain
	for old, new := range statusMapPhase5 {
		if _, err := tx.ExecContext(ctx, `UPDATE media_ingest_step SET status=? WHERE status=?`, new, old); err != nil {
			return fmt.Errorf("phase5 map status %s->%s: %w", old, new, err)
		}
	}

	// Rebuild media_ingest_step with node_key
	if err := rebuildMediaIngestStepPhase5(ctx, tx); err != nil {
		return fmt.Errorf("phase5 rebuild step: %w", err)
	}

	// Create supplemental tables
	if err := ensurePhase5SupplementalTablesTx(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("phase5 commit: %w", err)
	}

	return ensurePhase5ClaimIndexes(ctx, db)
}

func rebuildMediaIngestStepPhase5(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable fk: %w", err)
	}

	// Create new table
	if _, err := tx.ExecContext(ctx, mediaIngestStepPhase5Schema); err != nil {
		return fmt.Errorf("create new: %w", err)
	}

	// Get old column names
	oldCols, err := publicationColumns(ctx, tx, "media_ingest_step")
	if err != nil {
		return fmt.Errorf("old columns: %w", err)
	}

	// Count old rows
	var oldCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step`).Scan(&oldCount); err != nil {
		return fmt.Errorf("count old: %w", err)
	}

	// Build node_key: use COALESCE(node_key, step_type) for backfill
	nodeKeyExpr := "COALESCE(step_type,'unknown')"
	if oldCols["node_key"] {
		nodeKeyExpr = "COALESCE(node_key,step_type,'unknown')"
	}

	// Build capability_subtask: preserve if exists
	capExpr := "NULL"
	if oldCols["capability_subtask"] {
		capExpr = "capability_subtask"
	}

	insertSQL := fmt.Sprintf(`INSERT INTO media_ingest_step_new(id,run_id,media_id,generation,step_type,node_key,capability_subtask,required,status,attempts,max_attempts,available_at,lease_owner,lease_until,last_error,started_at,finished_at,created_at,updated_at,retry_round) SELECT id,run_id,media_id,generation,step_type,%s,%s,required,status,attempts,max_attempts,available_at,lease_owner,lease_until,last_error,started_at,finished_at,created_at,updated_at,retry_round FROM media_ingest_step`, nodeKeyExpr, capExpr)

	if _, err := tx.ExecContext(ctx, insertSQL); err != nil {
		return fmt.Errorf("insert new: %w", err)
	}

	// Verify row count
	var newCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_ingest_step_new`).Scan(&newCount); err != nil {
		return fmt.Errorf("count new: %w", err)
	}
	if oldCount != newCount {
		return fmt.Errorf("row count mismatch: old=%d new=%d", oldCount, newCount)
	}

	// Drop old and rename
	if _, err := tx.ExecContext(ctx, `DROP TABLE media_ingest_step`); err != nil {
		return fmt.Errorf("drop old: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE media_ingest_step_new RENAME TO media_ingest_step`); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("enable fk: %w", err)
	}

	return nil
}

func ensurePhase5ClaimIndexes(ctx context.Context, db *sql.DB) error {
	idxs := []string{
		`CREATE INDEX IF NOT EXISTS idx_media_ingest_step_claim_node ON media_ingest_step(status,node_key,available_at,lease_until)`,
		`CREATE INDEX IF NOT EXISTS idx_media_ingest_step_run_node ON media_ingest_step(run_id,node_key)`,
	}
	for _, idx := range idxs {
		if _, err := db.ExecContext(ctx, idx); err != nil {
			return fmt.Errorf("create idx: %w", err)
		}
	}
	return nil
}

func ensurePhase5SupplementalTables(ctx context.Context, db *sql.DB) error {
	ddls := []string{documentArtifactSchema, documentFulltextSchemaPhase5, aiAnalysisResultSchema}
	for _, ddl := range ddls {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create tbl: %w", err)
		}
	}
	return nil
}

func ensurePhase5SupplementalTablesTx(ctx context.Context, tx *sql.Tx) error {
	ddls := []string{documentArtifactSchema, documentFulltextSchemaPhase5, aiAnalysisResultSchema}
	for _, ddl := range ddls {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create tbl: %w", err)
		}
	}
	return nil
}

// canonicalMediaIngestStepPhase5Schema returns the Phase 5 canonical schema for fresh database validation.
func canonicalMediaIngestStepPhase5Schema() string {
	return strings.Replace(mediaIngestStepPhase5Schema, "media_ingest_step_new", "media_ingest_step", 1)
}
