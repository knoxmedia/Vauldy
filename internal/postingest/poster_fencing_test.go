package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/publication"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
	"testing"
)

func seedLinkedPosterFenceTask(t *testing.T, meta string) (*sql.DB, string, Task) {
	t.Helper()
	db, upload, mediaID, _ := seedPosterTest(t, meta, "video")
	if _, err := db.Exec(`UPDATE media SET ingest_generation=2, publication_state='processing' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version,superseded_at,superseded_by_generation) VALUES(7101,?,1,'scan','cancelled','{}',2,CURRENT_TIMESTAMP,2),(7102,?,2,'scan','processing','{}',2,NULL,NULL)`, mediaID, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,lease_owner) VALUES(7201,7101,?,1,'poster',1,'running',1,'poster-owner/old'),(7202,7102,?,2,'poster',1,'running',1,'poster-owner/current')`, mediaID, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,lease_owner) VALUES(?,7101,7201,1,'poster','running',1,'poster-owner/old')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	runID, stepID := int64(7101), int64(7201)
	return db, upload, Task{ID: id, MediaID: mediaID, RunID: &runID, StepID: &stepID, Generation: 1, Type: TaskPoster, Status: StatusRunning, Attempts: 1, RetryRound: 0, LeaseOwner: "poster-owner/old"}
}

func assertPosterFenceUnchanged(t *testing.T, db *sql.DB, task Task, wantMeta string) {
	t.Helper()
	var meta, taskStatus, stepStatus string
	var generation int64
	if err := db.QueryRow(`SELECT meta_json,ingest_generation FROM media WHERE id=?`, task.MediaID).Scan(&meta, &generation); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, task.ID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, *task.StepID).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if meta != wantMeta || generation != 2 || taskStatus != "running" || stepStatus != "running" {
		t.Fatalf("mutated meta=%s gen=%d task=%s step=%s", meta, generation, taskStatus, stepStatus)
	}
}

func requirePosterShutdown(t *testing.T, err error) {
	t.Helper()
	var classified ClassifiedError
	if !errors.As(err, &classified) || classified.Kind != FailureShutdown {
		t.Fatalf("err=%v", err)
	}
}

func TestPosterAdapterStaleLinkedFastPathsCannotMutate(t *testing.T) {
	t.Run("existing metadata", func(t *testing.T) {
		const meta = `{"scrape":{"poster":"/current.jpg"}}`
		db, upload, task := seedLinkedPosterFenceTask(t, meta)
		r := &fakePosterRunner{}
		requirePosterShutdown(t, NewPosterAdapter(db, upload, nil, r).Execute(context.Background(), task))
		if r.calls != 0 {
			t.Fatalf("runner calls=%d", r.calls)
		}
		assertPosterFenceUnchanged(t, db, task, meta)
	})
	t.Run("existing plain poster", func(t *testing.T) {
		db, upload, task := seedLinkedPosterFenceTask(t, `{}`)
		path := filepath.Join(upload, "posters", "1.jpg")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("current-plain"), 0644); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		requirePosterShutdown(t, NewPosterAdapter(db, upload, nil, &fakePosterRunner{}).Execute(context.Background(), task))
		after, _ := os.ReadFile(path)
		if string(after) != string(before) {
			t.Fatalf("poster mutated=%q", after)
		}
		assertPosterFenceUnchanged(t, db, task, `{}`)
	})
	t.Run("existing derived poster", func(t *testing.T) {
		db, upload, task := seedLinkedPosterFenceTask(t, `{}`)
		path := filepath.Join(t.TempDir(), "poster.enc")
		if err := os.WriteFile(path, []byte("current-derived"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'poster','poster.jpg',?,'w','i')`, task.MediaID, path); err != nil {
			t.Fatal(err)
		}
		requirePosterShutdown(t, NewPosterAdapter(db, upload, nil, &fakePosterRunner{}).Execute(context.Background(), task))
		var selected string
		_ = db.QueryRow(`SELECT enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind='poster' AND logical_name='poster.jpg'`, task.MediaID).Scan(&selected)
		if selected != path {
			t.Fatalf("pointer mutated=%s", selected)
		}
		assertPosterFenceUnchanged(t, db, task, `{}`)
	})
}

func seedCurrentLinkedPosterTask(t *testing.T) (*sql.DB, string, Task) {
	t.Helper()
	db, upload, mediaID, _ := seedPosterTest(t, `{}`, "video")
	source := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(source, []byte("video-source"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET file_path=?,ingest_generation=1,publication_state='processing' WHERE id=?`, source, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(7301,?,1,'scan','processing','{}',2);INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status,attempts,lease_owner) VALUES(7302,7301,?,1,'poster',1,'running',1,'poster-owner/current')`, mediaID, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,lease_owner) VALUES(?,7301,7302,1,'poster','running',1,'poster-owner/current')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	run, step := int64(7301), int64(7302)
	return db, upload, Task{ID: id, MediaID: mediaID, RunID: &run, StepID: &step, Generation: 1, Type: TaskPoster, Status: StatusRunning, Attempts: 1, RetryRound: 0, LeaseOwner: "poster-owner/current"}
}

type stagedPosterFake struct {
	staged StagedPoster
	calls  int
}

func (r *stagedPosterFake) Capture(context.Context, int64, int64, scraper.Config) (string, string, error) {
	return "", "", errors.New("legacy capture called")
}
func (r *stagedPosterFake) StagePoster(_ context.Context, req publication.StageRequest, _ int64, _ scraper.Config) (StagedPoster, error) {
	r.calls++
	r.staged.Stage.Request = req
	r.staged.Stage.Kind = publication.ArtifactPoster
	r.staged.Stage.State = "staged"
	return r.staged, nil
}

func TestPosterCurrentGenerationCommitsAtomicallyAndIsIdempotent(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	dir := filepath.Join(upload, "posters", "generation-1", "poster-stage-current")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(path, []byte("new-poster"), 0644); err != nil {
		t.Fatal(err)
	}
	size, hash, _ := hashPath(path)
	stageID := "poster-stage-current"
	fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
	if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,original_path,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged','',?,'{}')`, stageID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, dir); err != nil {
		t.Fatal(err)
	}
	runner := &stagedPosterFake{staged: StagedPoster{Stage: publication.StageRecord{StageID: stageID, StagedPath: dir}, Path: path, URL: "/immutable/poster.jpg", Source: "screen_grabber", Size: size, Hash: hash}}
	a := NewPosterAdapter(db, upload, nil, runner)
	originalFingerprint := posterSourceFingerprint
	fingerprintCalls := 0
	posterSourceFingerprint = func(ctx context.Context, path string) (string, error) {
		fingerprintCalls++
		return publication.SourceFingerprintContext(ctx, path)
	}
	t.Cleanup(func() { posterSourceFingerprint = originalFingerprint })
	result, err := a.ExecuteWithResult(context.Background(), task)
	if err != nil || result.Completion != AlreadyCommittedAtomically {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if fingerprintCalls != 1 {
		t.Fatalf("source fingerprint calls=%d want 1", fingerprintCalls)
	}
	var taskStatus, stepStatus, journalState, meta string
	var evidence int
	if err = db.QueryRow(`SELECT p.status,s.status,j.state,m.meta_json,e.id FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_evidence e ON e.step_id=s.id AND e.kind='poster' JOIN media_asset_stage_journal j ON j.stage_id=e.stage_id JOIN media m ON m.id=p.media_id WHERE p.id=?`, task.ID).Scan(&taskStatus, &stepStatus, &journalState, &meta, &evidence); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "done" || stepStatus != "done" || journalState != "committed" || evidence <= 0 || !strings.Contains(meta, "/posters/objects/sha256/") {
		t.Fatalf("rows=%s/%s/%s evidence=%d meta=%s", taskStatus, stepStatus, journalState, evidence, meta)
	}
	result, err = a.ExecuteWithResult(context.Background(), task)
	if err != nil || result.Completion != AlreadyCommittedAtomically || runner.calls != 1 {
		t.Fatalf("retry result=%+v err=%v calls=%d", result, err, runner.calls)
	}
}

func TestCurrentPosterEvidenceRequiresDoneQueueAndStep(t *testing.T) {
	for _, target := range []string{"queue", "step"} {
		t.Run(target, func(t *testing.T) {
			db, upload, task := seedCurrentLinkedPosterTask(t)
			source := taskSource(t, db, task.MediaID)
			poster := filepath.Join(upload, "exact-evidence.jpg")
			if err := os.WriteFile(poster, []byte("exact poster"), 0o600); err != nil {
				t.Fatal(err)
			}
			size, hash, err := hashPath(poster)
			if err != nil {
				t.Fatal(err)
			}
			fp, err := publication.SourceFingerprintContext(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			url := "/uploads/exact-evidence.jpg"
			refs, _ := json.Marshal(map[string]any{"path": poster, "url": url, "size": size, "sha256": hash})
			if _, err = db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, `{"scrape":{"poster":"`+url+`"}}`, task.MediaID); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('exact-evidence',?,?,?,?,?,?,'poster','committed',?,?)`, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, filepath.Dir(poster), string(refs)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,'poster',?,?,'generated',CURRENT_TIMESTAMP,'exact-evidence')`, *task.RunID, *task.StepID, task.MediaID, task.Generation, fp, string(refs)); err != nil {
				t.Fatal(err)
			}
			if target == "queue" {
				if _, err = db.Exec(`UPDATE media_ingest_step SET status='done' WHERE id=?`, *task.StepID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = db.Exec(`UPDATE post_ingest_task SET status='done' WHERE id=?`, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			exact, _, err := currentPosterEvidence(context.Background(), db, task, source)
			if err != nil {
				t.Fatal(err)
			}
			if exact {
				t.Fatalf("%s running evidence accepted", target)
			}
		})
	}
}

func TestPosterExecutionSelectsSourceOnceForEvidenceAndStage(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	first := taskSource(t, db, task.MediaID)
	second := filepath.Join(t.TempDir(), "second.mp4")
	if err := os.WriteFile(second, []byte("second source"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstFP, err := publication.SourceFingerprintContext(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,'poster',?,'{}','generated',CURRENT_TIMESTAMP,'prior-selection')`, *task.RunID, *task.StepID, task.MediaID, task.Generation, "stale"); err != nil {
		t.Fatal(err)
	}

	stageID := "single-selection"
	dir := filepath.Join(upload, "posters", "generation-1", stageID)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, posterLogicalName)
	if err = os.WriteFile(artifact, []byte("poster"), 0o600); err != nil {
		t.Fatal(err)
	}
	size, hash, err := hashPath(artifact)
	if err != nil {
		t.Fatal(err)
	}
	runner := &stagedPosterFake{staged: StagedPoster{Stage: publication.StageRecord{StageID: stageID, StagedPath: dir}, Path: artifact, URL: storage.ImmutablePlainPosterURL(task.Generation, stageID), Source: "screen_grabber", Size: size, Hash: hash}}
	a := NewPosterAdapter(db, upload, nil, runner)
	original := posterSourceFingerprint
	calls := 0
	posterSourceFingerprint = func(ctx context.Context, path string) (string, error) {
		calls++
		if calls == 1 {
			if _, updateErr := db.ExecContext(ctx, `UPDATE media SET file_path=? WHERE id=?`, second, task.MediaID); updateErr != nil {
				return "", updateErr
			}
		}
		return publication.SourceFingerprintContext(ctx, path)
	}
	t.Cleanup(func() { posterSourceFingerprint = original })

	_, err = a.ExecuteWithResult(context.Background(), task)
	if err == nil {
		t.Fatal("selection change was not fenced at commit")
	}
	if calls != 1 {
		t.Fatalf("source fingerprint calls=%d want 1", calls)
	}
	if runner.calls != 1 {
		t.Fatalf("stage calls=%d want 1", runner.calls)
	}
	if runner.staged.Stage.Request.SourcePath != first || runner.staged.Stage.Request.SourceFingerprint != firstFP {
		t.Fatalf("request path=%q fp=%q want path=%q fp=%q", runner.staged.Stage.Request.SourcePath, runner.staged.Stage.Request.SourceFingerprint, first, firstFP)
	}
}

func TestPosterStalePriorEvidenceReusesSingleSourceFingerprint(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	oldStage := "stale-prior-poster"
	oldDir := filepath.Join(upload, "posters", "generation-1", oldStage)
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldDir, posterLogicalName)
	if err := os.WriteFile(oldPath, []byte("stale artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFP := strings.Replace(posterRequest(t, db, task).SourceFingerprint, "sha256:", "sha256:"+strings.Repeat("0", 64), 1)
	if _, err := db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?,'poster',?,'{}','generated',CURRENT_TIMESTAMP,?)`, *task.RunID, *task.StepID, task.MediaID, task.Generation, oldFP, oldStage); err != nil {
		t.Fatal(err)
	}

	runner := realPosterStageRunner(t, db, upload)
	a := NewPosterAdapter(db, upload, nil, runner)
	original := posterSourceFingerprint
	calls := 0
	posterSourceFingerprint = func(ctx context.Context, path string) (string, error) {
		calls++
		return publication.SourceFingerprintContext(ctx, path)
	}
	t.Cleanup(func() { posterSourceFingerprint = original })

	_, err := a.ExecuteWithResult(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "poster commit conflict") {
		t.Fatalf("err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("source fingerprint calls=%d want 1", calls)
	}
}

func TestPosterCommitRejectsLeaseLossWithoutMutation(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	dir := filepath.Join(upload, "posters", "generation-1", "lease-lost")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "poster.jpg")
	_ = os.WriteFile(path, []byte("candidate"), 0644)
	size, hash, _ := hashPath(path)
	fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
	stage := StagedPoster{Stage: publication.StageRecord{StageID: "lease-lost", Request: publication.StageRequest{QueueID: task.ID, MediaID: task.MediaID, RunID: *task.RunID, StepID: *task.StepID, Generation: task.Generation, OwnerToken: task.LeaseOwner, Attempt: task.Attempts, SourceFingerprint: fp}, Kind: publication.ArtifactPoster, State: "staged", StagedPath: filepath.Dir(path)}, Path: path, URL: "/stale.jpg", Size: size, Hash: hash}
	if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged',?,'{}')`, stage.Stage.StageID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='new-owner' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	requirePosterShutdown(t, commitStagedPoster(context.Background(), db, task, stage, PosterRecoveryRoots{Upload: upload}))
	var meta, taskStatus, stepStatus string
	var generation int64
	_ = db.QueryRow(`SELECT meta_json,ingest_generation FROM media WHERE id=?`, task.MediaID).Scan(&meta, &generation)
	_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, task.ID).Scan(&taskStatus)
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, *task.StepID).Scan(&stepStatus)
	if meta != `{}` || generation != 1 || taskStatus != "running" || stepStatus != "running" {
		t.Fatalf("mutated meta=%s gen=%d task=%s step=%s", meta, generation, taskStatus, stepStatus)
	}
}

func TestPosterRepairExactIdentityFastPath(t *testing.T) {
	db, upload, mediaID, _ := seedPosterTest(t, `{"scrape":{"poster":"/existing.jpg"}}`, "video")
	if _, err := db.Exec(`UPDATE media SET ingest_generation=3,publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?;INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(7401,?,3,'repair','published','{}',2)`, mediaID, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,lease_owner) VALUES(?,7401,NULL,3,'poster_repair','running',2,'repair-owner/current')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	run := int64(7401)
	task := Task{ID: id, MediaID: mediaID, RunID: &run, Generation: 3, Type: TaskPosterRepair, Attempts: 2, LeaseOwner: "repair-owner/current"}
	r := &fakePosterRunner{}
	if err = NewPosterAdapter(db, upload, nil, r).Execute(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if r.calls != 0 {
		t.Fatalf("runner calls=%d", r.calls)
	}
}

func TestPosterLegacyClaimedFastPath(t *testing.T) {
	db, upload, mediaID, _ := seedPosterTest(t, `{"scrape":{"poster":"/legacy.jpg"}}`, "video")
	res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,generation,task_type,status,attempts,lease_owner) VALUES(?,0,'poster','running',1,'legacy-owner/current')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	task := Task{ID: id, MediaID: mediaID, Generation: 0, Type: TaskPoster, Attempts: 1, LeaseOwner: "legacy-owner/current"}
	if err = NewPosterAdapter(db, upload, nil, &fakePosterRunner{}).Execute(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE post_ingest_task SET lease_owner='legacy-owner/new' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	requirePosterShutdown(t, NewPosterAdapter(db, upload, nil, &fakePosterRunner{}).Execute(context.Background(), task))
}
func TestPosterAtomicFinalizerFencesStaleRetryRound(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	dir := filepath.Join(upload, "posters", "generation-1", "poster-retry-round")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, posterLogicalName)
	if err := os.WriteFile(path, []byte("candidate"), 0o644); err != nil {
		t.Fatal(err)
	}
	size, hash, err := hashPath(path)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := sourceFingerprint(taskSource(t, db, task.MediaID))
	if err != nil {
		t.Fatal(err)
	}
	stage := StagedPoster{Stage: publication.StageRecord{StageID: "poster-retry-round", Request: publication.StageRequest{QueueID: task.ID, MediaID: task.MediaID, RunID: *task.RunID, StepID: *task.StepID, Generation: task.Generation, OwnerToken: task.LeaseOwner, Attempt: task.Attempts, SourceFingerprint: fp}, Kind: publication.ArtifactPoster, State: "staged", StagedPath: dir}, Path: path, URL: "/stale.jpg", Size: size, Hash: hash}
	if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged',?,'{}')`, stage.Stage.StageID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET retry_round=retry_round+1 WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	requirePosterShutdown(t, commitStagedPoster(context.Background(), db, task, stage, PosterRecoveryRoots{Upload: upload}))
	var meta, taskStatus, stepStatus, journalState string
	var evidence, derived int
	if err := db.QueryRow(`SELECT m.meta_json,p.status,s.status,j.state,(SELECT COUNT(*) FROM media_ingest_evidence WHERE step_id=s.id),(SELECT COUNT(*) FROM media_derived_assets WHERE media_id=m.id) FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media m ON m.id=p.media_id JOIN media_asset_stage_journal j ON j.stage_id=? WHERE p.id=?`, stage.Stage.StageID, task.ID).Scan(&meta, &taskStatus, &stepStatus, &journalState, &evidence, &derived); err != nil {
		t.Fatal(err)
	}
	if meta != `{}` || taskStatus != "running" || stepStatus != "running" || journalState != "staged" || evidence != 0 || derived != 0 {
		t.Fatalf("stale round mutated meta=%s task=%s step=%s journal=%s evidence=%d derived=%d", meta, taskStatus, stepStatus, journalState, evidence, derived)
	}
	task.RetryRound++
	if err := commitStagedPoster(context.Background(), db, task, stage, PosterRecoveryRoots{Upload: upload}); err != nil {
		t.Fatalf("current round finalizer: %v", err)
	}
}

func TestCachedPosterSourceFingerprintReusesEvidenceOnIdentityMatch(t *testing.T) {
	db, _, mediaID, _ := seedPosterTest(t, "{}", "video")
	source := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(source, []byte("video-source"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET file_path=?,ingest_generation=1,publication_state='processing' WHERE id=?`, source, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(7301,?,1,'scan','processing','{}',2);INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(7302,7301,?,1,'poster',1,'done')`, mediaID, mediaID); err != nil {
		t.Fatal(err)
	}
	fp, err := publication.SourceFingerprintContext(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(7301,7302,?,1,'poster',?,'{}','generated',CURRENT_TIMESTAMP,'cache-1')`, mediaID, fp); err != nil {
		t.Fatal(err)
	}
	original := posterSourceFingerprint
	calls := 0
	posterSourceFingerprint = func(ctx context.Context, path string) (string, error) {
		calls++
		return "", errors.New("full hash unexpectedly called")
	}
	t.Cleanup(func() { posterSourceFingerprint = original })
	got, err := cachedPosterSourceFingerprint(context.Background(), db, mediaID, source)
	if err != nil {
		t.Fatal(err)
	}
	if got != fp {
		t.Fatalf("got=%q want=%q", got, fp)
	}
	if calls != 0 {
		t.Fatalf("full fingerprint calls=%d want 0", calls)
	}
}

func TestCachedPosterSourceFingerprintFallsBackOnIdentityMismatch(t *testing.T) {
	db, _, mediaID, _ := seedPosterTest(t, "{}", "video")
	source := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(source, []byte("video-source"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET file_path=?,ingest_generation=1,publication_state='processing' WHERE id=?`, source, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(7301,?,1,'scan','processing','{}',2);INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES(7302,7301,?,1,'poster',1,'done')`, mediaID, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(7301,7302,?,1,'poster','stale|1|1|sha256:deadbeef','{}','generated',CURRENT_TIMESTAMP,'cache-2')`, mediaID); err != nil {
		t.Fatal(err)
	}
	original := posterSourceFingerprint
	calls := 0
	posterSourceFingerprint = func(ctx context.Context, path string) (string, error) {
		calls++
		return publication.SourceFingerprintContext(ctx, path)
	}
	t.Cleanup(func() { posterSourceFingerprint = original })
	got, err := cachedPosterSourceFingerprint(context.Background(), db, mediaID, source)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("full fingerprint calls=%d want 1", calls)
	}
	if !strings.Contains(got, "|sha256:") {
		t.Fatalf("unexpected fingerprint=%q", got)
	}
}
