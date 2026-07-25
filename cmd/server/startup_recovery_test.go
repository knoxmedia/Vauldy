package main

import (
	"context"
	"knox-media/internal/postingest"
	"knox-media/internal/store"
	"os"
	"path/filepath"
	"testing"
)

type startupRecoveryStageRoot string

func (r startupRecoveryStageRoot) ResolveEncryptionStageRoot(context.Context, int64, string) (string, error) {
	return string(r), nil
}

func startupRecoveryRoots(t *testing.T, scrapeArtwork string) StartupRecoveryRoots {
	t.Helper()
	root := t.TempDir()
	return StartupRecoveryRoots{
		Encryption: postingest.EncryptionRecoveryRoots{
			Quarantine: filepath.Join(root, "encryption-quarantine"),
			Resolver:   startupRecoveryStageRoot(filepath.Join(root, "encryption-stages")),
		},
		Thumbnail: postingest.ThumbnailRecoveryRoots{
			Preview: filepath.Join(root, "previews"),
			Derived: filepath.Join(root, "derived"),
		},
		Poster: postingest.PosterRecoveryRoots{
			Upload:  filepath.Join(root, "uploads"),
			Derived: filepath.Join(root, "derived"),
		},
		ScrapeArtwork: scrapeArtwork,
	}
}

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
	_, _ = db.Exec(`UPDATE media_asset_stage_journal SET updated_at=datetime(CURRENT_TIMESTAMP,'-11 minutes') WHERE stage_id='stale'`)
	if e = recoverStartupTasks(context.Background(), db, postingest.NewQueue(db, "r", nil), startupRecoveryRoots(t, root)); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(stale); !os.IsNotExist(e) {
		t.Fatalf("stale retained: %v", e)
	}
}
