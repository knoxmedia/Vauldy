package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func TestOrdinaryRecoveryExcludesOnlyCurrentJournal(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	_, _ = db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID)
	_, cleaned, e := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
	if e != nil || cleaned < 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, e)
	}
	if _, e = os.Stat(staged.Path); !os.IsNotExist(e) {
		t.Fatalf("current ref retained=%v", e)
	}
}
func TestOrdinaryRecoveryRetainsOtherJournalRef(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) SELECT 'other-ref',media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,'committed',staged_path,hashes_sizes_json FROM media_asset_stage_journal WHERE stage_id=?`, staged.Stage.StageID)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID)
	_, cleaned, e := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
	if e != nil || cleaned != 0 {
		t.Fatalf("cleaned=%d err=%v", cleaned, e)
	}
	if _, e = os.Stat(staged.Path); e != nil {
		t.Fatal(e)
	}
}
func TestRecoveryBudgetsRepairDespiteOrdinarySaturation(t *testing.T) {
	db, upload, task, runner, req := seedRepairPosterStage(t)
	staged, e := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	var stepID int64
	if e = db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? LIMIT 1`, *task.RunID).Scan(&stepID); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 100; i++ {
		stageID := string(rune(0x1000 + i))
		path := filepath.Join(upload, "posters", "generation-1", stageID, "poster.jpg")
		hashes, _ := json.Marshal(map[string]any{"path": path})
		_, e = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged',?,?)`, stageID, task.MediaID, *task.RunID, stepID, task.Generation, task.LeaseOwner, req.SourceFingerprint, filepath.Dir(path), string(hashes))
		if e != nil {
			t.Fatal(e)
		}
	}
	_, _ = db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID)
	_, cleaned, e := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
	if e != nil || cleaned < 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, e)
	}
	if _, e = os.Stat(staged.Path); !os.IsNotExist(e) {
		t.Fatalf("repair starved=%v", e)
	}
}
func TestCommitHashesBeforeImmediateTransaction(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	ot, oh, of := withImmediatePosterTx, posterHashPath, posterSourceFingerprint
	inside := false
	calls := 0
	posterHashPath = func(p string) (int64, string, error) {
		if inside {
			t.Fatal("hash in tx")
		}
		calls++
		return hashPath(p)
	}
	posterSourceFingerprint = func(p string) (string, error) {
		if inside {
			t.Fatal("fingerprint in tx")
		}
		calls++
		return sourceFingerprint(p)
	}
	withImmediatePosterTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.WithImmediateConnTx(ctx, d, func(tx store.ImmediateConnTx) error { inside = true; defer func() { inside = false }(); return fn(tx) })
	}
	t.Cleanup(func() { withImmediatePosterTx = ot; posterHashPath = oh; posterSourceFingerprint = of })
	if e = commitStagedPoster(context.Background(), db, task, staged); e != nil {
		t.Fatal(e)
	}
	if calls < 2 {
		t.Fatalf("calls=%d", calls)
	}
}
