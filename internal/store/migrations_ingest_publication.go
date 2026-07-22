package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const mediaIngestRunSchema = `CREATE TABLE IF NOT EXISTS media_ingest_run (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    scan_task_id INTEGER,
    reason TEXT NOT NULL CHECK (reason IN ('scan','repair','manual_retry')),
    status TEXT NOT NULL CHECK (status IN ('processing','published','degraded','failed','cancelled')),
    preserve_visibility INTEGER NOT NULL DEFAULT 0 CHECK (preserve_visibility IN (0,1)),
    config_snapshot_json TEXT NOT NULL CHECK (json_valid(config_snapshot_json)),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    FOREIGN KEY (scan_task_id) REFERENCES scan_task(id) ON DELETE SET NULL,
    UNIQUE(media_id,generation),
    UNIQUE(id,media_id,generation)
)`

const mediaIngestStepSchema = `CREATE TABLE IF NOT EXISTS media_ingest_step (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL,
    media_id INTEGER NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    step_type TEXT NOT NULL CHECK (step_type IN ('poster','scrape','preview','keyframe','subtitle','atrack','encrypt','prepare','thumbnail')),
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
    FOREIGN KEY (run_id) REFERENCES media_ingest_run(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id,generation) REFERENCES media_ingest_run(media_id,generation),
    FOREIGN KEY (run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation),
    UNIQUE(run_id,step_type),
    UNIQUE(id,media_id,generation)
)`

const scrapeTaskPublicationSchema = `CREATE TABLE scrape_task_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    task_type TEXT DEFAULT 'media',
    source TEXT DEFAULT 'auto',
    query TEXT,
    year INTEGER,
    status TEXT DEFAULT 'waiting',
    progress INTEGER DEFAULT 0,
    fail_count INTEGER DEFAULT 0,
    available_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    message TEXT,
    created_by INTEGER DEFAULT 0,
    ingest_run_id INTEGER,
    ingest_step_id INTEGER,
    generation INTEGER,
    lease_owner TEXT,
    lease_until TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    FOREIGN KEY (ingest_run_id) REFERENCES media_ingest_run(id) ON DELETE CASCADE,
    FOREIGN KEY (ingest_step_id) REFERENCES media_ingest_step(id) ON DELETE CASCADE,
    FOREIGN KEY (ingest_run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation),
    FOREIGN KEY (ingest_step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation),
    CHECK (status IN ('waiting','running','done','failed','abandoned','cancelled')),
    UNIQUE(ingest_run_id,ingest_step_id,generation)
)`
const postIngestTaskPublicationSchema = `CREATE TABLE post_ingest_task_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    scan_task_id INTEGER,
    ingest_run_id INTEGER,
    ingest_step_id INTEGER,
    generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
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
    FOREIGN KEY (ingest_run_id) REFERENCES media_ingest_run(id) ON DELETE CASCADE,
    FOREIGN KEY (ingest_step_id) REFERENCES media_ingest_step(id) ON DELETE CASCADE,
    FOREIGN KEY (ingest_run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation),
    FOREIGN KEY (ingest_step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation),
    UNIQUE(media_id,generation,task_type),
    CHECK (task_type IN ('poster','preview','keyframe','subtitle','atrack','encrypt','thumbnail')),
    CHECK (status IN ('waiting','running','done','failed','cancelled'))
)`

func migrateIngestPublicationV1(ctx context.Context, db *sql.DB) (err error) {
	if db == nil {
		return fmt.Errorf("ingest publication migration: nil database")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ingest publication migration begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	additions := []struct{ name, definition string }{
		{"publication_state", `TEXT NOT NULL DEFAULT 'published' CHECK (publication_state IN ('processing','published','degraded','failed','cancelled'))`},
		{"published_at", `TIMESTAMP`},
		{"publication_error", `TEXT NOT NULL DEFAULT ''`},
		{"ingest_generation", `INTEGER NOT NULL DEFAULT 0 CHECK (ingest_generation >= 0)`},
	}
	mediaColumns, err := ingestPublicationColumns(ctx, tx, "media")
	if err != nil {
		return fmt.Errorf("inspect media columns: %w", err)
	}
	needsLegacyBackfill := !mediaColumns["publication_state"]
	for _, addition := range additions {
		if mediaColumns[addition.name] {
			continue
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE media ADD COLUMN %s %s`, addition.name, addition.definition)); err != nil {
			return fmt.Errorf("add media.%s: %w", addition.name, err)
		}
	}
	if needsLegacyBackfill {
		if _, err = tx.ExecContext(ctx, `UPDATE media SET publication_state='published', published_at=COALESCE(created_at,CURRENT_TIMESTAMP), publication_error='', ingest_generation=0`); err != nil {
			return fmt.Errorf("backfill media publication state: %w", err)
		}
	}
	for _, statement := range []string{
		mediaIngestRunSchema,
		`CREATE INDEX IF NOT EXISTS idx_media_ingest_run_status_updated ON media_ingest_run(status,updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_media_ingest_run_scan_status ON media_ingest_run(scan_task_id,status)`,
		mediaIngestStepSchema,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create ingest schema: %w", err)
		}
	}
	if err = validateMediaIngestRunSchema(ctx, tx); err != nil {
		return fmt.Errorf("media_ingest_run schema invariant: %w", err)
	}
	if err = validateMediaIngestStepSchema(ctx, tx); err != nil {
		return fmt.Errorf("media_ingest_step schema invariant: %w", err)
	}

	taskColumns, err := ingestPublicationColumns(ctx, tx, "post_ingest_task")
	if err != nil {
		return fmt.Errorf("inspect post_ingest_task columns: %w", err)
	}
	currentPostIngest, err := postIngestPublicationSchemaCurrent(ctx, tx, taskColumns)
	if err != nil {
		return fmt.Errorf("inspect post_ingest_task constraints: %w", err)
	}
	if !currentPostIngest {
		if err = rebuildPostIngestTask(ctx, tx, taskColumns); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS idx_post_ingest_claim ON post_ingest_task(status,available_at,lease_until,created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_post_ingest_scan ON post_ingest_task(scan_task_id,status)`,
		`CREATE INDEX IF NOT EXISTS idx_post_ingest_run ON post_ingest_task(ingest_run_id,status)`,
		`CREATE INDEX IF NOT EXISTS idx_post_ingest_step ON post_ingest_task(ingest_step_id,status)`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create post ingest index: %w", err)
		}
	}
	if err = ensureScrapeTaskPublicationSchema(ctx, tx); err != nil {
		return fmt.Errorf("scrape task schema: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	var violation string
	for rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var fkID int
		if scanErr := rows.Scan(&table, &rowID, &parent, &fkID); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("foreign key check result: %w", scanErr)
		}
		if violation == "" {
			violation = fmt.Sprintf("table=%s row=%v parent=%s fk=%d", table, rowID, parent, fkID)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate foreign key check: %w", rowsErr)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return fmt.Errorf("close foreign key check: %w", closeErr)
	}
	if violation != "" {
		return fmt.Errorf("foreign key check failed: %s", violation)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("ingest publication migration commit: %w", err)
	}
	return nil
}

func ingestPublicationColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typeName string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[strings.ToLower(name)] = true
	}
	return columns, rows.Err()
}

func ingestPublicationUniqueIndexSets(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA index_list(%q)`, table))
	if err != nil {
		return nil, err
	}
	var uniqueNames []string
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if unique == 1 {
			uniqueNames = append(uniqueNames, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sets := make(map[string]bool)
	for _, name := range uniqueNames {
		columnRows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA index_info(%q)`, name))
		if err != nil {
			return nil, err
		}
		var columns []string
		for columnRows.Next() {
			var seqno, cid int
			var column string
			if err := columnRows.Scan(&seqno, &cid, &column); err != nil {
				_ = columnRows.Close()
				return nil, err
			}
			columns = append(columns, strings.ToLower(column))
		}
		if err := columnRows.Err(); err != nil {
			_ = columnRows.Close()
			return nil, err
		}
		if err := columnRows.Close(); err != nil {
			return nil, err
		}
		sets[strings.Join(columns, ",")] = true
	}
	return sets, nil
}

func ingestPublicationTableSQL(ctx context.Context, tx *sql.Tx, table string) (string, error) {
	var tableSQL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&tableSQL); err != nil {
		return "", err
	}
	return strings.ToLower(strings.Join(strings.Fields(tableSQL), "")), nil
}

func validateMediaIngestRunSchema(ctx context.Context, tx *sql.Tx) error {
	normalized, err := ingestPublicationTableSQL(ctx, tx, "media_ingest_run")
	if err != nil {
		return err
	}
	for _, required := range []string{
		"generationintegernotnullcheck(generation>0)",
		"check(reasonin('scan','repair','manual_retry'))",
		"check(statusin('processing','published','degraded','failed','cancelled'))",
		"check(preserve_visibilityin(0,1))",
		"check(json_valid(config_snapshot_json))",
		"foreignkey(media_id)referencesmedia(id)ondeletecascade",
		"foreignkey(scan_task_id)referencesscan_task(id)ondeletesetnull",
	} {
		if !strings.Contains(normalized, required) {
			return fmt.Errorf("required constraint %q missing", required)
		}
	}
	sets, err := ingestPublicationUniqueIndexSets(ctx, tx, "media_ingest_run")
	if err != nil {
		return err
	}
	if !sets["media_id,generation"] || !sets["id,media_id,generation"] {
		return fmt.Errorf("required unique keys missing: %v", sets)
	}
	return nil
}

func validateMediaIngestStepSchema(ctx context.Context, tx *sql.Tx) error {
	normalized, err := ingestPublicationTableSQL(ctx, tx, "media_ingest_step")
	if err != nil {
		return err
	}
	for _, required := range []string{
		"generationintegernotnullcheck(generation>0)",
		"check(requiredin(0,1))",
		"check(statusin('waiting','running','done','skipped','failed','cancelled'))",
		"foreignkey(run_id)referencesmedia_ingest_run(id)ondeletecascade",
		"foreignkey(media_id)referencesmedia(id)ondeletecascade",
		"foreignkey(media_id,generation)referencesmedia_ingest_run(media_id,generation)",
		"foreignkey(run_id,media_id,generation)referencesmedia_ingest_run(id,media_id,generation)",
	} {
		if !strings.Contains(normalized, required) {
			return fmt.Errorf("required constraint %q missing", required)
		}
	}
	sets, err := ingestPublicationUniqueIndexSets(ctx, tx, "media_ingest_step")
	if err != nil {
		return err
	}
	if !sets["run_id,step_type"] || !sets["id,media_id,generation"] {
		return fmt.Errorf("required unique keys missing: %v", sets)
	}
	return nil
}
func postIngestPublicationSchemaCurrent(ctx context.Context, tx *sql.Tx, columns map[string]bool) (bool, error) {
	for _, name := range []string{"ingest_run_id", "ingest_step_id", "generation"} {
		if !columns[name] {
			return false, nil
		}
	}
	var tableSQL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='post_ingest_task'`).Scan(&tableSQL); err != nil {
		return false, err
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(tableSQL), ""))
	for _, required := range []string{
		"generationintegernotnulldefault0check(generation>=0)",
		"unique(media_id,generation,task_type)",
		"check(task_typein('poster','preview','keyframe','subtitle','atrack','encrypt','thumbnail'))",
		"check(statusin('waiting','running','done','failed','cancelled'))",
	} {
		if !strings.Contains(normalized, required) {
			return false, nil
		}
	}
	uniqueSets, err := ingestPublicationUniqueIndexSets(ctx, tx, "post_ingest_task")
	if err != nil {
		return false, err
	}
	if !uniqueSets["media_id,generation,task_type"] || uniqueSets["media_id,task_type"] {
		return false, nil
	}

	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(post_ingest_task)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	type foreignKeyPart struct{ table, from, to, onDelete string }
	groups := make(map[int][]foreignKeyPart)
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return false, err
		}
		groups[id] = append(groups[id], foreignKeyPart{strings.ToLower(table), strings.ToLower(from), strings.ToLower(to), strings.ToLower(onDelete)})
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	hasComposite := func(table string, from, to []string) bool {
		for _, parts := range groups {
			if len(parts) != len(from) {
				continue
			}
			matches := true
			for i, part := range parts {
				if part.table != table || part.from != from[i] || part.to != to[i] {
					matches = false
					break
				}
			}
			if matches {
				return true
			}
		}
		return false
	}
	return hasComposite("media_ingest_run", []string{"ingest_run_id", "media_id", "generation"}, []string{"id", "media_id", "generation"}) &&
		hasComposite("media_ingest_step", []string{"ingest_step_id", "media_id", "generation"}, []string{"id", "media_id", "generation"}), nil
}
func rebuildPostIngestTask(ctx context.Context, tx *sql.Tx, columns map[string]bool) error {
	var before int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task`).Scan(&before); err != nil {
		return fmt.Errorf("count legacy post ingest tasks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS post_ingest_task_new`); err != nil {
		return fmt.Errorf("drop stale post ingest rebuild table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, postIngestTaskPublicationSchema); err != nil {
		return fmt.Errorf("create rebuilt post_ingest_task: %w", err)
	}
	value := func(name, fallback string) string {
		if columns[name] {
			return name
		}
		return fallback
	}
	insert := fmt.Sprintf(`INSERT INTO post_ingest_task_new(id,media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,available_at,lease_owner,lease_until,last_error,created_at,updated_at,started_at,finished_at)
SELECT id,media_id,scan_task_id,%s,%s,%s,task_type,status,attempts,max_attempts,available_at,lease_owner,lease_until,last_error,created_at,updated_at,started_at,finished_at FROM post_ingest_task`, value("ingest_run_id", "NULL"), value("ingest_step_id", "NULL"), value("generation", "0"))
	if _, err := tx.ExecContext(ctx, insert); err != nil {
		return fmt.Errorf("copy post ingest tasks: %w", err)
	}
	var after int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task_new`).Scan(&after); err != nil {
		return fmt.Errorf("count copied post ingest tasks: %w", err)
	}
	if after != before {
		return fmt.Errorf("post ingest task row count changed: before=%d after=%d", before, after)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE post_ingest_task`); err != nil {
		return fmt.Errorf("drop legacy post_ingest_task: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE post_ingest_task_new RENAME TO post_ingest_task`); err != nil {
		return fmt.Errorf("rename rebuilt post_ingest_task: %w", err)
	}
	return nil
}

func scrapeTaskPublicationSchemaCurrent(ctx context.Context, tx *sql.Tx, columns map[string]bool) (bool, error) {
	for _, name := range []string{"ingest_run_id", "ingest_step_id", "generation", "lease_owner", "lease_until"} {
		if !columns[name] {
			return false, nil
		}
	}
	normalized, err := ingestPublicationTableSQL(ctx, tx, "scrape_task")
	if err != nil {
		return false, err
	}
	for _, required := range []string{"check(statusin('waiting','running','done','failed','abandoned','cancelled'))", "unique(ingest_run_id,ingest_step_id,generation)"} {
		if !strings.Contains(normalized, required) {
			return false, nil
		}
	}
	sets, err := ingestPublicationUniqueIndexSets(ctx, tx, "scrape_task")
	if err != nil {
		return false, err
	}
	if !sets["ingest_run_id,ingest_step_id,generation"] || sets["media_id"] || sets["media_id,task_type"] {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(scrape_task)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	type part struct{ table, from, to string }
	groups := map[int][]part{}
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return false, err
		}
		groups[id] = append(groups[id], part{strings.ToLower(table), strings.ToLower(from), strings.ToLower(to)})
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	has := func(table string, from, to []string) bool {
		for _, ps := range groups {
			if len(ps) != len(from) {
				continue
			}
			ok := true
			for i, p := range ps {
				if p.table != table || p.from != from[i] || p.to != to[i] {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
	if !has("media_ingest_run", []string{"ingest_run_id", "media_id", "generation"}, []string{"id", "media_id", "generation"}) || !has("media_ingest_step", []string{"ingest_step_id", "media_id", "generation"}, []string{"id", "media_id", "generation"}) {
		return false, nil
	}
	requiredIndexes := map[string]string{"idx_scrape_task_claim": "status,lease_until,created_at", "idx_scrape_task_ingest": "ingest_run_id,ingest_step_id,generation", "idx_scrape_task_media": "media_id,created_at"}
	for name, want := range requiredIndexes {
		indexRows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA index_info(%q)`, name))
		if err != nil {
			return false, err
		}
		var got []string
		for indexRows.Next() {
			var seq, cid int
			var column string
			if err := indexRows.Scan(&seq, &cid, &column); err != nil {
				indexRows.Close()
				return false, err
			}
			got = append(got, strings.ToLower(column))
		}
		if err := indexRows.Close(); err != nil {
			return false, err
		}
		if strings.Join(got, ",") != want {
			return false, nil
		}
	}
	return true, nil
}

func ensureScrapeTaskPublicationSchema(ctx context.Context, tx *sql.Tx) error {
	columns, err := ingestPublicationColumns(ctx, tx, "scrape_task")
	if err != nil {
		return err
	}
	needed := []string{"ingest_run_id", "ingest_step_id", "generation", "lease_owner", "lease_until"}
	_ = needed
	current, err := scrapeTaskPublicationSchemaCurrent(ctx, tx, columns)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='scrape_task'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		if _, err := tx.ExecContext(ctx, strings.Replace(scrapeTaskPublicationSchema, "scrape_task_new", "scrape_task", 1)); err != nil {
			return err
		}
		return nil
	}
	var before int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scrape_task`).Scan(&before); err != nil {
		return fmt.Errorf("count legacy scrape tasks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS scrape_task_new`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, scrapeTaskPublicationSchema); err != nil {
		return err
	}
	value := func(name, fallback string) string {
		if columns[name] {
			return name
		}
		return fallback
	}
	q := fmt.Sprintf(`INSERT INTO scrape_task_new(id,media_id,task_type,source,query,year,status,progress,fail_count,available_at,message,created_by,ingest_run_id,ingest_step_id,generation,lease_owner,lease_until,created_at,started_at,finished_at)
SELECT id,media_id,task_type,source,query,year,status,progress,COALESCE(fail_count,0),%s,message,created_by,%s,%s,%s,%s,%s,created_at,started_at,finished_at FROM scrape_task`, value("available_at", "CURRENT_TIMESTAMP"), value("ingest_run_id", "NULL"), value("ingest_step_id", "NULL"), value("generation", "NULL"), value("lease_owner", "NULL"), value("lease_until", "NULL"))
	if _, err := tx.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("copy scrape tasks: %w", err)
	}
	var after int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scrape_task_new`).Scan(&after); err != nil {
		return fmt.Errorf("count copied scrape tasks: %w", err)
	}
	if after != before {
		return fmt.Errorf("scrape task row count changed: before=%d after=%d", before, after)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE scrape_task`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE scrape_task_new RENAME TO scrape_task`); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS idx_scrape_task_claim ON scrape_task(status,lease_until,created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_scrape_task_ingest ON scrape_task(ingest_run_id,ingest_step_id,generation)`,
		`CREATE INDEX IF NOT EXISTS idx_scrape_task_media ON scrape_task(media_id,created_at)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

var publicationMigrationMu sync.Mutex

// PostCommitMigrationValidationError means schema changes committed but the
// restored FK graph did not validate. Startup must stop; rollback is impossible.
type PostCommitMigrationValidationError struct{ Cause error }

func (e *PostCommitMigrationValidationError) Error() string {
	return fmt.Sprintf("publication migration post-commit validation failed: %v", e.Cause)
}
func (e *PostCommitMigrationValidationError) Unwrap() error { return e.Cause }

var publicationMigrationPostCommitValidation = validatePublicationForeignKeys

func migrateIngestPublication(ctx context.Context, db *sql.DB) error {
	publicationMigrationMu.Lock()
	defer publicationMigrationMu.Unlock()
	if err := publicationMigrationPreflightDB(ctx, db); err != nil {
		return err
	}
	if err := migrateIngestPublicationV1(ctx, db); err != nil {
		return err
	}
	return migratePublicationV2(ctx, db)
}

func migratePublicationV2(ctx context.Context, db *sql.DB) (err error) {
	if err := publicationMigrationPreflightDB(ctx, db); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	var fk int
	if err = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 0 {
		return fmt.Errorf("foreign keys not disabled: value=%d err=%v", fk, err)
	}
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("publication v2 begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
		_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
	}()
	for _, alter := range []string{
		`ALTER TABLE media_ingest_run ADD COLUMN policy_version INTEGER NOT NULL DEFAULT 1 CHECK(policy_version IN (1,2))`,
		`ALTER TABLE media_ingest_run ADD COLUMN terminal_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE media_ingest_run ADD COLUMN superseded_by_generation INTEGER`,
		`ALTER TABLE media_ingest_run ADD COLUMN superseded_at TIMESTAMP`,
	} {
		if _, e := conn.ExecContext(ctx, alter); e != nil && !strings.Contains(strings.ToLower(e.Error()), "duplicate column") {
			return e
		}
	}
	if tableExists(ctx, conn, "transcode_task") {
		if err = rebuildPublicationTranscodeTask(ctx, conn); err != nil {
			return err
		}
	}
	// Rebuild the step table only when its CHECK predates thumbnail.
	stepSQL, e := publicationTableSQL(ctx, conn, "media_ingest_step")
	if e != nil {
		return e
	}
	if !strings.Contains(stepSQL, "'thumbnail'") {
		if _, e = conn.ExecContext(ctx, `DROP TABLE IF EXISTS media_ingest_step_v2`); e != nil {
			return e
		}
		create := strings.Replace(mediaIngestStepSchema, "CREATE TABLE IF NOT EXISTS media_ingest_step", "CREATE TABLE media_ingest_step_v2", 1)
		if _, e = conn.ExecContext(ctx, create); e != nil {
			return e
		}
		if _, e = conn.ExecContext(ctx, `INSERT INTO media_ingest_step_v2 SELECT * FROM media_ingest_step`); e != nil {
			return e
		}
		if _, e = conn.ExecContext(ctx, `DROP TABLE media_ingest_step`); e != nil {
			return e
		}
		if _, e = conn.ExecContext(ctx, `ALTER TABLE media_ingest_step_v2 RENAME TO media_ingest_step`); e != nil {
			return e
		}
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS media_ingest_step_dependency(step_id INTEGER NOT NULL REFERENCES media_ingest_step(id) ON DELETE CASCADE,depends_on_step_id INTEGER REFERENCES media_ingest_step(id) ON DELETE CASCADE,dependency_kind TEXT NOT NULL CHECK(dependency_kind IN ('step_done','media_visible')),CHECK((dependency_kind='step_done' AND depends_on_step_id IS NOT NULL) OR (dependency_kind='media_visible' AND depends_on_step_id IS NULL)),UNIQUE(step_id,dependency_kind,depends_on_step_id))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ingest_dependency_visible ON media_ingest_step_dependency(step_id) WHERE dependency_kind='media_visible'`,
		`CREATE TABLE IF NOT EXISTS media_ingest_evidence(id INTEGER PRIMARY KEY AUTOINCREMENT,run_id INTEGER NOT NULL,step_id INTEGER NOT NULL,media_id INTEGER NOT NULL,generation INTEGER NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('poster','thumbnail','encrypt')),source_fingerprint TEXT NOT NULL,artifact_refs_json TEXT NOT NULL CHECK(json_valid(artifact_refs_json)),reused_from_evidence_id INTEGER REFERENCES media_ingest_evidence(id) ON DELETE SET NULL,reason TEXT NOT NULL DEFAULT '',created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,verified_at TIMESTAMP NOT NULL,stage_id TEXT NOT NULL,FOREIGN KEY(run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation) ON DELETE CASCADE,FOREIGN KEY(step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation) ON DELETE CASCADE,FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,UNIQUE(step_id,kind),UNIQUE(stage_id))`,
		`CREATE TABLE IF NOT EXISTS media_asset_stage_journal(stage_id TEXT PRIMARY KEY,media_id INTEGER NOT NULL,run_id INTEGER NOT NULL,step_id INTEGER NOT NULL,generation INTEGER NOT NULL,owner_token TEXT NOT NULL,source_fingerprint TEXT NOT NULL,artifact_kind TEXT NOT NULL CHECK(artifact_kind IN ('poster','thumbnail','encrypt')),state TEXT NOT NULL CHECK(state IN ('staged','quarantined','committed')),original_path TEXT NOT NULL DEFAULT '',quarantine_path TEXT NOT NULL DEFAULT '',staged_path TEXT NOT NULL,hashes_sizes_json TEXT NOT NULL CHECK(json_valid(hashes_sizes_json)),recovery_error TEXT NOT NULL DEFAULT '',created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,FOREIGN KEY(run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation) ON DELETE CASCADE,FOREIGN KEY(step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS idx_asset_stage_recovery ON media_asset_stage_journal(state,updated_at)`,
	} {
		if _, e = conn.ExecContext(ctx, stmt); e != nil {
			return e
		}
	}
	if tableExists(ctx, conn, "transcode_task") {
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("publication v2 commit: %w", err)
	}
	committed = true
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return &PostCommitMigrationValidationError{err}
	}
	if err = publicationMigrationPostCommitValidation(ctx, conn); err != nil {
		return &PostCommitMigrationValidationError{err}
	}
	return nil
}

func tableExists(ctx context.Context, q SQLExecutor, table string) bool {
	var n int
	return q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n) == nil && n == 1
}
func publicationColumns(ctx context.Context, q SQLExecutor, table string) (map[string]bool, error) {
	rows, e := q.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var d sql.NullString
		if e = rows.Scan(&cid, &name, &typ, &nn, &d, &pk); e != nil {
			return nil, e
		}
		out[strings.ToLower(name)] = true
	}
	return out, rows.Err()
}
func publicationTableSQL(ctx context.Context, q SQLExecutor, table string) (string, error) {
	var s string
	e := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&s)
	return strings.ToLower(strings.Join(strings.Fields(s), "")), e
}
func validatePublicationForeignKeys(ctx context.Context, conn *sql.Conn) error {
	var enabled int
	if e := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); e != nil || enabled != 1 {
		return fmt.Errorf("foreign_keys=%d err=%v", enabled, e)
	}
	rows, e := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if e != nil {
		return e
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var row sql.NullInt64
		var id int
		_ = rows.Scan(&table, &row, &parent, &id)
		return fmt.Errorf("foreign key check: table=%s row=%v parent=%s fk=%d", table, row, parent, id)
	}
	return rows.Err()
}

func rebuildPublicationTranscodeTask(ctx context.Context, conn *sql.Conn) error {
	cols, err := publicationColumns(ctx, conn, "transcode_task")
	if err != nil {
		return err
	}
	// Reject partial or mismatched linkage before any table is dropped.
	for _, name := range []string{"ingest_run_id", "ingest_step_id", "generation"} {
		if !cols[name] {
			if _, err = conn.ExecContext(ctx, `ALTER TABLE transcode_task ADD COLUMN `+name+` INTEGER`); err != nil {
				return err
			}
		}
	}
	for _, def := range []string{"media_id INTEGER", "lease_owner TEXT", "lease_until TIMESTAMP"} {
		name := strings.Fields(def)[0]
		if !cols[name] {
			if _, err = conn.ExecContext(ctx, `ALTER TABLE transcode_task ADD COLUMN `+def); err != nil {
				return err
			}
		}
	}
	var bad int
	err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_task t WHERE NOT ((t.ingest_run_id IS NULL AND t.ingest_step_id IS NULL AND t.generation IS NULL AND t.media_id IS NULL) OR (t.ingest_run_id IS NOT NULL AND t.ingest_step_id IS NOT NULL AND t.generation IS NOT NULL AND EXISTS(SELECT 1 FROM media_ingest_step s WHERE s.id=t.ingest_step_id AND s.run_id=t.ingest_run_id AND s.generation=t.generation AND (t.media_id IS NULL OR t.media_id=s.media_id))))`).Scan(&bad)
	if err != nil {
		return err
	}
	if bad != 0 {
		return fmt.Errorf("transcode linkage invalid or ambiguous: rows=%d", bad)
	}
	if _, err = conn.ExecContext(ctx, `UPDATE transcode_task SET media_id=(SELECT media_id FROM media_ingest_step WHERE id=transcode_task.ingest_step_id) WHERE ingest_step_id IS NOT NULL`); err != nil {
		return err
	}

	original, err := publicationRawTableSQL(ctx, conn, "transcode_task")
	if err != nil {
		return err
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(original), ""))
	if strings.Contains(normalized, "foreignkey(ingest_step_id,media_id,generation)referencesmedia_ingest_step(id,media_id,generation)ondeletecascade") && publicationTranscodeSchemaCurrent(ctx, conn) {
		return nil
	}
	approved := map[string]bool{"pretranscode_task_meta": true, "pretranscode_rendition_job": true, "media_ingest_step_dependency": true, "media_ingest_evidence": true, "media_asset_stage_journal": true}
	refs, err := conn.QueryContext(ctx, `SELECT m.name FROM sqlite_master m,pragma_foreign_key_list(m.name) f WHERE m.type='table' AND f."table"='transcode_task'`)
	if err != nil {
		return err
	}
	for refs.Next() {
		var name string
		if err = refs.Scan(&name); err != nil {
			refs.Close()
			return err
		}
		if !approved[name] {
			refs.Close()
			return fmt.Errorf("unknown foreign key reference to transcode_task from %s", name)
		}
	}
	if err = refs.Close(); err != nil {
		return err
	}
	indexRows, err := conn.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name='transcode_task' AND sql IS NOT NULL`)
	if err != nil {
		return err
	}
	var indexes []string
	for indexRows.Next() {
		var q string
		if err = indexRows.Scan(&q); err != nil {
			indexRows.Close()
			return err
		}
		indexes = append(indexes, q)
	}
	if err = indexRows.Close(); err != nil {
		return err
	}
	closeAt := strings.LastIndex(original, ")")
	if closeAt < 0 {
		return fmt.Errorf("transcode_task CREATE SQL malformed")
	}
	body := original[:closeAt]
	create := strings.Replace(body, "transcode_task", "transcode_task_v2", 1) + `,
 FOREIGN KEY(media_id) REFERENCES media(id) ON DELETE CASCADE,
 FOREIGN KEY(ingest_run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation) ON DELETE CASCADE,
 FOREIGN KEY(ingest_step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation) ON DELETE CASCADE,
 CHECK((ingest_run_id IS NULL AND ingest_step_id IS NULL AND generation IS NULL AND media_id IS NULL) OR (ingest_run_id IS NOT NULL AND ingest_step_id IS NOT NULL AND generation IS NOT NULL AND media_id IS NOT NULL)))` + original[closeAt+1:]
	if _, err = conn.ExecContext(ctx, `DROP TABLE IF EXISTS transcode_task_v2`); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, create); err != nil {
		return fmt.Errorf("create transcode_task v2: %w", err)
	}
	cols, err = publicationColumns(ctx, conn, "transcode_task")
	if err != nil {
		return err
	}
	var names []string
	for name := range cols {
		names = append(names, name)
	}
	sort.Strings(names)
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = `"` + strings.ReplaceAll(n, `"`, `""`) + `"`
	}
	list := strings.Join(quoted, ",")
	if _, err = conn.ExecContext(ctx, `INSERT INTO transcode_task_v2(`+list+`) SELECT `+list+` FROM transcode_task`); err != nil {
		return fmt.Errorf("copy transcode_task: %w", err)
	}
	var before, after int
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_task`).Scan(&before); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_task_v2`).Scan(&after); err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("transcode row count changed: %d/%d", before, after)
	}
	if _, err = conn.ExecContext(ctx, `DROP TABLE transcode_task`); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `ALTER TABLE transcode_task_v2 RENAME TO transcode_task`); err != nil {
		return err
	}
	for _, q := range indexes {
		if _, err = conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
func publicationRawTableSQL(ctx context.Context, q SQLExecutor, table string) (string, error) {
	var s string
	err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&s)
	return s, err
}

var publicationRebuiltParents = map[string]bool{"media_ingest_step": true, "transcode_task": true}
var publicationApprovedChildren = map[string]bool{"post_ingest_task": true, "scrape_task": true, "transcode_task": true, "pretranscode_task_meta": true, "pretranscode_rendition_job": true, "media_ingest_step_dependency": true, "media_ingest_evidence": true, "media_asset_stage_journal": true}

func publicationMigrationPreflightDB(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return publicationMigrationPreflight(ctx, conn)
}
func publicationMigrationPreflight(ctx context.Context, q SQLExecutor) error {
	rows, err := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND (name LIKE '%_v2' OR name LIKE 'publication_backup_%')`)
	if err != nil {
		return err
	}
	if rows.Next() {
		var n string
		_ = rows.Scan(&n)
		rows.Close()
		return fmt.Errorf("publication migration mixed temporary schema: %s", n)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for parent := range publicationRebuiltParents {
		refs, e := q.QueryContext(ctx, `SELECT DISTINCT m.name FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) f WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%' AND f."table"=?`, parent)
		if e != nil {
			return e
		}
		for refs.Next() {
			var child string
			if e = refs.Scan(&child); e != nil {
				refs.Close()
				return e
			}
			if !publicationApprovedChildren[child] {
				refs.Close()
				return fmt.Errorf("publication migration unknown reference %s -> %s", child, parent)
			}
		}
		if e = refs.Close(); e != nil {
			return e
		}
		tr, e := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name=?`, parent)
		if e != nil {
			return e
		}
		if tr.Next() {
			var n string
			_ = tr.Scan(&n)
			tr.Close()
			return fmt.Errorf("publication migration unknown trigger %s on %s", n, parent)
		}
		if e = tr.Close(); e != nil {
			return e
		}
	}
	if tableExists(ctx, q, "media_ingest_step_dependency") {
		if err := validatePublicationV2Schema(ctx, q); err != nil {
			return err
		}
	}
	if tableExists(ctx, q, "media_ingest_run") {
		cols, e := publicationColumns(ctx, q, "media_ingest_run")
		if e != nil {
			return e
		}
		present := 0
		for _, n := range []string{"policy_version", "terminal_reason", "superseded_by_generation", "superseded_at"} {
			if cols[n] {
				present++
			}
		}
		if present != 0 && present != 4 {
			return fmt.Errorf("publication migration mixed run schema: %d/4 v2 columns", present)
		}
	}
	return nil
}
func publicationTranscodeSchemaCurrent(ctx context.Context, q SQLExecutor) bool {
	cols, err := publicationColumns(ctx, q, "transcode_task")
	if err != nil {
		return false
	}
	for _, n := range []string{"media_id", "ingest_run_id", "ingest_step_id", "generation", "lease_owner", "lease_until"} {
		if !cols[n] {
			return false
		}
	}
	groups, err := publicationForeignKeys(ctx, q, "transcode_task")
	if err != nil {
		return false
	}
	return groups["media:media_id:id:cascade"] && groups["media_ingest_run:ingest_run_id,media_id,generation:id,media_id,generation:cascade"] && groups["media_ingest_step:ingest_step_id,media_id,generation:id,media_id,generation:cascade"]
}
func publicationForeignKeys(ctx context.Context, q SQLExecutor, table string) (map[string]bool, error) {
	rows, e := q.QueryContext(ctx, fmt.Sprintf(`PRAGMA foreign_key_list(%q)`, table))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	type p struct {
		seq                  int
		table, from, to, del string
	}
	g := map[int][]p{}
	for rows.Next() {
		var id, seq int
		var table, from, to, upd, del, match string
		if e = rows.Scan(&id, &seq, &table, &from, &to, &upd, &del, &match); e != nil {
			return nil, e
		}
		g[id] = append(g[id], p{seq, strings.ToLower(table), strings.ToLower(from), strings.ToLower(to), strings.ToLower(del)})
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	out := map[string]bool{}
	for _, ps := range g {
		sort.Slice(ps, func(i, j int) bool { return ps[i].seq < ps[j].seq })
		var from, to []string
		for _, x := range ps {
			from = append(from, x.from)
			to = append(to, x.to)
		}
		out[ps[0].table+":"+strings.Join(from, ",")+":"+strings.Join(to, ",")+":"+ps[0].del] = true
	}
	return out, nil
}

func validatePublicationV2Schema(ctx context.Context, q SQLExecutor) error {
	rows, err := q.QueryContext(ctx, `PRAGMA index_list(media_ingest_step_dependency)`)
	if err != nil {
		return err
	}
	visibleUnique, visiblePartial := 0, 0
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err = rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return err
		}
		if name == "idx_ingest_dependency_visible" {
			visibleUnique = unique
			visiblePartial = partial
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if visibleUnique != 1 || visiblePartial != 1 {
		return fmt.Errorf("publication v2 dependency visible index drift: unique=%d partial=%d", visibleUnique, visiblePartial)
	}
	for table := range map[string]bool{"media_ingest_evidence": true, "media_asset_stage_journal": true} {
		if !tableExists(ctx, q, table) {
			return fmt.Errorf("publication v2 missing table %s", table)
		}
	}
	deps, err := publicationForeignKeys(ctx, q, "media_ingest_evidence")
	if err != nil {
		return err
	}
	for _, key := range []string{"media:media_id:id:cascade", "media_ingest_run:run_id,media_id,generation:id,media_id,generation:cascade", "media_ingest_step:step_id,media_id,generation:id,media_id,generation:cascade", "media_ingest_evidence:reused_from_evidence_id:id:set null"} {
		if !deps[key] {
			return fmt.Errorf("publication v2 evidence FK missing %s", key)
		}
	}
	journal, err := publicationForeignKeys(ctx, q, "media_asset_stage_journal")
	if err != nil {
		return err
	}
	for _, key := range []string{"media_ingest_run:run_id,media_id,generation:id,media_id,generation:cascade", "media_ingest_step:step_id,media_id,generation:id,media_id,generation:cascade"} {
		if !journal[key] {
			return fmt.Errorf("publication v2 journal FK missing %s", key)
		}
	}
	return nil
}
