package store_test

import (
	"testing"

	"knox-media/internal/store"
)

func storeEnterprisePrepareReady(t *testing.T) bool {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var meta, jobs int
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='pretranscode_task_meta'), EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='pretranscode_rendition_job')`).Scan(&meta, &jobs); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return meta == 1 && jobs == 1
}
