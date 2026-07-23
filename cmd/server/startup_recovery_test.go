package main

import (
	"context"
	"knox-media/internal/postingest"
	"knox-media/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverStartupTasksCleansScrapeStagesBeforeQueueReset(t *testing.T) {
	db, e := store.OpenSQLite(filepath.Join(t.TempDir(), "r.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	root := t.TempDir()
	_, _ = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');INSERT INTO media(id,library_id,file_id,file_type,ingest_generation) VALUES(1,1,'f','video',1);INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json) VALUES(1,1,1,'scan','processing','{}');INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(1,1,1,1,'scrape',0,'running')`)
	stale := filepath.Join(root, "stale")
	_ = os.MkdirAll(stale, 0755)
	_ = os.WriteFile(filepath.Join(stale, "poster.jpg"), []byte("x"), 0644)
	_, _ = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('stale',1,1,1,1,'dead','fp','scrape_artwork','staged',?,'{}')`, stale)
	if e = recoverStartupTasks(context.Background(), db, postingest.NewQueue(db, "r", nil), StartupRecoveryRoots{ScrapeArtwork: root}); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(stale); !os.IsNotExist(e) {
		t.Fatalf("stale retained: %v", e)
	}
}
