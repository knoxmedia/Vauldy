package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
    step_type TEXT NOT NULL CHECK (step_type IN ('poster','scrape','preview','keyframe','subtitle','atrack','encrypt','prepare')),
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
    CHECK (task_type IN ('poster','preview','keyframe','subtitle','atrack','encrypt')),
    CHECK (status IN ('waiting','running','done','failed','cancelled'))
)`

func migrateIngestPublication(ctx context.Context, db *sql.DB) (err error) {
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
		"check(step_typein('poster','scrape','preview','keyframe','subtitle','atrack','encrypt','prepare'))",
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
		"check(task_typein('poster','preview','keyframe','subtitle','atrack','encrypt'))",
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
