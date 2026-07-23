package pretranscode

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"knox-media/internal/publication"
)

func TestClaimNextJobSelectsWaitingRendition(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, err := presetSvc.CreatePreset(CreatePresetInput{
		Name: "claim-test", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mid := seedVideo(t, db, t.TempDir(), "fid-claim", "Claim")
	taskSvc := &TaskService{DB: db, TranscodeDir: t.TempDir()}
	ids, err := taskSvc.CreateTask([]int64{mid}, preset.ID, "high")
	if err != nil {
		t.Fatal(err)
	}

	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	job, gotPreset, rendition, mediaID, catalogPath, _, _, err := w.claimNextJob()
	if err != nil {
		t.Fatalf("claimNextJob: %v", err)
	}
	if job == nil || gotPreset == nil || rendition == nil {
		t.Fatal("expected claimed job, preset, and rendition")
	}
	if job.TaskID != ids[0] {
		t.Fatalf("task id=%d want %d", job.TaskID, ids[0])
	}
	if mediaID != mid {
		t.Fatalf("media id=%d want %d", mediaID, mid)
	}
	if catalogPath == "" {
		t.Fatal("catalog path should be set")
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM pretranscode_rendition_job WHERE id = ?`, job.ID).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if status != "running" {
		t.Fatalf("claimed job should be running, got %s", status)
	}
}

func TestClaimNextJobReturnsNoRowsWhenNoneWaiting(t *testing.T) {
	db := newTestDB(t)
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	_, _, _, _, _, _, _, err := w.claimNextJob()
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestClaimNextJobAllowsRunningTask(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, err := presetSvc.CreatePreset(CreatePresetInput{
		Name: "multi-claim", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{
			{Name: "480p", Height: 480, VideoBitrate: "1400k"},
			{Name: "720p", Height: 720, VideoBitrate: "2800k"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mid := seedVideo(t, db, t.TempDir(), "fid-multi", "Multi")
	taskSvc := &TaskService{DB: db, TranscodeDir: t.TempDir()}
	ids, err := taskSvc.CreateTask([]int64{mid}, preset.ID, "normal")
	if err != nil {
		t.Fatal(err)
	}
	taskID := ids[0]

	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 2, 1)
	first, _, _, _, _, _, _, err := w.claimNextJob()
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}

	_, _ = db.Exec(`UPDATE transcode_task SET status='running' WHERE id=?`, taskID)

	second, _, _, _, _, _, _, err := w.claimNextJob()
	if err != nil {
		t.Fatalf("second claim while task running: %v", err)
	}
	if second == nil {
		t.Fatal("expected second job to be claimable while task is running")
	}
	if second.ID == first.ID {
		t.Fatalf("expected different job, got same id %d", second.ID)
	}
	if second.TaskID != taskID {
		t.Fatalf("second job task_id=%d want %d", second.TaskID, taskID)
	}
}

func TestPrepareCompletionPublishesFinalRequiredStep(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	mediaID := seedVideo(t, db, root, "fid-prepare-publish", "Prepare publish")
	var libraryID int64
	if err := db.QueryRow(`SELECT library_id FROM media WHERE id=?`, mediaID).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,1,'scan','processing',0,'{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	if _, err = db.Exec(`UPDATE media SET ingest_generation=1,publication_state='processing' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'poster',1,'done')`, runID, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,lease_owner,lease_until) VALUES(?,?,1,'prepare',1,'running',1,'test-parent',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO transcode_task(file_id,quality,status,progress,task_type,media_id,ingest_run_id,ingest_step_id,generation,lease_owner,lease_until) VALUES('fid-prepare-publish','360p','running',99,'pretranscode',?,?,?,1,'test-parent',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress) VALUES(?,1,'360p','done',100)`, taskID); err != nil {
		t.Fatal(err)
	}

	w := NewWorker(db, nil, "ffmpeg", root, 1, 1)
	if err := w.finalizeTask(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	var taskStatus, stepStatus, runStatus, mediaStatus string
	if err := db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, taskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&mediaStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "done" || stepStatus != "done" || runStatus != "published" || mediaStatus != "published" {
		t.Fatalf("terminal states task=%s step=%s run=%s media=%s library=%d", taskStatus, stepStatus, runStatus, mediaStatus, libraryID)
	}
}

func seedLinkedPrepareTerminal(t *testing.T, db *sql.DB, generation int64) (taskID, runID, stepID, mediaID int64) {
	t.Helper()
	root := t.TempDir()
	mediaID = seedVideo(t, db, root, "fid-linked-terminal", "linked")
	if _, err := db.Exec(`UPDATE media SET ingest_generation=?,publication_state='processing' WHERE id=?`, generation, mediaID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,?,'scan','processing',0,'{}')`, mediaID, generation)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,lease_owner,lease_until) VALUES(?,?,?,'prepare',1,'running',1,'test-parent',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, runID, mediaID, generation)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO transcode_task(file_id,quality,status,progress,task_type,media_id,ingest_run_id,ingest_step_id,generation,lease_owner,lease_until) VALUES('fid-linked-terminal','360p','running',50,'pretranscode',?,?,?,?,'test-parent',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, mediaID, runID, stepID, generation)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ = res.LastInsertId()
	return
}

func TestPrepareFailureFailsInitialLinkedPublication(t *testing.T) {
	db := newTestDB(t)
	taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
	if _, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,error_message) VALUES(?,1,'360p','failed','boom')`, taskID); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	if err := w.finalizeTask(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	var task, step, run, media string
	var attempts, maxAttempts int
	_ = db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, taskID).Scan(&task)
	_ = db.QueryRow(`SELECT status,attempts,max_attempts FROM media_ingest_step WHERE id=?`, stepID).Scan(&step, &attempts, &maxAttempts)
	_ = db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&run)
	_ = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&media)
	if task != "failed" || step != "failed" || attempts != maxAttempts || run != "failed" || media != "failed" {
		t.Fatalf("states=%s/%s(%d/%d)/%s/%s", task, step, attempts, maxAttempts, run, media)
	}
}

func TestPrepareStaleGenerationCompletesOwnRunOnly(t *testing.T) {
	db := newTestDB(t)
	taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
	if _, err := db.Exec(`UPDATE media SET ingest_generation=2,publication_state='processing' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress) VALUES(?,1,'360p','done',100)`, taskID); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	if err := w.finalizeTask(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	var step, run, media string
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&step)
	_ = db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&run)
	_ = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&media)
	if step != "done" || run != "published" || media != "processing" {
		t.Fatalf("states=%s/%s/%s", step, run, media)
	}
}

type terminalRollbackCase struct {
	name       string
	terminal   renditionJobTerminal
	triggerSQL string
}

func TestFinalizeJobAndTaskTxRollsBackEntireTerminalTransition(t *testing.T) {
	cases := []terminalRollbackCase{
		{name: "normal success step failure", terminal: renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "/out/master.m3u8", Encoder: "libx264"}, triggerSQL: `CREATE TRIGGER fail_prepare_step BEFORE UPDATE ON media_ingest_step WHEN NEW.status='done' BEGIN SELECT RAISE(ABORT,'step sync failed'); END`},
		{name: "skip success aggregate failure", terminal: renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "", Encoder: "skip"}, triggerSQL: `CREATE TRIGGER fail_prepare_aggregate BEFORE UPDATE ON media_ingest_run WHEN NEW.status='published' BEGIN SELECT RAISE(ABORT,'aggregate failed'); END`},
		{name: "failure step failure", terminal: renditionJobTerminal{Status: "failed", Progress: 0, ErrorMessage: "ffmpeg failed", Encoder: "libx264"}, triggerSQL: `CREATE TRIGGER fail_prepare_failure BEFORE UPDATE ON media_ingest_step WHEN NEW.status='failed' BEGIN SELECT RAISE(ABORT,'failure sync failed'); END`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
			res, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,started_at,lease_owner,lease_until) VALUES(?,1,'360p','running',42,CURRENT_TIMESTAMP,'test-owner',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, taskID)
			if err != nil {
				t.Fatal(err)
			}
			jobID, _ := res.LastInsertId()
			if _, err = db.Exec(tc.triggerSQL); err != nil {
				t.Fatal(err)
			}
			w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
			if _, err = w.finalizeJobAndTaskTx(context.Background(), claimedJob{ID: jobID, TaskID: taskID, Owner: "test-owner"}, tc.terminal); err == nil {
				t.Fatal("expected injected terminal transaction failure")
			}
			assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "running", "running", "running", "processing", "processing")
			if _, err = db.Exec(`DROP TRIGGER ` + triggerName(tc.triggerSQL)); err != nil {
				t.Fatal(err)
			}
			if _, err = w.finalizeJobAndTaskTx(context.Background(), claimedJob{ID: jobID, TaskID: taskID, Owner: "test-owner"}, tc.terminal); err != nil {
				t.Fatalf("retry terminal transition: %v", err)
			}
			want := "done"
			wantRun := "published"
			wantMedia := "published"
			if tc.terminal.Status == "failed" {
				want = "failed"
				wantRun = "failed"
				wantMedia = "failed"
			}
			assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, want, want, want, wantRun, wantMedia)
			var output, errorMessage, encoder string
			if err = db.QueryRow(`SELECT COALESCE(output_path,''),COALESCE(error_message,''),COALESCE(encoder_used,'') FROM pretranscode_rendition_job WHERE id=?`, jobID).Scan(&output, &errorMessage, &encoder); err != nil {
				t.Fatal(err)
			}
			if output != tc.terminal.OutputPath || errorMessage != tc.terminal.ErrorMessage || encoder != tc.terminal.Encoder {
				t.Fatalf("terminal payload=%q/%q/%q", output, errorMessage, encoder)
			}
		})
	}
}

func triggerName(create string) string {
	fields := strings.Fields(create)
	if len(fields) < 3 {
		return ""
	}
	return fields[2]
}

func assertPrepareTerminalState(t *testing.T, db *sql.DB, jobID, taskID, stepID, runID, mediaID int64, wantJob, wantTask, wantStep, wantRun, wantMedia string) {
	t.Helper()
	var job, task, step, run, media string
	if err := db.QueryRow(`SELECT status FROM pretranscode_rendition_job WHERE id=?`, jobID).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, taskID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&media); err != nil {
		t.Fatal(err)
	}
	if job != wantJob || task != wantTask || step != wantStep || run != wantRun || media != wantMedia {
		t.Fatalf("states job/task/step/run/media=%s/%s/%s/%s/%s want %s/%s/%s/%s/%s", job, task, step, run, media, wantJob, wantTask, wantStep, wantRun, wantMedia)
	}
}

func TestFinalizeJobAndTaskTxCommitsOnlyJobWhileAnotherRenditionActive(t *testing.T) {
	db := newTestDB(t)
	taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
	res, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,lease_owner,lease_until) VALUES(?,1,'360p','running',40,'test-owner',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	firstID, _ := res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress) VALUES(?,2,'720p','running',20)`, taskID); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 2, 1)
	terminal, err := w.finalizeJobAndTaskTx(context.Background(), claimedJob{ID: firstID, TaskID: taskID, Owner: "test-owner"}, renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "/360/master.m3u8", Encoder: "libx264"})
	if err != nil {
		t.Fatal(err)
	}
	if terminal {
		t.Fatal("task reported terminal while another rendition is running")
	}
	assertPrepareTerminalState(t, db, firstID, taskID, stepID, runID, mediaID, "done", "running", "running", "processing", "processing")
}

func TestFinalizeJobAndTaskTxStatusGuardIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
	res, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,lease_owner,lease_until) VALUES(?,1,'360p','running',40,'test-owner',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := res.LastInsertId()
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 2, 1)
	payload := renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "/first", Encoder: "libx264"}
	if terminal, err := w.finalizeJobAndTaskTx(context.Background(), claimedJob{ID: jobID, TaskID: taskID, Owner: "test-owner"}, payload); err != nil || !terminal {
		t.Fatalf("first finalize=%v/%v", terminal, err)
	}
	if terminal, err := w.finalizeJobAndTaskTx(context.Background(), claimedJob{ID: jobID, TaskID: taskID, Owner: "test-owner"}, renditionJobTerminal{Status: "failed", ErrorMessage: "late"}); !errors.Is(err, ErrJobOwnershipLost) || terminal {
		t.Fatalf("duplicate finalize=%v/%v", terminal, err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "done", "done", "done", "published", "published")
	var output string
	if err = db.QueryRow(`SELECT output_path FROM pretranscode_rendition_job WHERE id=?`, jobID).Scan(&output); err != nil {
		t.Fatal(err)
	}
	if output != "/first" {
		t.Fatalf("duplicate overwrote output %q", output)
	}
}

func TestFinalizeJobAndTaskTxConcurrentRenditionsConverge(t *testing.T) {
	db := newTestDB(t)
	taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
	ids := make([]int64, 0, 2)
	owners := []string{"owner-360", "owner-720"}
	for i, name := range []string{"360p", "720p"} {
		res, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,lease_owner,lease_until) VALUES(?,?,?,'running',50,?,datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, taskID, i+1, name, owners[i])
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 2, 1)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, id := range ids {
		wg.Add(1)
		go func(jobID int64, owner string) {
			defer wg.Done()
			_, err := w.finalizeJobAndTaskTx(context.Background(), claimedJob{ID: jobID, TaskID: taskID, Owner: owner}, renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "/done", Encoder: "libx264"})
			errs <- err
		}(id, owners[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent finalize: %v", err)
		}
	}
	assertPrepareTerminalState(t, db, ids[0], taskID, stepID, runID, mediaID, "done", "done", "done", "published", "published")
	var second string
	if err := db.QueryRow(`SELECT status FROM pretranscode_rendition_job WHERE id=?`, ids[1]).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second != "done" {
		t.Fatalf("second job=%s", second)
	}
}

func TestFinalizeJobAndTaskTxRejectsStaleOwnerAfterRecoveryReclaim(t *testing.T) {
	db := newTestDB(t)
	taskID, runID, stepID, mediaID := seedLinkedPrepareTerminal(t, db, 1)
	res, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,lease_owner,lease_until) VALUES(?,1,'360p','running',25,'new-owner',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := res.LastInsertId()
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	if terminal, err := w.finalizeJobAndTaskTx(context.Background(), claimedJob{ID: jobID, TaskID: taskID, Owner: "old-owner"}, renditionJobTerminal{Status: "done", Progress: 100}); !errors.Is(err, ErrJobOwnershipLost) || terminal {
		t.Fatalf("stale finalize=%v/%v", terminal, err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "running", "running", "running", "processing", "processing")
}

func TestClaimHydrationFailureRestoresOwnedJobWaiting(t *testing.T) {
	db := newTestDB(t)
	var presetID, renditionID int64
	if err := db.QueryRow(`SELECT p.id,r.id FROM transcode_preset p JOIN preset_rendition r ON r.preset_id=p.id ORDER BY p.id,r.id LIMIT 1`).Scan(&presetID, &renditionID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO transcode_task(file_id,status,task_type,preset_id) VALUES('missing-media','waiting','pretranscode',?)`, presetID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO pretranscode_task_meta(task_id,preset_id,output_format,encryption_mode,priority) VALUES(?,?,'hls','none','normal')`, taskID, presetID)
	if err != nil {
		t.Fatal(err)
	}
	res, err = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status) VALUES(?,?,'broken','waiting')`, taskID, renditionID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := res.LastInsertId()
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	if _, _, _, _, _, _, _, err = w.claimNextJob(); err == nil {
		t.Fatal("expected hydration failure")
	}
	var status string
	var owner, lease sql.NullString
	if err = db.QueryRow(`SELECT status,lease_owner,lease_until FROM pretranscode_rendition_job WHERE id=?`, jobID).Scan(&status, &owner, &lease); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" || owner.Valid || lease.Valid {
		t.Fatalf("job leaked=%s owner=%v lease=%v", status, owner, lease)
	}
}

func TestProgressUpdateRequiresCurrentOwner(t *testing.T) {
	db := newTestDB(t)
	taskID, _, _, _ := seedLinkedPrepareTerminal(t, db, 1)
	res, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,lease_owner,lease_until) VALUES(?,1,'360p','running',10,'current',datetime(CURRENT_TIMESTAMP,'+90 seconds'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := res.LastInsertId()
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	if err = w.updateJobProgress(context.Background(), claimedJob{ID: jobID, TaskID: taskID, Owner: "stale"}, 55); !errors.Is(err, ErrJobOwnershipLost) {
		t.Fatalf("stale progress err=%v", err)
	}
	var progress int
	_ = db.QueryRow(`SELECT progress FROM pretranscode_rendition_job WHERE id=?`, jobID).Scan(&progress)
	if progress != 10 {
		t.Fatalf("stale owner changed progress=%d", progress)
	}
	if err = w.updateJobProgress(context.Background(), claimedJob{ID: jobID, TaskID: taskID, Owner: "current"}, 55); err != nil {
		t.Fatal(err)
	}
}

func TestClaimNextJobHonorsAvailableAt(t *testing.T) {
	db := newTestDB(t)
	preset, err := (&PresetService{DB: db}).CreatePreset(CreatePresetInput{Name: "future", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k", Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}}})
	if err != nil {
		t.Fatal(err)
	}
	mid := seedVideo(t, db, t.TempDir(), "fid-future", "Future")
	ids, err := (&TaskService{DB: db, TranscodeDir: t.TempDir()}).CreateTask([]int64{mid}, preset.ID, "normal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE pretranscode_rendition_job SET available_at=datetime(CURRENT_TIMESTAMP,'+5 minutes') WHERE task_id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	if _, _, _, _, _, _, _, err = w.claimNextJob(); err != sql.ErrNoRows {
		t.Fatalf("future claim err=%v", err)
	}
	if _, err = db.Exec(`UPDATE pretranscode_rendition_job SET available_at=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE task_id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if job, _, _, _, _, _, _, err := w.claimNextJob(); err != nil || job == nil {
		t.Fatalf("due claim job=%v err=%v", job, err)
	}
}

func TestProcessNextCompletesSkippedRenditionThroughPublicationAggregate(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	mediaID := seedVideo(t, db, root, "fid-process-next", "Process next")
	if _, err := db.Exec(`UPDATE media SET width=1,height=1 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	var libraryID int64
	if err := db.QueryRow(`SELECT library_id FROM media WHERE id=?`, mediaID).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE library SET jit_prepare_on_ingest=1 WHERE id=?`, libraryID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'done','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanID, _ := res.LastInsertId()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := publication.NewPlanner(publication.PlanOptions{PreparePlanner: ingestPreparePlanner{}, Capabilities: publication.NewCapabilityMatrix([]string{"prepare"})}).PlanNewMediaTx(context.Background(), tx, publication.NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE media_ingest_step SET status='done' WHERE run_id=? AND step_type<>'prepare'`, run.ID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = publication.AggregateTx(context.Background(), tx, run.ID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(db, nil, "missing-ffmpeg", t.TempDir(), 1, 1)
	_, _ = db.Exec(`UPDATE transcode_task SET status='running',lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE ingest_run_id=?`, worker.claimOwner, run.ID)
	_, _ = db.Exec(`UPDATE media_ingest_step SET status='running',attempts=1,lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE run_id=? AND step_type='prepare'`, worker.claimOwner, run.ID)
	processed := 0
	for {
		ok, processErr := worker.ProcessNext(context.Background())
		if processErr != nil {
			t.Fatal(processErr)
		}
		if !ok {
			break
		}
		processed++
	}
	var jobs int
	var state string
	if err = db.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id WHERE t.ingest_run_id=? AND j.status='done'`, run.ID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if processed == 0 || jobs != processed || state != "published" {
		t.Fatalf("processed=%d jobs=%d state=%s", processed, jobs, state)
	}
}
