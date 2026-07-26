package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"knox-media/internal/keystore"
	"knox-media/internal/publication"
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
func TestCommitStagedPosterDoesNotRehashFullSource(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	original := posterSourceFingerprint
	calls := 0
	posterSourceFingerprint = func(context.Context, string) (string, error) {
		calls++
		return "", errors.New("duplicate full source fingerprint")
	}
	t.Cleanup(func() { posterSourceFingerprint = original })

	if err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("source fingerprint calls during commit=%d want 0", calls)
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
	posterSourceFingerprint = func(ctx context.Context, p string) (string, error) {
		if inside {
			t.Fatal("fingerprint in tx")
		}
		calls++
		return publication.SourceFingerprintContext(ctx, p)
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

func TestParsePosterSourceFingerprintParsesSuffixFromRight(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("source|sha256:segment", "video.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	raw := path + "|12|34|sha256:" + strings.Repeat("a", 64)
	got, err := parsePosterSourceFingerprint(raw, path)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != path || got.size != 12 || got.mtime != 34 {
		t.Fatalf("got=%+v", got)
	}
}

func TestPosterCommitRejectsSourceMutationSinceFingerprintAndCleansSeal(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	source := staged.Stage.Request.SourcePath
	if err = os.WriteFile(source, []byte("source changed after fingerprint and staging"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
	if err == nil || !strings.Contains(err.Error(), "poster commit: source stat differs from fingerprint") {
		t.Fatalf("err=%v", err)
	}
	assertNoPosterObjectOrQuarantine(t, upload)
}

func TestPosterCommitRejectsMalformedSourceFingerprintAndCleansSeal(t *testing.T) {
	for _, fingerprint := range []string{"", "malformed", "C:\\video.mp4|12|34|sha256:short", "C:\\video.mp4|bad|34|sha256:" + strings.Repeat("a", 64)} {
		t.Run(fingerprint, func(t *testing.T) {
			db, upload, task := seedCurrentLinkedPosterTask(t)
			runner := realPosterStageRunner(t, db, upload)
			staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
			if err != nil {
				t.Fatal(err)
			}
			staged.Stage.Request.SourceFingerprint = fingerprint
			err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
			if err == nil || (!strings.Contains(err.Error(), "missing source fingerprint") && !strings.Contains(err.Error(), "malformed source fingerprint")) {
				t.Fatalf("err=%v", err)
			}
			assertNoPosterObjectOrQuarantine(t, upload)
			if _, statErr := os.Stat(staged.Stage.StagedPath); !os.IsNotExist(statErr) {
				t.Fatalf("staging directory leaked: %v", statErr)
			}
			var actionable int
			if queryErr := db.QueryRow(`SELECT COUNT(*) FROM media_asset_stage_journal WHERE stage_id=? AND state IN ('staged','quarantined')`, staged.Stage.StageID).Scan(&actionable); queryErr != nil {
				t.Fatal(queryErr)
			}
			if actionable != 0 {
				t.Fatalf("actionable journal rows=%d", actionable)
			}
		})
	}
}

func TestPosterCommitCorruptFingerprintCannotDeleteAnotherStage(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	victim, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	attacker := victim
	attacker.Stage.Request.SourceFingerprint = "malformed"
	attacker.Stage.StagedPath = filepath.Join(upload, "posters", "generation-1", "different-stage")
	attacker.Path = filepath.Join(attacker.Stage.StagedPath, posterLogicalName)

	err = commitStagedPoster(context.Background(), db, task, attacker, PosterRecoveryRoots{Upload: upload})
	if err == nil {
		t.Fatal("corrupt request accepted")
	}
	if _, statErr := os.Stat(victim.Path); statErr != nil {
		t.Fatalf("other stage deleted: %v", statErr)
	}
	var state string
	if queryErr := db.QueryRow(`SELECT state FROM media_asset_stage_journal WHERE stage_id=?`, victim.Stage.StageID).Scan(&state); queryErr != nil || state != "staged" {
		t.Fatalf("victim journal state=%q err=%v", state, queryErr)
	}
}

type posterSymlinkFileInfo struct{ os.FileInfo }

func (v posterSymlinkFileInfo) Mode() os.FileMode { return v.FileInfo.Mode() | os.ModeSymlink }

func TestCleanupUnreferencedDerivedPosterWithoutPlaintext(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(staged.Stage.StagedPath, posterLogicalName)
	if err = os.Remove(plain); err != nil {
		t.Fatal(err)
	}
	derivedRoot := t.TempDir()
	derivedDir := filepath.Join(derivedRoot, fmt.Sprintf("%d", task.MediaID), posterKind)
	if err = os.MkdirAll(derivedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	derivedPath := filepath.Join(derivedDir, "poster.jpg.cleanup.enc")
	if err = os.WriteFile(derivedPath, []byte("derived cleanup"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged.Path = derivedPath
	staged.Derived = storage.RestoreStagedDerivedAsset(task.MediaID, posterKind, posterLogicalName, derivedPath, "wrapped", "iv")
	if err = cleanupUnreferencedPoster(context.Background(), db, staged, PosterRecoveryRoots{Upload: upload, Derived: derivedRoot}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(derivedPath); !os.IsNotExist(statErr) {
		t.Fatalf("derived artifact retained: %v", statErr)
	}
	if _, statErr := os.Stat(staged.Stage.StagedPath); !os.IsNotExist(statErr) {
		t.Fatalf("stage directory retained: %v", statErr)
	}
	var count int
	if queryErr := db.QueryRow(`SELECT COUNT(*) FROM media_asset_stage_journal WHERE stage_id=?`, staged.Stage.StageID).Scan(&count); queryErr != nil || count != 0 {
		t.Fatalf("journal count=%d err=%v", count, queryErr)
	}
}

func TestPosterCommitRejectsDerivedJunctionEscapeWithoutDeletingSentinel(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junction integration")
	}
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	derivedRoot := t.TempDir()
	external := t.TempDir()
	sentinel := filepath.Join(external, "poster.jpg.test.enc")
	if err = os.WriteFile(sentinel, []byte("junction sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	mediaDir := filepath.Join(derivedRoot, fmt.Sprintf("%d", task.MediaID))
	if err = os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kindDir := filepath.Join(mediaDir, posterKind)
	if output, linkErr := exec.Command("cmd", "/c", "mklink", "/J", kindDir, external).CombinedOutput(); linkErr != nil {
		t.Skipf("junction unavailable: %v: %s", linkErr, output)
	}
	staged.Path = filepath.Join(kindDir, filepath.Base(sentinel))
	staged.Derived = storage.RestoreStagedDerivedAsset(task.MediaID, posterKind, posterLogicalName, staged.Path, "wrapped", "iv")
	staged.Size, staged.Hash, err = hashPath(staged.Path)
	if err != nil {
		t.Fatal(err)
	}
	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload, Derived: derivedRoot})
	if err == nil || !strings.Contains(err.Error(), "derived") {
		t.Fatalf("err=%v", err)
	}
	if got, readErr := os.ReadFile(sentinel); readErr != nil || string(got) != "junction sentinel" {
		t.Fatalf("sentinel=%q err=%v", got, readErr)
	}
	var state string
	if queryErr := db.QueryRow(`SELECT state FROM media_asset_stage_journal WHERE stage_id=?`, staged.Stage.StageID).Scan(&state); queryErr != nil || state != "staged" {
		t.Fatalf("journal state=%q err=%v", state, queryErr)
	}
}

func TestPosterCommitRejectsDerivedSymlinkEscapeWithoutDeletingSentinel(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	derivedRoot := t.TempDir()
	external := t.TempDir()
	sentinel := filepath.Join(external, "poster.jpg.test.enc")
	if err = os.WriteFile(sentinel, []byte("external derived sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	mediaDir := filepath.Join(derivedRoot, fmt.Sprintf("%d", task.MediaID))
	if err = os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kindDir := filepath.Join(mediaDir, posterKind)
	if err = os.Symlink(external, kindDir); err != nil {
		t.Skipf("derived symlink unavailable: %v", err)
	}
	staged.Path = filepath.Join(kindDir, filepath.Base(sentinel))
	staged.Derived = storage.RestoreStagedDerivedAsset(task.MediaID, posterKind, posterLogicalName, staged.Path, "wrapped", "iv")
	staged.Size, staged.Hash, err = hashPath(staged.Path)
	if err != nil {
		t.Fatal(err)
	}

	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload, Derived: derivedRoot})
	if err == nil || !strings.Contains(err.Error(), "derived") {
		t.Fatalf("err=%v", err)
	}
	if got, readErr := os.ReadFile(sentinel); readErr != nil || string(got) != "external derived sentinel" {
		t.Fatalf("sentinel=%q err=%v", got, readErr)
	}
	var state string
	if queryErr := db.QueryRow(`SELECT state FROM media_asset_stage_journal WHERE stage_id=?`, staged.Stage.StageID).Scan(&state); queryErr != nil || state != "staged" {
		t.Fatalf("journal state=%q err=%v", state, queryErr)
	}
}

func TestValidateStagedPosterIdentityRejectsDerivedLinkedComponent(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	derivedRoot := t.TempDir()
	derivedDir := filepath.Join(derivedRoot, fmt.Sprintf("%d", task.MediaID), posterKind)
	if err = os.MkdirAll(derivedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	derivedPath := filepath.Join(derivedDir, "poster.jpg.test.enc")
	if err = os.WriteFile(derivedPath, []byte("derived"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged.Path = derivedPath
	staged.Derived = storage.RestoreStagedDerivedAsset(task.MediaID, posterKind, posterLogicalName, derivedPath, "wrapped", "iv")
	original := posterLstat
	posterLstat = func(path string) (os.FileInfo, error) {
		info, statErr := os.Lstat(path)
		if statErr == nil && sameResolvedPath(path, filepath.Dir(derivedPath)) {
			return posterSymlinkFileInfo{info}, nil
		}
		return info, statErr
	}
	t.Cleanup(func() { posterLstat = original })
	if err = validateStagedPosterIdentity(task, staged, PosterRecoveryRoots{Upload: upload, Derived: derivedRoot}); err == nil || !strings.Contains(err.Error(), "derived") {
		t.Fatalf("err=%v", err)
	}
}

func TestPosterPathComponentLinkedHookClassifiesReparse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "component")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	original := posterPathPlatformLinked
	posterPathPlatformLinked = func(string, os.FileInfo) bool { return true }
	t.Cleanup(func() { posterPathPlatformLinked = original })
	if !posterPathComponentLinked(path, info) {
		t.Fatal("platform reparse classification ignored")
	}
}

func TestValidateStagedPosterIdentityRejectsSymlinkComponent(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	original := posterLstat
	posterLstat = func(path string) (os.FileInfo, error) {
		info, statErr := os.Lstat(path)
		if statErr == nil && sameResolvedPath(path, staged.Stage.StagedPath) {
			return posterSymlinkFileInfo{info}, nil
		}
		return info, statErr
	}
	t.Cleanup(func() { posterLstat = original })
	if err = validateStagedPosterIdentity(task, staged, PosterRecoveryRoots{Upload: upload}); err == nil || !strings.Contains(err.Error(), "unsafe staged path") {
		t.Fatalf("err=%v", err)
	}
}

func TestCleanupUnreferencedPosterRejectsSymlinkStageComponent(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	original := posterLstat
	posterLstat = func(path string) (os.FileInfo, error) {
		info, statErr := os.Lstat(path)
		if statErr == nil && sameResolvedPath(path, staged.Stage.StagedPath) {
			return posterSymlinkFileInfo{info}, nil
		}
		return info, statErr
	}
	t.Cleanup(func() { posterLstat = original })
	if err = cleanupUnreferencedPoster(context.Background(), db, staged, PosterRecoveryRoots{Upload: upload}); err == nil || !strings.Contains(err.Error(), "unsafe staged path") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(staged.Path); statErr != nil {
		t.Fatalf("artifact removed: %v", statErr)
	}
	var state string
	if queryErr := db.QueryRow(`SELECT state FROM media_asset_stage_journal WHERE stage_id=?`, staged.Stage.StageID).Scan(&state); queryErr != nil || state != "staged" {
		t.Fatalf("journal state=%q err=%v", state, queryErr)
	}
}

func TestPosterCommitRejectsSymlinkStageEscapeWithoutDeletingSentinel(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	stageID := "symlink-stage"
	external := t.TempDir()
	sentinel := filepath.Join(external, posterLogicalName)
	if err := os.WriteFile(sentinel, []byte("external sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	generationDir := filepath.Join(upload, "posters", "generation-1")
	if err := os.MkdirAll(generationDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(generationDir, stageID)
	if err := os.Symlink(external, stageDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	req := posterRequest(t, db, task)
	size, hash, err := hashPath(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	staged := StagedPoster{Stage: publication.StageRecord{StageID: stageID, Request: req, Kind: publication.ArtifactPoster, State: "staged", StagedPath: stageDir}, Path: sentinel, URL: storage.ImmutablePlainPosterURL(task.Generation, stageID), Size: size, Hash: hash}
	if _, err = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged',?,'{}')`, stageID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, req.SourceFingerprint, stageDir); err != nil {
		t.Fatal(err)
	}

	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
	if err == nil || !strings.Contains(err.Error(), "unsafe staged path") {
		t.Fatalf("err=%v", err)
	}
	if got, readErr := os.ReadFile(sentinel); readErr != nil || string(got) != "external sentinel" {
		t.Fatalf("sentinel=%q err=%v", got, readErr)
	}
	var state string
	if queryErr := db.QueryRow(`SELECT state FROM media_asset_stage_journal WHERE stage_id=?`, stageID).Scan(&state); queryErr != nil || state != "staged" {
		t.Fatalf("journal state=%q err=%v", state, queryErr)
	}
}

func TestPosterCommitPerformsNoSourceFilesystemIOInsideImmediateTransaction(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	origTx, origStat, origOpen := withImmediatePosterTx, posterSourceStat, posterSourceOpen
	inside := false
	posterSourceStat = func(path string) (os.FileInfo, error) {
		if inside {
			t.Fatal("source stat inside immediate transaction")
		}
		return os.Stat(path)
	}
	posterSourceOpen = func(path string) (*os.File, error) {
		if inside {
			t.Fatal("source open inside immediate transaction")
		}
		return os.Open(path)
	}
	withImmediatePosterTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.WithImmediateConnTx(ctx, d, func(tx store.ImmediateConnTx) error {
			inside = true
			defer func() { inside = false }()
			return fn(tx)
		})
	}
	t.Cleanup(func() { withImmediatePosterTx, posterSourceStat, posterSourceOpen = origTx, origStat, origOpen })
	if err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); err != nil {
		t.Fatal(err)
	}
}

func TestPosterCommitRejectsReadablePlaintextPreferenceTransition(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	enc := filepath.Join(t.TempDir(), "video.mp4.enc")
	plain := filepath.Join(t.TempDir(), "plain.mp4")
	if err := os.WriteFile(enc, []byte("encrypted catalog"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET file_path=? WHERE id=?`, enc, task.MediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,?,?,?,?,'encrypted')`, task.MediaID, enc, "wrapped", "iv", plain); err != nil {
		t.Fatal(err)
	}
	req := posterRequest(t, db, task)
	req.SourcePath = enc
	req.SourceFingerprint, _ = publication.SourceFingerprintContext(context.Background(), enc)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(plain, []byte("now readable plaintext"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
	if err == nil || !strings.Contains(err.Error(), "poster commit: source selection changed") {
		t.Fatalf("err=%v", err)
	}
	assertNoPosterObjectOrQuarantine(t, upload)
}

func TestPosterCommitRejectsUnreadablePlaintextFallbackTransition(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	plain := taskSource(t, db, task.MediaID)
	enc := filepath.Join(t.TempDir(), "video.mp4.enc")
	if err := os.WriteFile(enc, []byte("encrypted catalog"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET file_path=? WHERE id=?`, enc, task.MediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,?,?,?,?,'encrypted')`, task.MediaID, enc, "wrapped", "iv", plain); err != nil {
		t.Fatal(err)
	}
	req := posterRequest(t, db, task)
	req.SourcePath = plain
	req.SourceFingerprint, _ = publication.SourceFingerprintContext(context.Background(), plain)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(plain); err != nil {
		t.Fatal(err)
	}

	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
	if err == nil || !strings.Contains(err.Error(), "poster commit: source selection changed") {
		t.Fatalf("err=%v", err)
	}
	assertNoPosterObjectOrQuarantine(t, upload)
}

func TestPosterCommitUsesPreferredPlaintextSelectionAndRejectsChanges(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "changed"}[change], func(t *testing.T) {
			db, upload, task := seedCurrentLinkedPosterTask(t)
			plain := taskSource(t, db, task.MediaID)
			enc := filepath.Join(t.TempDir(), "video.mp4.enc")
			if err := os.WriteFile(enc, []byte("encrypted"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media SET file_path=? WHERE id=?`, enc, task.MediaID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status) VALUES(?,?,?,?,?,'encrypted')`, task.MediaID, enc, "wrapped", "iv", plain); err != nil {
				t.Fatal(err)
			}
			runner := realPosterStageRunner(t, db, upload)
			req := posterRequest(t, db, task)
			req.SourcePath = storage.PreferredFFmpegPath(db, task.MediaID, 1, enc)
			req.SourceFingerprint, _ = publication.SourceFingerprintContext(context.Background(), req.SourcePath)
			staged, err := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
			if err != nil {
				t.Fatal(err)
			}
			if change {
				other := filepath.Join(t.TempDir(), "other.mp4")
				if err = os.WriteFile(other, []byte("other"), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`UPDATE media_encrypted_assets SET plain_path=? WHERE media_id=?`, other, task.MediaID); err != nil {
					t.Fatal(err)
				}
			}
			err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
			if change {
				if err == nil || !strings.Contains(err.Error(), "source selection changed") {
					t.Fatalf("err=%v", err)
				}
				assertNoPosterObjectOrQuarantine(t, upload)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPosterCommitRejectsSelectedSourcePathChangedAfterStage(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement.mp4")
	if err = os.WriteFile(replacement, []byte("replacement source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE media SET file_path=? WHERE id=?`, replacement, task.MediaID); err != nil {
		t.Fatal(err)
	}

	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
	if err == nil || !strings.Contains(err.Error(), "poster commit: source selection changed") {
		t.Fatalf("err=%v", err)
	}
	assertNoPosterObjectOrQuarantine(t, upload)
}

func TestPosterCommitRejectsSourceStatMutationBeforeImmediateTransaction(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	source := taskSource(t, db, task.MediaID)
	original := posterSourceStat
	calls := 0
	posterSourceStat = func(path string) (os.FileInfo, error) {
		calls++
		if calls == 2 {
			if err := os.WriteFile(source, []byte("changed source before transaction"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return os.Stat(path)
	}
	t.Cleanup(func() { posterSourceStat = original })

	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
	if err == nil || !strings.Contains(err.Error(), "poster commit: source stat changed") {
		t.Fatalf("err=%v", err)
	}
	assertNoPosterObjectOrQuarantine(t, upload)
}

func TestPosterCommitRejectsSelectedSourcePathMutationBeforeImmediateTransaction(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement.mp4")
	if err = os.WriteFile(replacement, []byte("replacement source"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := withImmediatePosterTx
	withImmediatePosterTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		if _, updateErr := d.ExecContext(ctx, `UPDATE media SET file_path=? WHERE id=?`, replacement, task.MediaID); updateErr != nil {
			t.Fatal(updateErr)
		}
		return store.WithImmediateConnTx(ctx, d, fn)
	}
	t.Cleanup(func() { withImmediatePosterTx = original })

	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
	if err == nil || !strings.Contains(err.Error(), "poster commit: source selection changed") {
		t.Fatalf("err=%v", err)
	}
	assertNoPosterObjectOrQuarantine(t, upload)
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
