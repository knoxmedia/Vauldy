package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"modernc.org/sqlite"
	"modernc.org/sqlite/lib"
)

func openPlaybackCompletionMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE user (id INTEGER PRIMARY KEY); INSERT INTO user(id) VALUES (1);`); err != nil {
		t.Fatalf("create base schema: %v", err)
	}
	return db
}

func TestEnsurePlaybackCompletionSchemaFreshAndIdempotent(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	ctx := context.Background()
	if err := ensurePlaybackCompletionSchema(ctx, db); err != nil {
		t.Fatalf("fresh migration: %v", err)
	}
	if err := ensurePlaybackCompletionSchema(ctx, db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}

	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='playback_completion_session'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Fatalf("table count = %d, want 1", tableCount)
	}
}

func TestEnsurePlaybackCompletionSchemaRequiredColumns(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	if err := ensurePlaybackCompletionSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(`PRAGMA table_info(playback_completion_session)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type column struct {
		typeName string
		notNull  int
		defaultV sql.NullString
		pk       int
	}
	got := make(map[string]column)
	for rows.Next() {
		var cid int
		var name string
		var c column
		if err := rows.Scan(&cid, &name, &c.typeName, &c.notNull, &c.defaultV, &c.pk); err != nil {
			t.Fatal(err)
		}
		got[name] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	want := map[string]column{
		"user_id":             {typeName: "INTEGER", notNull: 1, pk: 1},
		"file_id":             {typeName: "TEXT", notNull: 1, pk: 2},
		"session_id":          {typeName: "TEXT", notNull: 1, pk: 3},
		"active":              {typeName: "INTEGER", notNull: 1, defaultV: sql.NullString{String: "1", Valid: true}},
		"last_position":       {typeName: "INTEGER", notNull: 1, defaultV: sql.NullString{String: "0", Valid: true}},
		"last_received_at_ms": {typeName: "INTEGER", notNull: 1, defaultV: sql.NullString{String: "0", Valid: true}},
		"last_sequence":       {typeName: "INTEGER", notNull: 1, defaultV: sql.NullString{String: "0", Valid: true}},
		"valid_play_seconds":  {typeName: "REAL", notNull: 1, defaultV: sql.NullString{String: "0", Valid: true}},
		"awaiting_baseline":   {typeName: "INTEGER", notNull: 1, defaultV: sql.NullString{String: "1", Valid: true}},
		"created_at":          {typeName: "TIMESTAMP", notNull: 1, defaultV: sql.NullString{String: "CURRENT_TIMESTAMP", Valid: true}},
		"updated_at":          {typeName: "TIMESTAMP", notNull: 1, defaultV: sql.NullString{String: "CURRENT_TIMESTAMP", Valid: true}},
	}
	if len(got) != len(want) {
		t.Fatalf("column count = %d, want %d: %#v", len(got), len(want), got)
	}
	for name, expected := range want {
		actual, ok := got[name]
		if !ok {
			t.Errorf("missing column %q", name)
			continue
		}
		if actual != expected {
			t.Errorf("column %s = %#v, want %#v", name, actual, expected)
		}
	}
}

func TestEnsurePlaybackCompletionSchemaRequiredIndexes(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	if err := ensurePlaybackCompletionSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	var activeSQL, updatedSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_playback_completion_active'`).Scan(&activeSQL); err != nil {
		t.Fatalf("active index: %v", err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_playback_completion_updated'`).Scan(&updatedSQL); err != nil {
		t.Fatalf("updated index: %v", err)
	}
	activeNormalized := strings.ToLower(strings.Join(strings.Fields(activeSQL), " "))
	if !strings.Contains(activeNormalized, "unique index") || !strings.Contains(activeNormalized, "(user_id,file_id)") || !strings.Contains(activeNormalized, "where active=1") {
		t.Errorf("unexpected active index SQL: %s", activeSQL)
	}
	updatedNormalized := strings.ToLower(strings.ReplaceAll(updatedSQL, " ", ""))
	if !strings.Contains(updatedNormalized, "(updated_at)") {
		t.Errorf("unexpected updated index SQL: %s", updatedSQL)
	}
}

func TestPlaybackCompletionSchemaAllowsOnlyOneActiveSession(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	if err := ensurePlaybackCompletionSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id) VALUES(1,'file-1','session-1')`); err != nil {
		t.Fatalf("insert first active session: %v", err)
	}
	_, err := db.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id) VALUES(1,'file-1','session-2')`)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		t.Fatalf("error = %T %v code=%d, want SQLITE_CONSTRAINT_UNIQUE", err, err, sqliteErr.Code())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM playback_completion_session WHERE user_id=1 AND file_id='file-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("active row count = %d, err=%v, want 1", count, err)
	}
}

func TestPlaybackCompletionSchemaAllowsInactiveHistory(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	if err := ensurePlaybackCompletionSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{"historical-1", "historical-2"} {
		if _, err := db.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id,active) VALUES(1,'file-1',?,0)`, sessionID); err != nil {
			t.Fatalf("insert inactive session %q: %v", sessionID, err)
		}
	}
}

func createPlaybackCompletionTableOnly(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(playbackCompletionSchemaStatements[0]); err != nil {
		t.Fatalf("create table only: %v", err)
	}
}

func TestEnsurePlaybackCompletionSchemaRepairsMissingIndexes(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	createPlaybackCompletionTableOnly(t, db)

	if err := ensurePlaybackCompletionSchema(context.Background(), db); err != nil {
		t.Fatalf("repair missing indexes: %v", err)
	}
	for _, name := range []string{"idx_playback_completion_active", "idx_playback_completion_updated"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count=%d err=%v", name, count, err)
		}
	}
}

func TestEnsurePlaybackCompletionSchemaRejectsIncompatibleTable(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	if _, err := db.Exec(`CREATE TABLE playback_completion_session (user_id INTEGER, file_id TEXT, session_id TEXT)`); err != nil {
		t.Fatal(err)
	}

	err := ensurePlaybackCompletionSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "table playback_completion_session invariant") || !strings.Contains(err.Error(), "column count") {
		t.Fatalf("error = %v, want precise table invariant error", err)
	}
}

func TestEnsurePlaybackCompletionSchemaRejectsIncompatibleActiveIndex(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	createPlaybackCompletionTableOnly(t, db)
	if _, err := db.Exec(`CREATE INDEX idx_playback_completion_active ON playback_completion_session(file_id)`); err != nil {
		t.Fatal(err)
	}

	err := ensurePlaybackCompletionSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "index idx_playback_completion_active invariant") {
		t.Fatalf("error = %v, want active index invariant error", err)
	}
}

func TestEnsurePlaybackCompletionSchemaRejectsIncompatibleUpdatedIndex(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	createPlaybackCompletionTableOnly(t, db)
	if _, err := db.Exec(`CREATE INDEX idx_playback_completion_updated ON playback_completion_session(last_sequence)`); err != nil {
		t.Fatal(err)
	}

	err := ensurePlaybackCompletionSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "index idx_playback_completion_updated invariant") {
		t.Fatalf("error = %v, want updated index invariant error", err)
	}
}

func TestEnsurePlaybackCompletionSchemaPropagatesDuplicateActiveRepairError(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	createPlaybackCompletionTableOnly(t, db)
	if _, err := db.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id) VALUES(1,'file-1','one'),(1,'file-1','two')`); err != nil {
		t.Fatal(err)
	}

	err := ensurePlaybackCompletionSchema(context.Background(), db)
	var sqliteErr *sqlite.Error
	if err == nil || !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_CONSTRAINT {
		t.Fatalf("error = %T %v, want wrapped SQLITE_CONSTRAINT", err, err)
	}
	if !strings.Contains(err.Error(), "create index idx_playback_completion_active") {
		t.Fatalf("error = %v, want active index creation context", err)
	}
}

func TestPlaybackCompletionSchemaForeignKeyCascades(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	if err := ensurePlaybackCompletionSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO playback_completion_session(user_id,file_id,session_id) VALUES(1,'file-1','session-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM user WHERE id=1`); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM playback_completion_session`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("session count after cascade = %d, err=%v", count, err)
	}
}

func TestPlaybackCompletionSchemaBooleanChecks(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	if err := ensurePlaybackCompletionSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "active", query: `INSERT INTO playback_completion_session(user_id,file_id,session_id,active) VALUES(1,'file-1','bad-active',2)`},
		{name: "awaiting_baseline", query: `INSERT INTO playback_completion_session(user_id,file_id,session_id,awaiting_baseline) VALUES(1,'file-2','bad-baseline',2)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(tc.query)
			var sqliteErr *sqlite.Error
			if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_CONSTRAINT {
				t.Fatalf("error = %T %v, want SQLITE_CONSTRAINT", err, err)
			}
		})
	}
}

func TestEnsurePlaybackCompletionSchemaCanceledContext(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ensurePlaybackCompletionSchema(ctx, db)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want wrapped context.Canceled", err)
	}
	var count int
	if queryErr := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='playback_completion_session'`).Scan(&count); queryErr != nil || count != 0 {
		t.Fatalf("table count after canceled migration = %d, err=%v", count, queryErr)
	}
}

func TestEnsurePlaybackCompletionSchemaAcceptsEquivalentSQLiteFormatting(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	_, err := db.Exec(`CREATE TABLE playback_completion_session (
		"user_id" INTEGER NOT NULL,
		[file_id] TEXT NOT NULL,
		` + "`session_id`" + ` TEXT NOT NULL,
		"active" INTEGER NOT NULL DEFAULT ((+1)) CHECK ((("active") IN (((0)), ((+1))))),
		last_position INTEGER NOT NULL DEFAULT ((+0)),
		last_received_at_ms INTEGER NOT NULL DEFAULT (+0),
		last_sequence INTEGER NOT NULL DEFAULT (0),
		valid_play_seconds REAL NOT NULL DEFAULT ((0)),
		awaiting_baseline INTEGER NOT NULL DEFAULT (+1) CHECK ((([awaiting_baseline])) IN ((0), (1))),
		created_at TIMESTAMP NOT NULL DEFAULT ((CURRENT_TIMESTAMP)),
		updated_at TIMESTAMP NOT NULL DEFAULT (current_timestamp),
		PRIMARY KEY ("user_id", [file_id], ` + "`session_id`" + `),
		FOREIGN KEY ("user_id") REFERENCES "user"("id") ON DELETE CASCADE
	)`)
	if err != nil {
		t.Fatalf("create equivalent table: %v", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_playback_completion_active
		ON playback_completion_session("user_id", [file_id])
		WHERE ((("active")) = ((+1)))`); err != nil {
		t.Fatalf("create equivalent active index: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_playback_completion_updated
		ON playback_completion_session("updated_at")`); err != nil {
		t.Fatalf("create equivalent updated index: %v", err)
	}

	if err := ensurePlaybackCompletionSchema(context.Background(), db); err != nil {
		t.Fatalf("equivalent formatting rejected: %v", err)
	}
}

func TestEnsurePlaybackCompletionSchemaRejectsDifferentSemanticDefault(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	tableSQL := strings.Replace(playbackCompletionSchemaStatements[0], "active INTEGER NOT NULL DEFAULT 1", "active INTEGER NOT NULL DEFAULT 0", 1)
	if _, err := db.Exec(tableSQL); err != nil {
		t.Fatal(err)
	}

	err := ensurePlaybackCompletionSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "column active metadata") {
		t.Fatalf("error = %v, want active default invariant error", err)
	}
}

func TestEnsurePlaybackCompletionSchemaRejectsDifferentBooleanCheck(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	tableSQL := strings.Replace(playbackCompletionSchemaStatements[0], "CHECK (active IN (0,1))", "CHECK (active IN (0,1,2))", 1)
	if _, err := db.Exec(tableSQL); err != nil {
		t.Fatal(err)
	}

	err := ensurePlaybackCompletionSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "boolean constraint") {
		t.Fatalf("error = %v, want boolean constraint invariant error", err)
	}
}

func TestEnsurePlaybackCompletionSchemaRejectsDifferentActivePredicate(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	createPlaybackCompletionTableOnly(t, db)
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_playback_completion_active ON playback_completion_session(user_id,file_id) WHERE active=0`); err != nil {
		t.Fatal(err)
	}

	err := ensurePlaybackCompletionSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "predicate") {
		t.Fatalf("error = %v, want predicate invariant error", err)
	}
}

func TestEnsurePlaybackCompletionSchemaAcceptsMixedCaseMetadataIdentifiers(t *testing.T) {
	db := openPlaybackCompletionMigrationTestDB(t)
	_, err := db.Exec(`CREATE TABLE PLAYBACK_COMPLETION_SESSION (
		USER_ID INTEGER NOT NULL,
		File_ID TEXT NOT NULL,
		SESSION_ID TEXT NOT NULL,
		ACTIVE INTEGER NOT NULL DEFAULT 1 CHECK (ACTIVE IN (0,1)),
		LAST_POSITION INTEGER NOT NULL DEFAULT 0,
		LAST_RECEIVED_AT_MS INTEGER NOT NULL DEFAULT 0,
		LAST_SEQUENCE INTEGER NOT NULL DEFAULT 0,
		VALID_PLAY_SECONDS REAL NOT NULL DEFAULT 0,
		AWAITING_BASELINE INTEGER NOT NULL DEFAULT 1 CHECK (AWAITING_BASELINE IN (0,1)),
		CREATED_AT TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UPDATED_AT TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (USER_ID, File_ID, SESSION_ID),
		FOREIGN KEY (USER_ID) REFERENCES USER(ID) ON DELETE CASCADE
	)`)
	if err != nil {
		t.Fatalf("create mixed-case table: %v", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IDX_PLAYBACK_COMPLETION_ACTIVE
		ON PLAYBACK_COMPLETION_SESSION(USER_ID,File_ID) WHERE ACTIVE=1`); err != nil {
		t.Fatalf("create mixed-case active index: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX Idx_Playback_Completion_Updated
		ON PLAYBACK_COMPLETION_SESSION(UPDATED_AT)`); err != nil {
		t.Fatalf("create mixed-case updated index: %v", err)
	}

	if err := ensurePlaybackCompletionSchema(context.Background(), db); err != nil {
		t.Fatalf("mixed-case compatible schema rejected: %v", err)
	}
}
