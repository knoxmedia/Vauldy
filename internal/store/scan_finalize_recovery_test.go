package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenSQLiteCreatesScanFinalizeRecoverySchema(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "scan-finalize-recovery.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := []string{"task_id", "library_id", "owner_id", "desired_status", "next_available_at", "claim_owner", "claim_until"}
	for _, column := range want {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('scan_finalize_recovery') WHERE name=?`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("scan_finalize_recovery.%s count=%d want 1", column, count)
		}
	}
}

func TestOpenSQLiteVerifiesPragmasAndPool(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pragmas.sqlite")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite err: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout, foreignKeys int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if busyTimeout != 30000 {
		t.Errorf("busy_timeout = %d, want 30000", busyTimeout)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	if got := db.Stats().MaxOpenConnections; got != 8 {
		t.Errorf("MaxOpenConnections = %d, want 8", got)
	}
}

func TestOpenSQLiteMemoryUsesSingleConnection(t *testing.T) {
	db, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite err: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stats := db.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
	if stats.Idle != 1 {
		t.Fatalf("Idle = %d, want 1", stats.Idle)
	}
	if _, err := db.Exec(`INSERT INTO library (name, type, path) VALUES ('memory', 'video', '/memory')`); err != nil {
		t.Fatalf("insert through pooled handle: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM library`).Scan(&count); err != nil {
		t.Fatalf("query through pooled handle: %v", err)
	}
	if count != 1 {
		t.Fatalf("library count = %d, want 1", count)
	}
}

func TestOpenSQLiteConfiguresPragmasOnEveryFileConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "connection-pragmas.sqlite")
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite err: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	connections := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		connections = append(connections, conn)
	}
	for i, conn := range connections {
		var busyTimeout, foreignKeys, synchronous int
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil {
			t.Fatalf("connection %d synchronous: %v", i, err)
		}
		if busyTimeout != 30000 || foreignKeys != 1 || synchronous != 1 {
			t.Errorf("connection %d pragmas = busy_timeout:%d foreign_keys:%d synchronous:%d, want 30000/1/1", i, busyTimeout, foreignKeys, synchronous)
		}
	}
}

func TestOpenSQLitePreservesExistingFileURIParameters(t *testing.T) {
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "existing-query.sqlite"))
	dsn := "file:" + dbPath + "?mode=rwc&cache=private"
	db, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("OpenSQLite file URI: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := db.Stats().MaxOpenConnections; got != 8 {
		t.Errorf("MaxOpenConnections = %d, want 8", got)
	}
	var journalMode string
	var busyTimeout, foreignKeys, synchronous int
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if journalMode != "wal" || busyTimeout != 30000 || foreignKeys != 1 || synchronous != 1 {
		t.Fatalf("pragmas = journal:%s busy:%d foreign:%d sync:%d, want wal/30000/1/1", journalMode, busyTimeout, foreignKeys, synchronous)
	}
}
