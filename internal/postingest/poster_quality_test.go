package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/keystore"
	"knox-media/internal/storage"
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
	if e = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); e != nil {
		t.Fatal(e)
	}
	if calls < 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestIdempotentPosterCommitPreverifiesBeforeImmediateTransaction(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	origTx, origHash := withImmediatePosterTx, posterHashPath
	inside := false
	calls := 0
	posterHashPath = func(p string) (int64, string, error) {
		if inside {
			t.Fatal("idempotent hash in tx")
		}
		calls++
		return hashPath(p)
	}
	withImmediatePosterTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.WithImmediateConnTx(ctx, d, func(tx store.ImmediateConnTx) error { inside = true; defer func() { inside = false }(); return fn(tx) })
	}
	t.Cleanup(func() { withImmediatePosterTx = origTx; posterHashPath = origHash })
	if e = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); e != nil {
		t.Fatal(e)
	}
	if calls < 1 {
		t.Fatalf("hash calls=%d", calls)
	}
}

func TestPosterCommitRejectsMutationAfterPrehash(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	orig := posterAfterSealHook
	posterAfterSealHook = func() {
		_ = os.Chmod(storage.PosterObjectPath(upload, staged.Hash, ".jpg"), 0644)
		_ = os.WriteFile(storage.PosterObjectPath(upload, staged.Hash, ".jpg"), []byte("changed-poster"), 0644)
	}
	t.Cleanup(func() { posterAfterSealHook = orig })
	if e = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); e == nil || (!strings.Contains(e.Error(), "staged stat changed") && !strings.Contains(e.Error(), "hash/size mismatch")) {
		t.Fatalf("err=%v", e)
	}
}

func TestPosterPrehashRejectsSameSizeMtimeMutation(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(staged.Path)
	if e != nil {
		t.Fatal(e)
	}
	original, _ := os.ReadFile(staged.Path)
	changed := append([]byte(nil), original...)
	changed[0] ^= 0x1
	if e = os.WriteFile(staged.Path, changed, 0644); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(staged.Path, st.ModTime(), st.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); e == nil || !strings.Contains(e.Error(), "hash/size mismatch") {
		t.Fatalf("err=%v", e)
	}
}

func TestPlainPosterCommitSealsContentAddressedObject(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	temp := staged.Path
	if e = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); e != nil {
		t.Fatal(e)
	}
	resolved := storage.ResolvePosterServePath(db, upload, task.MediaID)
	if !strings.Contains(filepath.ToSlash(resolved), "/posters/objects/sha256/") {
		t.Fatalf("resolved=%s", resolved)
	}
	size, hash, e := hashPath(resolved)
	if e != nil || size != staged.Size || hash != staged.Hash {
		t.Fatalf("size=%d hash=%s err=%v", size, hash, e)
	}
	if sameResolvedPath(temp, resolved) {
		t.Fatal("generation temp selected")
	}
	var refs string
	if e = db.QueryRow(`SELECT artifact_refs_json FROM media_ingest_evidence WHERE stage_id=?`, staged.Stage.StageID).Scan(&refs); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(refs, hash) {
		t.Fatalf("refs=%s", refs)
	}
}
func TestPosterSealIgnoresMutationAfterSeal(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	orig := posterAfterSealHook
	posterAfterSealHook = func() { _ = os.WriteFile(staged.Path, []byte("mutated-after"), 0644) }
	t.Cleanup(func() { posterAfterSealHook = orig })
	if e = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); e != nil {
		t.Fatal(e)
	}
	resolved := storage.ResolvePosterServePath(db, upload, task.MediaID)
	_, hash, e := hashPath(resolved)
	if e != nil || hash != staged.Hash {
		t.Fatalf("hash=%s err=%v", hash, e)
	}
}
func TestPosterSealRejectsMutationBeforeCopy(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	orig := posterBeforeSealHook
	posterBeforeSealHook = func() { _ = os.WriteFile(staged.Path, []byte("mutated-before"), 0644) }
	t.Cleanup(func() { posterBeforeSealHook = orig })
	if e = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); e == nil {
		t.Fatal("mutation accepted")
	}
}
func TestPosterSealReusesOnlyVerifiedObject(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	object := storage.PosterObjectPath(upload, staged.Hash, ".jpg")
	if e = os.MkdirAll(filepath.Dir(object), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(object, []byte("wrong-object"), 0644); e != nil {
		t.Fatal(e)
	}
	if e = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); e == nil {
		t.Fatal("corrupt existing object reused")
	}
}
func TestEncryptedPosterCommitUsesExactUniqueDerivedIdentity(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	if _, e := db.Exec(`UPDATE library SET encrypted_assets_enabled=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, task.MediaID); e != nil {
		t.Fatal(e)
	}
	vault, e := keystore.NewVault("poster-encrypted-test", "")
	if e != nil {
		t.Fatal(e)
	}
	runner := realPosterStageRunner(t, db, upload)
	runner.Derived = &storage.DerivedAssetStore{DB: db, Vault: vault, BaseDir: filepath.Join(t.TempDir(), "derived")}
	first, e := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	if first.Derived == nil || strings.Contains(filepath.ToSlash(first.Path), "/posters/objects/") {
		t.Fatalf("path=%s", first.Path)
	}
	if e = commitStagedPoster(context.Background(), db, task, first, PosterRecoveryRoots{Upload: upload, Derived: runner.Derived.BaseDir}); e != nil {
		t.Fatal(e)
	}
	var path string
	if e = db.QueryRow(`SELECT enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind='poster'`, task.MediaID).Scan(&path); e != nil || !sameResolvedPath(path, first.Path) {
		t.Fatalf("path=%s want=%s err=%v", path, first.Path, e)
	}
	if e = os.WriteFile(first.Path, []byte("replacement"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = commitStagedPoster(context.Background(), db, task, first, PosterRecoveryRoots{Upload: upload}); e == nil {
		t.Fatal("modified encrypted artifact reused")
	}
}

func TestPosterRecoveryTerminalizesMalformedThenCleansLaterOrdinary(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
	goodDir := filepath.Join(upload, "posters", "generation-1", "z-good")
	goodPath := filepath.Join(goodDir, "poster.jpg")
	if e := os.MkdirAll(goodDir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(goodPath, []byte("good"), 0600); e != nil {
		t.Fatal(e)
	}
	goodJSON, _ := json.Marshal(map[string]any{"path": goodPath})
	for _, row := range []struct{ id, hashes, dir string }{{"a-malformed", "{}", filepath.Join(upload, "posters", "generation-1", "a-malformed")}, {"z-good", string(goodJSON), goodDir}} {
		if _, e := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','quarantined',?,?)`, row.id, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, row.dir, row.hashes); e != nil {
			t.Fatal(e)
		}
	}
	_, _ = db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID)
	checked, cleaned, e := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
	if e != nil || checked < 2 || cleaned != 1 {
		t.Fatalf("checked=%d cleaned=%d err=%v", checked, cleaned, e)
	}
	var state, marker string
	if e = db.QueryRow(`SELECT state,recovery_error FROM media_asset_stage_journal WHERE stage_id='a-malformed'`).Scan(&state, &marker); e != nil {
		t.Fatal(e)
	}
	if state != "failed_closed" || !strings.HasPrefix(marker, "failed_closed:") {
		t.Fatalf("state=%s marker=%q", state, marker)
	}
	checked, cleaned, e = ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
	if e != nil || checked != 0 || cleaned != 0 {
		t.Fatalf("repeat checked=%d cleaned=%d err=%v", checked, cleaned, e)
	}
}

func TestPosterRecoveryTerminalizesUnsafeThenCleansLaterRepair(t *testing.T) {
	db, upload, task, runner, req := seedRepairPosterStage(t)
	good, e := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	external := filepath.Join(t.TempDir(), "sentinel.jpg")
	if e = os.WriteFile(external, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	badJSON, _ := json.Marshal(map[string]any{"path": external})
	if _, e = db.Exec(`INSERT INTO poster_repair_stage(stage_id,queue_id,media_id,run_id,generation,owner_token,attempt,source_fingerprint,state,staged_path,hashes_sizes_json) VALUES('a-unsafe',?,?,?,?,?,?,?,'quarantined',?,?)`, task.ID+100, task.MediaID, *task.RunID, task.Generation, task.LeaseOwner, task.Attempts+10, req.SourceFingerprint, filepath.Dir(external), string(badJSON)); e != nil {
		t.Fatal(e)
	}
	_, _ = db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID)
	checked, cleaned, e := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
	if e != nil || checked < 2 || cleaned != 1 {
		t.Fatalf("checked=%d cleaned=%d err=%v", checked, cleaned, e)
	}
	if got, _ := os.ReadFile(external); string(got) != "keep" {
		t.Fatalf("unsafe path deleted: %q", got)
	}
	var state, marker string
	if e = db.QueryRow(`SELECT state,recovery_error FROM poster_repair_stage WHERE stage_id='a-unsafe'`).Scan(&state, &marker); e != nil {
		t.Fatal(e)
	}
	if state != "failed_closed" || !strings.HasPrefix(marker, "failed_closed:") {
		t.Fatalf("state=%s marker=%q", state, marker)
	}
	if _, e = os.Stat(good.Path); !os.IsNotExist(e) {
		t.Fatalf("later repair retained: %v", e)
	}
	checked, cleaned, e = ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
	if e != nil || checked != 0 || cleaned != 0 {
		t.Fatalf("repeat checked=%d cleaned=%d err=%v", checked, cleaned, e)
	}
}
