package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	pathpkg "path"
	"runtime"
	"strings"
	"time"

	"knox-media/internal/sqliteretry"
)

// SQLiteDBIdentity describes the opened database. SchemaRevision is SQLite's
// PRAGMA schema_version; UserRevision is PRAGMA user_version.
type SQLiteDBIdentity struct {
	Path           string
	SchemaRevision int
	UserRevision   int
}

func SQLiteIdentity(db *sql.DB) (SQLiteDBIdentity, bool) {
	if db == nil {
		return SQLiteDBIdentity{}, false
	}
	var identity SQLiteDBIdentity
	var sequence int
	var name string
	if err := db.QueryRow(`SELECT seq, name, file FROM pragma_database_list WHERE name='main'`).Scan(&sequence, &name, &identity.Path); err != nil {
		return SQLiteDBIdentity{}, false
	}
	if identity.Path == "" {
		identity.Path = "memory"
	} else {
		identity.Path = normalizeSQLiteIdentityPath(identity.Path)
	}
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&identity.SchemaRevision); err != nil {
		return SQLiteDBIdentity{}, false
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&identity.UserRevision); err != nil {
		return SQLiteDBIdentity{}, false
	}
	return identity, true
}

func normalizeSQLiteIdentityPath(dsn string) string {
	cwd, _ := os.Getwd()
	return normalizeSQLiteIdentityPathForOS(dsn, runtime.GOOS, cwd)
}

func normalizeSQLiteIdentityPathForOS(dsn, goos, cwd string) string {
	raw := strings.TrimSpace(dsn)
	if isMemorySQLitePath(raw) {
		return "memory"
	}

	candidate := strings.SplitN(raw, "?", 2)[0]
	decoded := false
	if strings.HasPrefix(strings.ToLower(raw), "file:") {
		if parsed, err := url.Parse(raw); err == nil && strings.EqualFold(parsed.Scheme, "file") {
			escaped := parsed.EscapedPath()
			if parsed.Opaque != "" {
				escaped = parsed.Opaque
			}
			if unescaped, err := url.PathUnescape(escaped); err == nil {
				candidate = unescaped
				decoded = true
			}
			host := parsed.Hostname()
			if host != "" && !strings.EqualFold(host, "localhost") {
				candidate = "//" + host + "/" + strings.TrimLeft(candidate, "/")
			}
		}
	}
	if !decoded {
		if unescaped, err := url.PathUnescape(candidate); err == nil {
			candidate = unescaped
		}
	}
	if goos == "windows" {
		return cleanWindowsSQLitePath(candidate, cwd)
	}
	candidate = strings.ReplaceAll(candidate, `\`, "/")
	if !strings.HasPrefix(candidate, "/") {
		candidate = pathpkg.Join(strings.ReplaceAll(cwd, `\`, "/"), candidate)
	}
	return pathpkg.Clean(candidate)
}

func cleanWindowsSQLitePath(candidate, cwd string) string {
	candidate = strings.ReplaceAll(candidate, `\`, "/")
	cwd = strings.ReplaceAll(cwd, `\`, "/")
	if len(candidate) >= 3 && candidate[0] == '/' && isASCIILetter(candidate[1]) && candidate[2] == ':' {
		candidate = candidate[1:]
	}
	isUNC := strings.HasPrefix(candidate, "//")
	isDriveAbsolute := len(candidate) >= 3 && isASCIILetter(candidate[0]) && candidate[1] == ':' && candidate[2] == '/'
	if !isUNC && !isDriveAbsolute {
		candidate = pathpkg.Join(cwd, candidate)
	}
	if isUNC {
		candidate = "//" + strings.TrimPrefix(pathpkg.Clean("/"+strings.TrimLeft(candidate, "/")), "/")
	} else {
		candidate = pathpkg.Clean(candidate)
	}
	return strings.ReplaceAll(candidate, "/", `\`)
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

// SQLiteDiagnostic is safe structured context for logging SQLite failures.
type SQLiteDiagnostic struct {
	Path                    string
	SchemaRevision          int
	UserRevision            int
	Owner                   string
	Operation               string
	PrimaryCode             int
	ExtendedCode            int
	Elapsed                 time.Duration
	Attempts                int
	TaskID                  int64
	LibraryID               int64
	RemainingLeaseBudget    time.Duration
	HasRemainingLeaseBudget bool
}

func NewSQLiteDiagnostic(db *sql.DB, err error, owner, operation string, elapsed time.Duration, attempts int) SQLiteDiagnostic {
	identity, _ := SQLiteIdentity(db)
	primary, extended, _ := sqliteretry.ErrorCodes(err)
	return SQLiteDiagnostic{Path: identity.Path, SchemaRevision: identity.SchemaRevision, UserRevision: identity.UserRevision, Owner: owner, Operation: operation, PrimaryCode: primary, ExtendedCode: extended, Elapsed: elapsed, Attempts: attempts}
}

func (d SQLiteDiagnostic) Error() string {
	text := fmt.Sprintf("sqlite operation=%s owner=%s path=%s schema_revision=%d user_revision=%d primary_code=%d extended_code=%d elapsed=%s attempts=%d", d.Operation, d.Owner, d.Path, d.SchemaRevision, d.UserRevision, d.PrimaryCode, d.ExtendedCode, d.Elapsed, d.Attempts)
	if d.TaskID > 0 {
		text += fmt.Sprintf(" task_id=%d", d.TaskID)
	}
	if d.LibraryID > 0 {
		text += fmt.Sprintf(" library_id=%d", d.LibraryID)
	}
	if d.HasRemainingLeaseBudget {
		text += fmt.Sprintf(" remaining_lease_budget=%s", d.RemainingLeaseBudget)
	}
	return text
}

func (d SQLiteDiagnostic) Fields() map[string]any {
	fields := map[string]any{"path": d.Path, "schema_revision": d.SchemaRevision, "user_revision": d.UserRevision, "owner": d.Owner, "operation": d.Operation, "primary_code": d.PrimaryCode, "extended_code": d.ExtendedCode, "elapsed": d.Elapsed, "attempts": d.Attempts}
	if d.TaskID > 0 {
		fields["task_id"] = d.TaskID
	}
	if d.LibraryID > 0 {
		fields["library_id"] = d.LibraryID
	}
	if d.HasRemainingLeaseBudget {
		fields["remaining_lease_budget"] = d.RemainingLeaseBudget
	}
	return fields
}

type SQLiteDiagnosticContext struct {
	TaskID                  int64
	LibraryID               int64
	RemainingLeaseBudget    time.Duration
	HasRemainingLeaseBudget bool
}

type sqliteDiagnosticError struct {
	cause      error
	diagnostic SQLiteDiagnostic
}

func (e sqliteDiagnosticError) Error() string   { return fmt.Sprintf("%v: %v", e.cause, e.diagnostic) }
func (e sqliteDiagnosticError) Unwrap() []error { return []error{e.cause, e.diagnostic} }

func WithSQLiteDiagnosticContext(err error, db *sql.DB, owner, operation string, attempts int, elapsed time.Duration, context SQLiteDiagnosticContext) error {
	if err == nil {
		return nil
	}
	var existing SQLiteDiagnostic
	if errors.As(err, &existing) && existing.Operation == operation {
		return err
	}
	diagnostic := NewSQLiteDiagnostic(db, err, owner, operation, elapsed, attempts)
	diagnostic.TaskID = context.TaskID
	diagnostic.LibraryID = context.LibraryID
	diagnostic.RemainingLeaseBudget = context.RemainingLeaseBudget
	diagnostic.HasRemainingLeaseBudget = context.HasRemainingLeaseBudget
	return sqliteDiagnosticError{cause: err, diagnostic: diagnostic}
}
