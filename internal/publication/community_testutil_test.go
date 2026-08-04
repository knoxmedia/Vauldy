package publication

import (
	"context"
	"database/sql"
	"testing"

	"knox-media/internal/store"
)

func enterprisePrepareTablesPresent(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var meta, jobs int
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='pretranscode_task_meta'), EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='pretranscode_rendition_job')`).Scan(&meta, &jobs); err != nil {
		t.Fatalf("inspect prepare tables: %v", err)
	}
	return meta == 1 && jobs == 1
}

func skipIfEnterprisePrepareUnavailable(t *testing.T) {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if !enterprisePrepareTablesPresent(t, db) {
		t.Skip("enterprise prepare tables unavailable in community build")
	}
}

func requireEnterprisePrepareTables(t *testing.T, db *sql.DB) {
	t.Helper()
	if !enterprisePrepareTablesPresent(t, db) {
		if err := db.Close(); err != nil {
			t.Logf("close before skip: %v", err)
		}
		t.Skip("enterprise prepare tables unavailable in community build")
	}
}

func dropEnterprisePrepareTablesIfPresent(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"pretranscode_rendition_job", "pretranscode_task_meta"} {
		if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
}
