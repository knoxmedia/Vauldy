package postingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type fixedStageRoot string

func (r fixedStageRoot) ResolveEncryptionStageRoot(context.Context, int64, string) (string, error) {
	return string(r), nil
}

func TestReconcileEncryptionStagesTerminalRowsDoNotStarveActionable(t *testing.T) {
	db, _ := openQueueTestDB(t)

	if _, e := db.Exec(`PRAGMA foreign_keys=OFF`); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	quarantine := t.TempDir()
	source := filepath.Join(root, "source.jpg")
	if e := os.WriteFile(source, []byte("plain"), 0600); e != nil {
		t.Fatal(e)
	}
	res, e := db.Exec(`INSERT INTO library(name,type,path) VALUES('recover','photo',?)`, root)
	if e != nil {
		t.Fatal(e)
	}
	libraryID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,ingest_generation) VALUES(?,'recover',?,'image','active','processing',1)`, libraryID, source)
	if e != nil {
		t.Fatal(e)
	}
	mediaID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,1,'repair','processing','{}',2)`, mediaID)
	if e != nil {
		t.Fatal(e)
	}
	runID, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,status,required) VALUES(?,?,1,'encrypt','running',1)`, runID, mediaID)
	if e != nil {
		t.Fatal(e)
	}
	stepID, _ := res.LastInsertId()
	insert := func(i int, state, marker, updated string) string {
		t.Helper()
		res, e := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts) VALUES(?,?,?,?,'encrypt','failed',?,3)`, mediaID, runID, stepID, i+2, i+1)
		if e != nil {
			t.Fatal(e)
		}
		taskID, _ := res.LastInsertId()
		stage := fmt.Sprintf("10000000-0000-0000-0000-%012d", i)
		enc := filepath.Join(root, stage+".enc")
		if e = os.WriteFile(enc, []byte("enc"), 0600); e != nil {
			t.Fatal(e)
		}
		_, e = db.Exec(`INSERT INTO media_encryption_stage_journal(stage_id,task_id,attempt,media_id,run_id,step_id,generation,owner_token,source_path,source_fingerprint,enc_path,wrapped_dek,iv,enc_sha256,enc_size,state,recovery_error,updated_at) VALUES(?,?,?,?,?,?,?,'owner',?,'fp',?,'wrapped','iv','hash',3,?,?,?)`, stage, taskID, i+1, mediaID, runID, stepID, i+2, source, enc, state, marker, updated)
		if e != nil {
			t.Fatal(e)
		}
		return stage
	}
	for i := 0; i < 101; i++ {
		insert(i, "restored", "stale_restored", "2000-01-01 00:00:00")
	}
	actionable := insert(101, "staged", "", "2099-01-01 00:00:00")
	roots := EncryptionRecoveryRoots{Quarantine: quarantine, Resolver: fixedStageRoot(root)}
	checked, cleaned, e := ReconcileEncryptionStages(context.Background(), db, roots, 100)
	if e != nil || checked != 1 || cleaned != 1 {
		t.Fatalf("checked=%d cleaned=%d err=%v", checked, cleaned, e)
	}
	var state, marker string
	if e = db.QueryRow(`SELECT state,recovery_error FROM media_encryption_stage_journal WHERE stage_id=?`, actionable).Scan(&state, &marker); e != nil {
		t.Fatal(e)
	}
	if state != "restored" || marker != "stale_restored" {
		t.Fatalf("state=%s marker=%s", state, marker)
	}
	checked, cleaned, e = ReconcileEncryptionStages(context.Background(), db, roots, 100)
	if e != nil || checked != 0 || cleaned != 0 {
		t.Fatalf("repeat checked=%d cleaned=%d err=%v", checked, cleaned, e)
	}
}
