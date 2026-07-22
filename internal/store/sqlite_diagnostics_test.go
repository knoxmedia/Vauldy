package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlite3 "modernc.org/sqlite/lib"
)

func TestSQLiteDiagnosticIncludesPathRevisionOwnerAndExtendedCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostic.sqlite")
	dsn := "file:" + filepath.ToSlash(path) + "?mode=rwc&token=must-not-leak"
	db, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	identity, ok := SQLiteIdentity(db)
	if !ok {
		t.Fatal("OpenSQLite did not register database identity")
	}
	if identity.Path != filepath.Clean(path) && identity.Path != filepath.ToSlash(filepath.Clean(path)) {
		t.Fatalf("identity path=%q, want absolute database path %q", identity.Path, path)
	}
	if strings.Contains(identity.Path, "token") || strings.Contains(identity.Path, "must-not-leak") {
		t.Fatalf("identity leaked DSN query: %q", identity.Path)
	}
	var schemaRevision, userRevision int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&schemaRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userRevision); err != nil {
		t.Fatal(err)
	}
	if identity.SchemaRevision != schemaRevision || identity.UserRevision != userRevision {
		t.Fatalf("identity revisions=%d/%d, want %d/%d", identity.SchemaRevision, identity.UserRevision, schemaRevision, userRevision)
	}

	busy := fmt.Errorf("wrapped: %w", sqliteTestError(t, sqlite3.SQLITE_BUSY_SNAPSHOT))
	diagnostic := NewSQLiteDiagnostic(db, busy, "worker-7", "claim", 45*time.Millisecond, 3)
	if diagnostic.Path != identity.Path || diagnostic.SchemaRevision != schemaRevision || diagnostic.UserRevision != userRevision {
		t.Fatalf("diagnostic identity=%+v, identity=%+v", diagnostic, identity)
	}
	if diagnostic.Owner != "worker-7" || diagnostic.Operation != "claim" || diagnostic.PrimaryCode != sqlite3.SQLITE_BUSY || diagnostic.ExtendedCode != sqlite3.SQLITE_BUSY_SNAPSHOT {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
	if diagnostic.Elapsed != 45*time.Millisecond || diagnostic.Attempts != 3 {
		t.Fatalf("diagnostic timing=%+v", diagnostic)
	}
	fields := diagnostic.Fields()
	if fields["path"] != identity.Path || fields["operation"] != "claim" || fields["extended_code"] != sqlite3.SQLITE_BUSY_SNAPSHOT {
		t.Fatalf("fields=%v", fields)
	}
	if !strings.Contains(diagnostic.Error(), "operation=claim") || strings.Contains(diagnostic.Error(), "must-not-leak") {
		t.Fatalf("Error()=%q", diagnostic.Error())
	}
}

func TestSQLiteIdentityIsIsolatedPerDatabase(t *testing.T) {
	db1, err := OpenSQLite(filepath.Join(t.TempDir(), "one.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := OpenSQLite(filepath.Join(t.TempDir(), "two.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	one, ok1 := SQLiteIdentity(db1)
	two, ok2 := SQLiteIdentity(db2)
	if !ok1 || !ok2 || one.Path == two.Path {
		t.Fatalf("one=%+v ok=%v two=%+v ok=%v", one, ok1, two, ok2)
	}
}

func TestNormalizeSQLiteIdentityPathFileURIs(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		goos string
		cwd  string
		want string
	}{
		{name: "windows drive opaque", dsn: `file:C:/data/media.db?token=secret`, goos: "windows", cwd: `D:\work`, want: `C:\data\media.db`},
		{name: "windows drive standard URI", dsn: `file:///C:/data/media.db?token=secret`, goos: "windows", cwd: `D:\work`, want: `C:\data\media.db`},
		{name: "localhost drive URI", dsn: `file://localhost/C:/data/media.db?mode=rwc`, goos: "windows", cwd: `D:\work`, want: `C:\data\media.db`},
		{name: "percent encoded", dsn: `file:///C:/Media/My%20Library.db?password=secret`, goos: "windows", cwd: `D:\work`, want: `C:\Media\My Library.db`},
		{name: "relative file URI", dsn: `file:foo.db?token=secret`, goos: "windows", cwd: `C:\app\data`, want: `C:\app\data\foo.db`},
		{name: "UNC authority", dsn: `file://server/share/media.db?token=secret`, goos: "windows", cwd: `C:\app`, want: `\\server\share\media.db`},
		{name: "unix standard URI", dsn: `file:///var/lib/media.db?token=secret`, goos: "linux", cwd: `/srv/app`, want: `/var/lib/media.db`},
		{name: "unix relative", dsn: `file:foo.db?token=secret`, goos: "linux", cwd: `/srv/app`, want: `/srv/app/foo.db`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeSQLiteIdentityPathForOS(tt.dsn, tt.goos, tt.cwd)
			if got != tt.want {
				t.Fatalf("normalizeSQLiteIdentityPathForOS(%q, %q, %q)=%q, want %q", tt.dsn, tt.goos, tt.cwd, got, tt.want)
			}
			if strings.Contains(got, "secret") || strings.Contains(got, "token") || strings.Contains(got, "password") {
				t.Fatalf("normalized path leaked DSN query: %q", got)
			}
		})
	}
}

func TestNormalizeSQLiteIdentityPathPreservesMemory(t *testing.T) {
	for _, dsn := range []string{`:memory:`, `file::memory:?cache=shared`, `file:task10?mode=memory&token=secret`} {
		if got := normalizeSQLiteIdentityPathForOS(dsn, "windows", `C:\app`); got != "memory" {
			t.Errorf("normalize memory %q=%q", dsn, got)
		}
	}
}

func TestNormalizeSQLiteIdentityPathDecodesFileURIExactlyOnce(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{name: "hierarchical encoded percent space", dsn: `file:///C:/Media/literal%2520name.db`, want: `C:\Media\literal%20name.db`},
		{name: "hierarchical encoded percent", dsn: `file:///C:/Media/rate%2525.db`, want: `C:\Media\rate%25.db`},
		{name: "opaque encoded percent space", dsn: `file:C:/Media/literal%2520name.db`, want: `C:\Media\literal%20name.db`},
		{name: "opaque encoded percent", dsn: `file:C:/Media/rate%2525.db`, want: `C:\Media\rate%25.db`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeSQLiteIdentityPathForOS(tt.dsn, "windows", `D:\work`); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLiteIdentityRefreshesRevisionsAndFailsAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live-identity.sqlite")
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	before, ok := SQLiteIdentity(db)
	if !ok {
		t.Fatal("missing initial identity")
	}
	if _, err := db.Exec(`CREATE TABLE identity_revision_probe(id INTEGER); PRAGMA user_version=37`); err != nil {
		t.Fatal(err)
	}
	after, ok := SQLiteIdentity(db)
	if !ok {
		t.Fatal("missing refreshed identity")
	}
	if after.SchemaRevision <= before.SchemaRevision {
		t.Fatalf("schema revision before=%d after=%d", before.SchemaRevision, after.SchemaRevision)
	}
	if after.UserRevision != 37 {
		t.Fatalf("user revision=%d", after.UserRevision)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if identity, ok := SQLiteIdentity(db); ok {
		t.Fatalf("closed identity=%+v ok=true", identity)
	}
}

func TestSQLiteIdentityUsesDatabaseListAndNoRegistry(t *testing.T) {
	for i := 0; i < 20; i++ {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("identity-%d.sqlite", i))
		db, err := OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		identity, ok := SQLiteIdentity(db)
		if !ok {
			t.Fatalf("identity %d missing", i)
		}
		absolute, _ := filepath.Abs(path)
		if filepath.Clean(identity.Path) != filepath.Clean(absolute) {
			t.Fatalf("path=%q want=%q", identity.Path, absolute)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWithSQLiteDiagnosticContextAddsOptionalScanFields(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "scan-context.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wrapped := WithSQLiteDiagnosticContext(fmt.Errorf("outer: %w", sqliteTestError(t, sqlite3.SQLITE_BUSY_SNAPSHOT)), db, "scan-owner", "scan_heartbeat", 4, 75*time.Millisecond, SQLiteDiagnosticContext{TaskID: 41, LibraryID: 9, RemainingLeaseBudget: 325 * time.Millisecond, HasRemainingLeaseBudget: true})
	var diagnostic SQLiteDiagnostic
	if !errors.As(wrapped, &diagnostic) {
		t.Fatalf("missing SQLiteDiagnostic: %v", wrapped)
	}
	if diagnostic.TaskID != 41 || diagnostic.LibraryID != 9 || diagnostic.RemainingLeaseBudget != 325*time.Millisecond {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
	fields := diagnostic.Fields()
	if fields["task_id"] != int64(41) || fields["library_id"] != int64(9) || fields["remaining_lease_budget"] != 325*time.Millisecond {
		t.Fatalf("fields=%v", fields)
	}
}

func TestSQLiteDiagnosticFieldsOmitMeaninglessOptionalValues(t *testing.T) {
	diagnostic := NewSQLiteDiagnostic(nil, errors.New("ordinary"), "owner", "op", time.Millisecond, 1)
	fields := diagnostic.Fields()
	for _, key := range []string{"task_id", "library_id", "remaining_lease_budget"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("fields unexpectedly contain %s: %v", key, fields)
		}
	}
}

func TestWithSQLiteDiagnosticContextAvoidsDuplicateSameOperation(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "dedupe.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	once := WithSQLiteDiagnosticContext(errors.New("failure"), db, "owner", "op", 1, time.Millisecond, SQLiteDiagnosticContext{TaskID: 1})
	twice := WithSQLiteDiagnosticContext(once, db, "owner", "op", 2, 2*time.Millisecond, SQLiteDiagnosticContext{TaskID: 1})
	count := 0
	for err := twice; err != nil; err = errors.Unwrap(err) {
		var diagnostic SQLiteDiagnostic
		if errors.As(err, &diagnostic) {
			count++
			break
		}
	}
	if twice != once || count != 1 {
		t.Fatalf("duplicate wrap: once=%v twice=%v count=%d", once, twice, count)
	}
}

func TestSQLiteDiagnosticExplicitZeroRemainingBudgetIsPresent(t *testing.T) {
	err := WithSQLiteDiagnosticContext(errors.New("deadline exhausted"), nil, "owner", "heartbeat", 1, 0, SQLiteDiagnosticContext{HasRemainingLeaseBudget: true})
	var diagnostic SQLiteDiagnostic
	if !errors.As(err, &diagnostic) {
		t.Fatalf("missing diagnostic: %v", err)
	}
	fields := diagnostic.Fields()
	if got, ok := fields["remaining_lease_budget"]; !ok || got != time.Duration(0) {
		t.Fatalf("remaining field=%v present=%v fields=%v", got, ok, fields)
	}
	if !strings.Contains(diagnostic.Error(), "remaining_lease_budget=0s") {
		t.Fatalf("error=%q", diagnostic.Error())
	}
}
