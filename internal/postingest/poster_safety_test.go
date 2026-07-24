package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/keystore"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
	"testing"

	"knox-media/internal/publication"
	"knox-media/internal/store"
)

func TestPlainStagedPosterURLResolvesImmutableBytes(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); err != nil {
		t.Fatal(err)
	}
	if staged.URL == storage.PlainPosterURL(task.MediaID) || !strings.Contains(staged.URL, "generation-1/") {
		t.Fatalf("mutable URL=%s", staged.URL)
	}
	resolved := storage.ResolvePosterServePath(db, upload, task.MediaID)
	got, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "exact-poster" {
		t.Fatalf("resolved=%q", got)
	}
}
func TestPosterJournalUncertainActualCommitPreservesStage(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	original := withImmediatePosterJournalTx
	withImmediatePosterJournalTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		out, err := store.WithImmediateConnTx(ctx, d, fn)
		if err != nil {
			return out, err
		}
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("lost")}
	}
	t.Cleanup(func() { withImmediatePosterJournalTx = original })
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatalf("stage reconcile: %v", err)
	}
	if _, err = os.Stat(staged.Path); err != nil {
		t.Fatalf("committed stage removed: %v", err)
	}
}

func TestPosterRecoveryRejectsExternalStagedPathWithoutRemovingSentinel(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	external := t.TempDir()
	sentinel := filepath.Join(external, "sentinel")
	_ = os.WriteFile(sentinel, []byte("keep"), 0644)
	fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
	artifact := filepath.Join(external, "poster.jpg")
	_ = os.WriteFile(artifact, []byte("evil"), 0644)
	hashes, _ := json.Marshal(map[string]any{"path": artifact})
	if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('evil',?,?,?,?,?,?,'poster','quarantined',?,?)`, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, external, string(hashes)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
	if err == nil {
		t.Fatal("external staged path accepted")
	}
	if got, _ := os.ReadFile(sentinel); string(got) != "keep" {
		t.Fatalf("sentinel=%q", got)
	}
}
func TestPosterFinalCommitUncertainActualCommitReconciles(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	original := withImmediatePosterTx
	withImmediatePosterTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		out, err := store.WithImmediateConnTx(ctx, d, fn)
		if err != nil {
			return out, err
		}
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("lost")}
	}
	t.Cleanup(func() { withImmediatePosterTx = original })
	if err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); err != nil {
		t.Fatalf("reconcile=%v", err)
	}
	if !strings.Contains(staged.URL, "generation-1/") {
		t.Fatalf("mutable URL=%s", staged.URL)
	}
}

func realPosterStageRunner(t *testing.T, db *sql.DB, upload string) *LocalPosterRunner {
	t.Helper()
	return &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake", RunFFmpeg: func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _, _ float64, _, post []string, _ string) ([]byte, error) {
		return nil, os.WriteFile(post[len(post)-1], []byte("exact-poster"), 0644)
	}}
}
func posterRequest(t *testing.T, db *sql.DB, task Task) publication.StageRequest {
	t.Helper()
	fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
	return publication.StageRequest{QueueID: task.ID, MediaID: task.MediaID, RunID: *task.RunID, StepID: *task.StepID, Generation: task.Generation, OwnerToken: task.LeaseOwner, Attempt: task.Attempts, SourcePath: taskSource(t, db, task.MediaID), SourceFingerprint: fp}
}
func screenGrabberConfig() scraper.Config {
	return scraper.Config{ImageSources: []string{"screen_grabber"}}
}

func TestPosterJournalUncertainAbsentCleansCandidate(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	original := withImmediatePosterJournalTx
	withImmediatePosterJournalTx = func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("unknown")}
	}
	t.Cleanup(func() { withImmediatePosterJournalTx = original })
	_, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	var uncertain *store.ImmediateCommitError
	if !errors.As(err, &uncertain) {
		t.Fatalf("err=%v", err)
	}
	entries, _ := filepath.Glob(filepath.Join(upload, "posters", "generation-1", "*", "poster.jpg"))
	if len(entries) != 0 {
		t.Fatalf("absent journal retained=%v", entries)
	}
}

func TestPosterJournalUncertainQueryFailurePreservesCandidate(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	original := withImmediatePosterJournalTx
	reconcileOriginal := reconcilePosterJournal
	withImmediatePosterJournalTx = func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("unknown")}
	}
	reconcilePosterJournal = func(context.Context, *sql.DB, StagedPoster) (posterCommitState, error) {
		return posterCommitUnknown, errors.New("query failed")
	}
	t.Cleanup(func() { withImmediatePosterJournalTx = original; reconcilePosterJournal = reconcileOriginal })
	_, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	var uncertain *store.ImmediateCommitError
	if !errors.As(err, &uncertain) {
		t.Fatalf("err=%v", err)
	}
	entries, _ := filepath.Glob(filepath.Join(upload, "posters", "generation-1", "*", "poster.jpg"))
	if len(entries) != 1 {
		t.Fatalf("query failure candidate count=%d", len(entries))
	}
}

func TestPosterFinalCommitUncertainAbsentCleansCandidate(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	runner := realPosterStageRunner(t, db, upload)
	staged, err := runner.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if err != nil {
		t.Fatal(err)
	}
	original := withImmediatePosterTx
	withImmediatePosterTx = func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("unknown")}
	}
	t.Cleanup(func() { withImmediatePosterTx = original })
	err = commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload})
	var uncertain *store.ImmediateCommitError
	if !errors.As(err, &uncertain) {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(staged.Path); !os.IsNotExist(statErr) {
		t.Fatalf("absent candidate retained: %v", statErr)
	}
}

func TestPosterRecoveryRejectsTraversalAndRetainsActiveAndCommitted(t *testing.T) {
	t.Run("traversal", func(t *testing.T) {
		db, upload, task := seedCurrentLinkedPosterTask(t)
		outside := filepath.Join(t.TempDir(), "outside.jpg")
		_ = os.WriteFile(outside, []byte("keep"), 0644)
		fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
		hashes, _ := json.Marshal(map[string]any{"path": outside})
		badStage := filepath.Join(upload, "posters", "generation-1", "..", "..")
		_, _ = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('traversal',?,?,?,?,?,?,'poster','quarantined',?,?)`, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, badStage, string(hashes))
		_, _ = db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID)
		if _, _, err := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100); err == nil {
			t.Fatal("traversal accepted")
		}
		if got, _ := os.ReadFile(outside); string(got) != "keep" {
			t.Fatalf("outside=%q", got)
		}
	})
	t.Run("active", func(t *testing.T) {
		db, upload, task := seedCurrentLinkedPosterTask(t)
		stageDir := filepath.Join(upload, "posters", "generation-1", "active")
		_ = os.MkdirAll(stageDir, 0700)
		artifact := filepath.Join(stageDir, "poster.jpg")
		_ = os.WriteFile(artifact, []byte("active"), 0644)
		fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
		hashes, _ := json.Marshal(map[string]any{"path": artifact})
		_, _ = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('active',?,?,?,?,?,?,'poster','staged',?,?)`, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, stageDir, string(hashes))
		_, _, err := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(artifact); err != nil {
			t.Fatalf("active removed: %v", err)
		}
	})
}

func TestEncryptedPosterRecoveryPathClassRequiresDerivedRoot(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	stageDir := filepath.Join(upload, "posters", "generation-1", "encstage")
	_ = os.MkdirAll(stageDir, 0700)
	outside := filepath.Join(t.TempDir(), "poster.enc")
	_ = os.WriteFile(outside, []byte("enc"), 0600)
	fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
	hashes, _ := json.Marshal(map[string]any{"path": outside})
	_, _ = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('encstage',?,?,?,?,?,?,'poster','quarantined',?,?)`, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, stageDir, string(hashes))
	_, _ = db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID)
	if _, _, err := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload, Derived: filepath.Join(t.TempDir(), "derived")}, 100); err == nil {
		t.Fatal("external encrypted path accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("encrypted sentinel removed: %v", err)
	}
}

func TestRepairPosterCommitRetainsNewAndCleansOldImmutableStage(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	_, err := db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?; UPDATE media_ingest_run SET status='published' WHERE id=?; UPDATE post_ingest_task SET task_type='poster_repair',ingest_step_id=NULL WHERE id=?`, task.MediaID, *task.RunID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.Type, task.StepID = TaskPosterRepair, nil
	runner := realPosterStageRunner(t, db, upload)
	stage := func() StagedPoster {
		fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
		req := publication.StageRequest{MediaID: task.MediaID, RunID: *task.RunID, Generation: task.Generation, OwnerToken: task.LeaseOwner, Attempt: task.Attempts, SourcePath: taskSource(t, db, task.MediaID), SourceFingerprint: fp}
		req.StepID = 0
		req.QueueID = task.ID
		req.Attempt = task.Attempts
		got, e := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
		if e != nil {
			t.Fatal(e)
		}
		return got
	}
	old := stage()
	oldURL := old.URL
	if err = commitStagedPoster(context.Background(), db, task, old, PosterRecoveryRoots{Upload: upload}); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE post_ingest_task SET status='running',attempts=attempts+1,lease_owner=? WHERE id=?`, task.LeaseOwner, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.Attempts++
	newStage := stage()
	if err = commitStagedPoster(context.Background(), db, task, newStage, PosterRecoveryRoots{Upload: upload}); err != nil {
		t.Fatal(err)
	}
	newObject := storage.PosterObjectPath(upload, newStage.Hash, ".jpg")
	if _, e := os.Stat(newObject); e != nil {
		t.Fatalf("new object removed: %v", e)
	}
	if old.Hash != newStage.Hash {
		oldObject := storage.PosterObjectPath(upload, old.Hash, ".jpg")
		if _, e := os.Stat(oldObject); !os.IsNotExist(e) {
			t.Fatalf("old object retained: %v", e)
		}
	}
	if p := managedPosterPath("/uploads/posters/../../sentinel", newStage.Path); p != "" {
		t.Fatalf("traversal resolved %q", p)
	}
	_ = oldURL
}

func seedRepairPosterStage(t *testing.T) (*sql.DB, string, Task, *LocalPosterRunner, publication.StageRequest) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	_, err := db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?; UPDATE media_ingest_run SET status='published' WHERE id=?; UPDATE post_ingest_task SET task_type='poster_repair',ingest_step_id=NULL WHERE id=?`, task.MediaID, *task.RunID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.Type, task.StepID = TaskPosterRepair, nil
	fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
	req := publication.StageRequest{MediaID: task.MediaID, RunID: *task.RunID, Generation: task.Generation, QueueID: task.ID, Attempt: task.Attempts, OwnerToken: task.LeaseOwner, SourcePath: taskSource(t, db, task.MediaID), SourceFingerprint: fp}
	return db, upload, task, realPosterStageRunner(t, db, upload), req
}

func TestRepairPosterJournalUncertainByTaskClass(t *testing.T) {
	t.Run("committed", func(t *testing.T) {
		db, _, _, runner, req := seedRepairPosterStage(t)
		orig := withImmediatePosterJournalTx
		withImmediatePosterJournalTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
			out, e := store.WithImmediateConnTx(ctx, d, fn)
			if e != nil {
				return out, e
			}
			return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("lost")}
		}
		t.Cleanup(func() { withImmediatePosterJournalTx = orig })
		staged, e := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = os.Stat(staged.Path); e != nil {
			t.Fatal(e)
		}
		var n int
		if e = db.QueryRow(`SELECT COUNT(*) FROM poster_repair_stage WHERE stage_id=? AND queue_id=? AND attempt=?`, staged.Stage.StageID, req.QueueID, req.Attempt).Scan(&n); e != nil || n != 1 {
			t.Fatalf("journal=%d err=%v", n, e)
		}
	})
	t.Run("absent", func(t *testing.T) {
		_, upload, _, runner, req := seedRepairPosterStage(t)
		orig := withImmediatePosterJournalTx
		withImmediatePosterJournalTx = func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
			return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("lost")}
		}
		t.Cleanup(func() { withImmediatePosterJournalTx = orig })
		_, e := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
		var u *store.ImmediateCommitError
		if !errors.As(e, &u) {
			t.Fatalf("err=%v", e)
		}
		files, _ := filepath.Glob(filepath.Join(upload, "posters", "generation-1", "*", "poster.jpg"))
		if len(files) != 0 {
			t.Fatalf("retained=%v", files)
		}
	})
	t.Run("query failure", func(t *testing.T) {
		_, upload, _, runner, req := seedRepairPosterStage(t)
		orig, ro := withImmediatePosterJournalTx, reconcilePosterJournal
		withImmediatePosterJournalTx = func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
			return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("lost")}
		}
		reconcilePosterJournal = func(context.Context, *sql.DB, StagedPoster) (posterCommitState, error) {
			return posterCommitUnknown, errors.New("query")
		}
		t.Cleanup(func() { withImmediatePosterJournalTx = orig; reconcilePosterJournal = ro })
		_, e := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
		var u *store.ImmediateCommitError
		if !errors.As(e, &u) {
			t.Fatalf("err=%v", e)
		}
		files, _ := filepath.Glob(filepath.Join(upload, "posters", "generation-1", "*", "poster.jpg"))
		if len(files) != 1 {
			t.Fatalf("files=%v", files)
		}
	})
}

func TestRepairPosterRecoveryStates(t *testing.T) {
	makeStage := func(t *testing.T) (*sql.DB, string, Task, StagedPoster) {
		db, upload, task, runner, req := seedRepairPosterStage(t)
		staged, e := runner.StagePoster(context.Background(), req, 1, screenGrabberConfig())
		if e != nil {
			t.Fatal(e)
		}
		return db, upload, task, staged
	}
	t.Run("stale cleanup", func(t *testing.T) {
		db, upload, task, staged := makeStage(t)
		_, _ = db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID)
		checked, cleaned, e := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
		if e != nil || checked < 1 || cleaned != 1 {
			t.Fatalf("checked=%d cleaned=%d err=%v", checked, cleaned, e)
		}
		if _, e = os.Stat(staged.Path); !os.IsNotExist(e) {
			t.Fatalf("retained=%v", e)
		}
	})
	t.Run("active retained", func(t *testing.T) {
		db, upload, _, staged := makeStage(t)
		_, cleaned, e := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
		if e != nil || cleaned != 0 {
			t.Fatalf("cleaned=%d err=%v", cleaned, e)
		}
		if _, e = os.Stat(staged.Path); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("committed retained", func(t *testing.T) {
		db, upload, task, staged := makeStage(t)
		if e := commitStagedPoster(context.Background(), db, task, staged, PosterRecoveryRoots{Upload: upload}); e != nil {
			t.Fatal(e)
		}
		_, cleaned, e := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100)
		if e != nil || cleaned != 0 {
			t.Fatalf("cleaned=%d err=%v", cleaned, e)
		}
		if _, e = os.Stat(staged.Path); !os.IsNotExist(e) {
			t.Fatalf("generation temp retained: %v", e)
		}
		object := storage.PosterObjectPath(upload, staged.Hash, ".jpg")
		if _, e = os.Stat(object); e != nil {
			t.Fatalf("CAS object removed: %v", e)
		}
	})
	t.Run("no starvation", func(t *testing.T) {
		db, upload, task, _ := makeStage(t)
		_, _ = db.Exec(`UPDATE post_ingest_task SET status='cancelled',lease_owner=NULL WHERE id=?`, task.ID)
		for i := 0; i < 3; i++ {
			_, _, e := ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 1)
			if e != nil {
				t.Fatal(e)
			}
		}
		var got string
		if e := db.QueryRow(`SELECT recovery_error FROM poster_repair_stage LIMIT 1`).Scan(&got); e != nil || got != "cleaned_unreferenced" {
			t.Fatalf("got=%q err=%v", got, e)
		}
	})
}
