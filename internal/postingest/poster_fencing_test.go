package postingest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/publication"
	"knox-media/internal/scraper"
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
	return db, upload, Task{ID: id, MediaID: mediaID, RunID: &runID, StepID: &stepID, Generation: 1, Type: TaskPoster, Status: StatusRunning, Attempts: 1, LeaseOwner: "poster-owner/old"}
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
	return db, upload, Task{ID: id, MediaID: mediaID, RunID: &run, StepID: &step, Generation: 1, Type: TaskPoster, Status: StatusRunning, Attempts: 1, LeaseOwner: "poster-owner/current"}
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
	return r.staged, nil
}

func TestPosterCurrentGenerationCommitsAtomicallyAndIsIdempotent(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	dir := t.TempDir()
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
	runner := &stagedPosterFake{staged: StagedPoster{Stage: publication.StageRecord{StageID: stageID}, Path: path, URL: "/immutable/poster.jpg", Source: "screen_grabber", Size: size, Hash: hash}}
	a := NewPosterAdapter(db, upload, nil, runner)
	result, err := a.ExecuteWithResult(context.Background(), task)
	if err != nil || result.Completion != AlreadyCommittedAtomically {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var taskStatus, stepStatus, journalState, meta string
	var evidence int
	if err = db.QueryRow(`SELECT p.status,s.status,j.state,m.meta_json,e.id FROM post_ingest_task p JOIN media_ingest_step s ON s.id=p.ingest_step_id JOIN media_ingest_evidence e ON e.step_id=s.id AND e.kind='poster' JOIN media_asset_stage_journal j ON j.stage_id=e.stage_id JOIN media m ON m.id=p.media_id WHERE p.id=?`, task.ID).Scan(&taskStatus, &stepStatus, &journalState, &meta, &evidence); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "done" || stepStatus != "done" || journalState != "committed" || evidence <= 0 || !strings.Contains(meta, "/immutable/poster.jpg") {
		t.Fatalf("rows=%s/%s/%s evidence=%d meta=%s", taskStatus, stepStatus, journalState, evidence, meta)
	}
	result, err = a.ExecuteWithResult(context.Background(), task)
	if err != nil || result.Completion != AlreadyCommittedAtomically || runner.calls != 1 {
		t.Fatalf("retry result=%+v err=%v calls=%d", result, err, runner.calls)
	}
}

func TestPosterCommitRejectsLeaseLossWithoutMutation(t *testing.T) {
	db, _, task := seedCurrentLinkedPosterTask(t)
	path := filepath.Join(t.TempDir(), "poster.jpg")
	_ = os.WriteFile(path, []byte("candidate"), 0644)
	size, hash, _ := hashPath(path)
	fp, _ := sourceFingerprint(taskSource(t, db, task.MediaID))
	stage := StagedPoster{Stage: publication.StageRecord{StageID: "lease-lost", Request: publication.StageRequest{MediaID: task.MediaID, RunID: *task.RunID, StepID: *task.StepID, Generation: task.Generation, OwnerToken: task.LeaseOwner, SourceFingerprint: fp}}, Path: path, URL: "/stale.jpg", Size: size, Hash: hash}
	if _, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES(?,?,?,?,?,?,?,'poster','staged',?,'{}')`, stage.Stage.StageID, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, fp, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='new-owner' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	requirePosterShutdown(t, commitStagedPoster(context.Background(), db, task, stage))
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
