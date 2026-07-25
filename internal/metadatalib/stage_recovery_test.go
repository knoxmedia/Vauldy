package metadatalib

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestScrapeArtworkRecoveryTerminalizesUnsafeAndContinues(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "unsafe")
	if err = os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	safe := filepath.Join(root, "safe")
	if err = os.MkdirAll(safe, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(safe, "poster.jpg"), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, path string }{{"a-unsafe", outside}, {"z-safe", safe}} {
		if _, err = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json,updated_at) VALUES(?,1,1,1,1,'owner','fp','scrape_artwork','quarantined',?,'{}',datetime(CURRENT_TIMESTAMP,'-20 minutes'))`, row.id, row.path); err != nil {
			t.Fatal(err)
		}
	}
	n, err := ReconcileScrapeArtworkStages(context.Background(), db, root, 100)
	if err != nil || n != 1 {
		t.Fatalf("cleaned=%d err=%v", n, err)
	}
	var state, marker string
	if err = db.QueryRow(`SELECT state,recovery_error FROM media_asset_stage_journal WHERE stage_id='a-unsafe'`).Scan(&state, &marker); err != nil {
		t.Fatal(err)
	}
	if state != "failed_closed" || marker != "failed_closed:unsafe_path" {
		t.Fatalf("state=%s marker=%q", state, marker)
	}
	if _, err = os.Stat(outside); err != nil {
		t.Fatalf("unsafe path touched: %v", err)
	}
	n, err = ReconcileScrapeArtworkStages(context.Background(), db, root, 100)
	if err != nil || n != 0 {
		t.Fatalf("repeat cleaned=%d err=%v", n, err)
	}
}
