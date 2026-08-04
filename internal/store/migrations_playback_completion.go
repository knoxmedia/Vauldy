package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

const playbackCompletionTableName = "playback_completion_session"

var playbackCompletionSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS playback_completion_session (
		user_id INTEGER NOT NULL,
		file_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
		last_position INTEGER NOT NULL DEFAULT 0,
		last_received_at_ms INTEGER NOT NULL DEFAULT 0,
		last_sequence INTEGER NOT NULL DEFAULT 0,
		valid_play_seconds REAL NOT NULL DEFAULT 0,
		awaiting_baseline INTEGER NOT NULL DEFAULT 1 CHECK (awaiting_baseline IN (0,1)),
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (user_id, file_id, session_id),
		FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_playback_completion_active
		ON playback_completion_session(user_id,file_id) WHERE active=1`,
	`CREATE INDEX IF NOT EXISTS idx_playback_completion_updated
		ON playback_completion_session(updated_at)`,
}

type playbackCompletionColumn struct {
	typeName string
	notNull  int
	defaultV string
	pk       int
}

var playbackCompletionColumns = map[string]playbackCompletionColumn{
	"user_id":             {typeName: "INTEGER", notNull: 1, pk: 1},
	"file_id":             {typeName: "TEXT", notNull: 1, pk: 2},
	"session_id":          {typeName: "TEXT", notNull: 1, pk: 3},
	"active":              {typeName: "INTEGER", notNull: 1, defaultV: "1"},
	"last_position":       {typeName: "INTEGER", notNull: 1, defaultV: "0"},
	"last_received_at_ms": {typeName: "INTEGER", notNull: 1, defaultV: "0"},
	"last_sequence":       {typeName: "INTEGER", notNull: 1, defaultV: "0"},
	"valid_play_seconds":  {typeName: "REAL", notNull: 1, defaultV: "0"},
	"awaiting_baseline":   {typeName: "INTEGER", notNull: 1, defaultV: "1"},
	"created_at":          {typeName: "TIMESTAMP", notNull: 1, defaultV: "CURRENT_TIMESTAMP"},
	"updated_at":          {typeName: "TIMESTAMP", notNull: 1, defaultV: "CURRENT_TIMESTAMP"},
}

func ensurePlaybackCompletionSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, playbackCompletionSchemaStatements[0]); err != nil {
		return fmt.Errorf("create table %s: %w", playbackCompletionTableName, err)
	}
	if err := validatePlaybackCompletionTable(ctx, db); err != nil {
		return fmt.Errorf("table %s invariant: %w", playbackCompletionTableName, err)
	}
	indexes := []struct {
		name      string
		statement string
		columns   []string
		unique    int
		partial   int
		predicate string
	}{
		{"idx_playback_completion_active", playbackCompletionSchemaStatements[1], []string{"user_id", "file_id"}, 1, 1, "active=1"},
		{"idx_playback_completion_updated", playbackCompletionSchemaStatements[2], []string{"updated_at"}, 0, 0, ""},
	}
	for _, index := range indexes {
		exists, err := playbackCompletionIndexExists(ctx, db, index.name)
		if err != nil {
			return fmt.Errorf("inspect index %s: %w", index.name, err)
		}
		if exists {
			if err := validatePlaybackCompletionIndex(ctx, db, index.name, index.columns, index.unique, index.partial, index.predicate); err != nil {
				return fmt.Errorf("index %s invariant: %w", index.name, err)
			}
			continue
		}
		if _, err := db.ExecContext(ctx, index.statement); err != nil {
			return fmt.Errorf("create index %s: %w", index.name, err)
		}
		if err := validatePlaybackCompletionIndex(ctx, db, index.name, index.columns, index.unique, index.partial, index.predicate); err != nil {
			return fmt.Errorf("index %s invariant after creation: %w", index.name, err)
		}
	}
	return nil
}

func validatePlaybackCompletionTable(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(playback_completion_session)`)
	if err != nil {
		return err
	}
	got := make(map[string]playbackCompletionColumn)
	for rows.Next() {
		var cid int
		var name string
		var column playbackCompletionColumn
		var defaultV sql.NullString
		if err := rows.Scan(&cid, &name, &column.typeName, &column.notNull, &defaultV, &column.pk); err != nil {
			_ = rows.Close()
			return err
		}
		if defaultV.Valid {
			column.defaultV = defaultV.String
		}
		got[strings.ToLower(name)] = column
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(got) != len(playbackCompletionColumns) {
		return fmt.Errorf("column count=%d want %d", len(got), len(playbackCompletionColumns))
	}
	for name, want := range playbackCompletionColumns {
		actual, ok := got[name]
		if !ok {
			return fmt.Errorf("column %s missing", name)
		}
		actualDefault, actualDefaultErr := canonicalPlaybackCompletionExpr(actual.defaultV)
		wantDefault, wantDefaultErr := canonicalPlaybackCompletionExpr(want.defaultV)
		defaultsMatch := actualDefaultErr == nil && wantDefaultErr == nil && actualDefault == wantDefault
		if !strings.EqualFold(actual.typeName, want.typeName) || actual.notNull != want.notNull || !defaultsMatch || actual.pk != want.pk {
			return fmt.Errorf("column %s metadata=%+v want %+v", name, actual, want)
		}
	}

	var tableSQL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=? COLLATE NOCASE`, playbackCompletionTableName).Scan(&tableSQL); err != nil {
		return err
	}
	checks, err := playbackCompletionCheckExpressions(tableSQL)
	if err != nil {
		return fmt.Errorf("parse boolean constraints: %w", err)
	}
	for _, check := range []string{"active in (0,1)", "awaiting_baseline in (0,1)"} {
		canonical, err := canonicalPlaybackCompletionExpr(check)
		if err != nil {
			return err
		}
		if _, ok := checks[canonical]; !ok {
			return fmt.Errorf("boolean constraint %s missing", check)
		}
	}

	fkRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(playback_completion_session)`)
	if err != nil {
		return err
	}
	defer fkRows.Close()
	foundCascade := false
	for fkRows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return err
		}
		if strings.EqualFold(table, "user") && strings.EqualFold(from, "user_id") && strings.EqualFold(to, "id") && strings.EqualFold(onDelete, "CASCADE") {
			foundCascade = true
		}
	}
	if err := fkRows.Err(); err != nil {
		return err
	}
	if !foundCascade {
		return fmt.Errorf("foreign key user_id -> user(id) ON DELETE CASCADE missing")
	}
	return nil
}

func playbackCompletionIndexExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? COLLATE NOCASE`, name).Scan(&count)
	return count == 1, err
}

func validatePlaybackCompletionIndex(ctx context.Context, db *sql.DB, name string, wantColumns []string, wantUnique, wantPartial int, wantPredicate string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA index_list(playback_completion_session)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var seq, unique, partial int
		var indexName, origin string
		if err := rows.Scan(&seq, &indexName, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return err
		}
		if strings.EqualFold(indexName, name) {
			found = true
			if unique != wantUnique || partial != wantPartial {
				_ = rows.Close()
				return fmt.Errorf("unique=%d partial=%d want unique=%d partial=%d", unique, partial, wantUnique, wantPartial)
			}
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("missing")
	}

	columnRows, err := db.QueryContext(ctx, fmt.Sprintf(`PRAGMA index_info(%q)`, name))
	if err != nil {
		return err
	}
	var columns []string
	for columnRows.Next() {
		var seqno, cid int
		var column string
		if err := columnRows.Scan(&seqno, &cid, &column); err != nil {
			_ = columnRows.Close()
			return err
		}
		columns = append(columns, strings.ToLower(column))
	}
	if err := columnRows.Close(); err != nil {
		return err
	}
	if err := columnRows.Err(); err != nil {
		return err
	}
	if strings.Join(columns, ",") != strings.Join(wantColumns, ",") {
		return fmt.Errorf("columns=%v want %v", columns, wantColumns)
	}

	var indexSQL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name=? COLLATE NOCASE`, name).Scan(&indexSQL); err != nil {
		return err
	}
	predicate, hasPredicate, err := playbackCompletionIndexPredicate(indexSQL)
	if err != nil {
		return err
	}
	if wantPredicate == "" {
		if hasPredicate {
			return fmt.Errorf("unexpected WHERE predicate in %q", indexSQL)
		}
		return nil
	}
	wantCanonical, err := canonicalPlaybackCompletionExpr(wantPredicate)
	if err != nil {
		return err
	}
	if !hasPredicate || predicate != wantCanonical {
		return fmt.Errorf("predicate=%q want WHERE %s", indexSQL, wantPredicate)
	}
	return nil
}

type playbackCompletionExprParser struct {
	tokens []string
	pos    int
}

func canonicalPlaybackCompletionExpr(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	tokens, err := tokenizePlaybackCompletionSQL(value)
	if err != nil {
		return "", err
	}
	parser := playbackCompletionExprParser{tokens: tokens}
	canonical, err := parser.parseExpr()
	if err != nil {
		return "", err
	}
	if parser.pos != len(tokens) {
		return "", fmt.Errorf("unexpected token %q", tokens[parser.pos])
	}
	return canonical, nil
}

func (p *playbackCompletionExprParser) parseExpr() (string, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return "", err
	}
	if p.consume("=") {
		right, err := p.parsePrimary()
		if err != nil {
			return "", err
		}
		return left + "=" + right, nil
	}
	if p.consume("in") {
		if !p.consume("(") {
			return "", fmt.Errorf("IN requires a list")
		}
		var values []string
		for {
			value, err := p.parsePrimary()
			if err != nil {
				return "", err
			}
			values = append(values, value)
			if p.consume(")") {
				break
			}
			if !p.consume(",") {
				return "", fmt.Errorf("IN list requires comma")
			}
		}
		return left + "in(" + strings.Join(values, ",") + ")", nil
	}
	return left, nil
}

func (p *playbackCompletionExprParser) parsePrimary() (string, error) {
	for p.consume("+") {
	}
	if p.consume("(") {
		value, err := p.parseExpr()
		if err != nil {
			return "", err
		}
		if !p.consume(")") {
			return "", fmt.Errorf("missing closing parenthesis")
		}
		return value, nil
	}
	if p.pos >= len(p.tokens) {
		return "", fmt.Errorf("expression ended unexpectedly")
	}
	token := p.tokens[p.pos]
	if token == ")" || token == "," || token == "=" || token == "in" {
		return "", fmt.Errorf("unexpected token %q", token)
	}
	p.pos++
	return token, nil
}

func (p *playbackCompletionExprParser) consume(token string) bool {
	if p.pos < len(p.tokens) && p.tokens[p.pos] == token {
		p.pos++
		return true
	}
	return false
}

func tokenizePlaybackCompletionSQL(value string) ([]string, error) {
	var tokens []string
	for i := 0; i < len(value); {
		r := rune(value[i])
		if unicode.IsSpace(r) {
			i++
			continue
		}
		if strings.ContainsRune("(),=+;", r) {
			tokens = append(tokens, string(r))
			i++
			continue
		}
		if r == '"' || r == '`' || r == '[' {
			close := byte(r)
			if r == '[' {
				close = ']'
			}
			i++
			var identifier strings.Builder
			closed := false
			for i < len(value) {
				if value[i] == close {
					if close != ']' && i+1 < len(value) && value[i+1] == close {
						identifier.WriteByte(close)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				identifier.WriteByte(value[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted identifier")
			}
			tokens = append(tokens, strings.ToLower(identifier.String()))
			continue
		}
		if unicode.IsLetter(r) || r == '_' || unicode.IsDigit(r) {
			start := i
			for i < len(value) {
				current := rune(value[i])
				if !unicode.IsLetter(current) && !unicode.IsDigit(current) && current != '_' {
					break
				}
				i++
			}
			tokens = append(tokens, strings.ToLower(value[start:i]))
			continue
		}
		return nil, fmt.Errorf("unsupported token %q", string(r))
	}
	return tokens, nil
}

func playbackCompletionCheckExpressions(tableSQL string) (map[string]struct{}, error) {
	tokens, err := tokenizePlaybackCompletionSQL(tableSQL)
	if err != nil {
		return nil, err
	}
	checks := make(map[string]struct{})
	for i := 0; i < len(tokens); i++ {
		if tokens[i] != "check" {
			continue
		}
		parser := playbackCompletionExprParser{tokens: tokens, pos: i + 1}
		canonical, err := parser.parsePrimary()
		if err != nil {
			return nil, err
		}
		checks[canonical] = struct{}{}
		i = parser.pos - 1
	}
	return checks, nil
}

func playbackCompletionIndexPredicate(indexSQL string) (string, bool, error) {
	tokens, err := tokenizePlaybackCompletionSQL(indexSQL)
	if err != nil {
		return "", false, err
	}
	for i, token := range tokens {
		if token != "where" {
			continue
		}
		parser := playbackCompletionExprParser{tokens: tokens, pos: i + 1}
		canonical, err := parser.parseExpr()
		if err != nil {
			return "", false, err
		}
		if parser.pos != len(tokens) && !(parser.pos == len(tokens)-1 && tokens[parser.pos] == ";") {
			return "", false, fmt.Errorf("unexpected predicate token %q", tokens[parser.pos])
		}
		return canonical, true, nil
	}
	return "", false, nil
}
