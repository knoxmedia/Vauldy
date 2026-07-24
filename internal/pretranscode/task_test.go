package pretranscode

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"

	"knox-media/internal/publication"
	"knox-media/internal/store"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// seedVideo inserts a library + media row with an on-disk source file and returns the media id.
func seedVideo(t *testing.T, db *sql.DB, root, fileID, title string) int64 {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	path := filepath.Join(root, fileID+".mp4")
	if err := os.WriteFile(path, []byte("fake-video"), 0o644); err != nil {
		t.Fatalf("write source %s: %v", path, err)
	}
	_, _ = db.Exec(`INSERT OR IGNORE INTO library (id, name, type, path) VALUES (1, 'L', 'video', ?)`, root)
	res, err := db.Exec(`INSERT INTO media (library_id, file_id, title, file_path, file_type, duration, height) VALUES (1, ?, ?, ?, 'video', 120, 720)`,
		fileID, title, path)
	if err != nil {
		t.Fatalf("seed media %s: %v", fileID, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestCreateTaskInsertsRenditionJobs(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, err := presetSvc.CreatePreset(CreatePresetInput{
		Name: "task-test", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{
			{Name: "360p", Height: 360, VideoBitrate: "850k"},
			{Name: "720p", Height: 720, VideoBitrate: "2800k"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mid := seedVideo(t, db, t.TempDir(), "fid-1", "Test")

	taskSvc := &TaskService{DB: db, TranscodeDir: t.TempDir()}
	ids, err := taskSvc.CreateTask([]int64{mid}, preset.ID, "normal")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 task id, got %d", len(ids))
	}
	jobs, err := taskSvc.ListRenditionJobs(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 rendition jobs, got %d", len(jobs))
	}
	for _, j := range jobs {
		if j.Status != "waiting" {
			t.Errorf("job %d should be waiting, got %s", j.ID, j.Status)
		}
	}
}

func TestListTasksTypeFilter(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, _ := presetSvc.CreatePreset(CreatePresetInput{
		Name: "filter-test", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	root := t.TempDir()
	midA := seedVideo(t, db, root, "fid-a", "A")
	seedVideo(t, db, root, "fid-b", "B")

	taskSvc := &TaskService{DB: db, TranscodeDir: t.TempDir()}
	_, _ = taskSvc.CreateTask([]int64{midA}, preset.ID, "normal")
	_, _ = db.Exec(`INSERT INTO transcode_task (file_id, quality, status, progress, task_type) VALUES (?, '720p', 'waiting', 0, 'batch')`, "fid-b")

	pre, err := taskSvc.ListTasks("pretranscode", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre) != 1 || pre[0].TaskType != "pretranscode" {
		t.Errorf("pretranscode filter wrong: %+v", pre)
	}
	batch, _ := taskSvc.ListTasks("batch", 50)
	if len(batch) != 1 || batch[0].TaskType != "batch" {
		t.Errorf("batch filter wrong: %+v", batch)
	}
	all, _ := taskSvc.ListTasks("all", 50)
	if len(all) != 2 {
		t.Errorf("all filter should return 2 tasks, got %d", len(all))
	}
}

func TestCancelAndRetryTask(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, _ := presetSvc.CreatePreset(CreatePresetInput{
		Name: "cancel-test", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	mid := seedVideo(t, db, t.TempDir(), "fid-c", "C")
	taskSvc := &TaskService{DB: db, TranscodeDir: t.TempDir()}
	ids, _ := taskSvc.CreateTask([]int64{mid}, preset.ID, "normal")
	id := ids[0]
	if err := taskSvc.CancelTask(id); err != nil {
		t.Fatal(err)
	}
	jobs, _ := taskSvc.ListRenditionJobs(id)
	if jobs[0].Status != "cancelled" {
		t.Errorf("job should be cancelled, got %s", jobs[0].Status)
	}
	if err := taskSvc.RetryTask(id); err != nil {
		t.Fatal(err)
	}
	jobs, _ = taskSvc.ListRenditionJobs(id)
	if jobs[0].Status != "waiting" {
		t.Errorf("job should be waiting after retry, got %s", jobs[0].Status)
	}
}

func TestRetryTaskHealsWaitingTaskWithFailedJobs(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, _ := presetSvc.CreatePreset(CreatePresetInput{
		Name: "orphan-retry", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	mid := seedVideo(t, db, t.TempDir(), "fid-orphan", "Orphan")
	taskSvc := &TaskService{DB: db, TranscodeDir: t.TempDir()}
	ids, _ := taskSvc.CreateTask([]int64{mid}, preset.ID, "normal")
	id := ids[0]

	// Simulate generic task-list retry: task waiting, rendition jobs still failed.
	_, _ = db.Exec(`UPDATE transcode_task SET status='waiting', error_message=NULL WHERE id=?`, id)
	_, _ = db.Exec(`UPDATE pretranscode_rendition_job SET status='failed', error_message='boom' WHERE task_id=?`, id)

	if err := taskSvc.RetryTask(id); err != nil {
		t.Fatal(err)
	}
	jobs, _ := taskSvc.ListRenditionJobs(id)
	if len(jobs) != 1 || jobs[0].Status != "waiting" {
		t.Fatalf("expected waiting rendition job, got %+v", jobs)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, id).Scan(&status)
	if status != "waiting" {
		t.Fatalf("task should stay waiting, got %s", status)
	}
}

func TestAggregateProgress(t *testing.T) {
	jobs := []RenditionJob{
		{Status: "done", Progress: 100},
		{Status: "running", Progress: 50},
		{Status: "waiting", Progress: 0},
	}
	got := AggregateProgress(jobs)
	if got != 50 {
		t.Errorf("expected avg progress 50, got %d", got)
	}
	jobs[1].Status, jobs[1].Progress = "done", 100
	jobs[2].Status, jobs[2].Progress = "done", 100
	if got := AggregateProgress(jobs); got != 100 {
		t.Errorf("expected 100 when all done, got %d", got)
	}
}

func TestDeleteTaskRemovesOutputDir(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, _ := presetSvc.CreatePreset(CreatePresetInput{
		Name: "del-test", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	mid := seedVideo(t, db, t.TempDir(), "fid-d", "D")
	tmpDir := t.TempDir()
	taskSvc := &TaskService{DB: db, TranscodeDir: tmpDir}
	ids, _ := taskSvc.CreateTask([]int64{mid}, preset.ID, "normal")
	id := ids[0]
	outDir := filepath.Join(tmpDir, "fid-d", "preset"+itoa(preset.ID))
	_ = os.MkdirAll(outDir, 0o755)
	f, _ := os.Create(filepath.Join(outDir, "720p.m3u8"))
	_ = f.Close()
	_, _ = db.Exec(`UPDATE pretranscode_task_meta SET output_path=? WHERE task_id=?`, outDir, id)
	if err := taskSvc.DeleteTask(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("output dir should be removed, got err=%v", err)
	}
}

func TestBatchCreateFilterUntranscoded(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, _ := presetSvc.CreatePreset(CreatePresetInput{
		Name: "batch-test", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	midE := seedVideo(t, db, t.TempDir(), "fid-e", "E")
	seedVideo(t, db, t.TempDir(), "fid-f", "F")
	taskSvc := &TaskService{DB: db, TranscodeDir: t.TempDir()}
	ids, _ := taskSvc.CreateTask([]int64{midE}, preset.ID, "normal")
	_, _ = db.Exec(`UPDATE pretranscode_rendition_job SET status='done', progress=100 WHERE task_id=?`, ids[0])
	_, _ = db.Exec(`UPDATE transcode_task SET status='done', progress=100 WHERE id=?`, ids[0])
	n, err := taskSvc.CreateBatchTask(1, preset.ID, "untranscoded", "low")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 untranscoded file, got %d", n)
	}
}

func TestPlaybackHasPretranscodeOutput(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, _ := presetSvc.CreatePreset(CreatePresetInput{
		Name: "pb-test", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	mid := seedVideo(t, db, t.TempDir(), "fid-pb", "PB")
	taskSvc := &TaskService{DB: db, TranscodeDir: t.TempDir()}
	ids, _ := taskSvc.CreateTask([]int64{mid}, preset.ID, "normal")
	pb := &PlaybackService{DB: db, TranscodeDir: t.TempDir()}
	if pb.HasPretranscodeOutput("fid-pb") {
		t.Errorf("should have no output before completion")
	}
	_, _ = db.Exec(`UPDATE pretranscode_rendition_job SET status='done', progress=100 WHERE task_id=?`, ids[0])
	if !pb.HasPretranscodeOutput("fid-pb") {
		t.Errorf("should have output after rendition done")
	}
	status, err := pb.GetPretranscodeStatus(context.Background(), "fid-pb")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Available || status.PresetName != "pb-test" {
		t.Errorf("status wrong: %+v", status)
	}
}

func TestPlaybackOnMediaDeleted(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, _ := presetSvc.CreatePreset(CreatePresetInput{
		Name: "del-pb", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k",
		Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}},
	})
	tmpDir := t.TempDir()
	mid := seedVideo(t, db, t.TempDir(), "fid-del", "DEL")
	taskSvc := &TaskService{DB: db, TranscodeDir: tmpDir}
	ids, _ := taskSvc.CreateTask([]int64{mid}, preset.ID, "normal")
	outDir := filepath.Join(tmpDir, "fid-del")
	_ = os.MkdirAll(outDir, 0o755)
	f, _ := os.Create(filepath.Join(outDir, "720p.m3u8"))
	_ = f.Close()
	_, _ = db.Exec(`UPDATE pretranscode_task_meta SET output_path=? WHERE task_id=?`, outDir, ids[0])
	pb := &PlaybackService{DB: db, TranscodeDir: tmpDir}
	if err := pb.OnMediaDeleted(context.Background(), mid, []string{"fid-del"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("output dir should be removed on media delete")
	}
}

// itoa is a tiny strconv-free helper.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func seedManagedLinkedTask(t *testing.T, db *sql.DB, status string) (svc *TaskService, taskID, jobID, runID, stepID, mediaID int64) {
	t.Helper()
	taskID, runID, stepID, mediaID = seedLinkedPrepareTerminal(t, db, 1)
	res, err := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,lease_owner,lease_until) VALUES(?,1,'360p',?,50,'old-owner',datetime(CURRENT_TIMESTAMP,'+1 hour'))`, taskID, status)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ = res.LastInsertId()
	if _, err = db.Exec(`UPDATE transcode_task SET status=? WHERE id=?`, status, taskID); err != nil {
		t.Fatal(err)
	}
	if status == "failed" {
		if _, err = db.Exec(`UPDATE media_ingest_step SET status='failed',attempts=max_attempts,last_error='boom',lease_owner='old',lease_until='2040-01-01',finished_at=CURRENT_TIMESTAMP WHERE id=?`, stepID); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`UPDATE media_ingest_run SET status='degraded',preserve_visibility=1,error_message='boom' WHERE id=?`, runID); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`UPDATE media SET publication_state='degraded',publication_error='boom' WHERE id=?`, mediaID); err != nil {
			t.Fatal(err)
		}
	}
	return &TaskService{DB: db, TranscodeDir: t.TempDir()}, taskID, jobID, runID, stepID, mediaID
}

func pretranscodeSQLiteError(t *testing.T, code int) error {
	t.Helper()
	err := &sqlite.Error{}
	v := reflect.ValueOf(err).Elem().FieldByName("code")
	reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().SetInt(int64(code))
	return err
}

func TestCancelTaskRetriesWholeTransactionAfterBusySnapshot(t *testing.T) {
	db := newTestDB(t)
	svc, taskID, jobID, runID, stepID, mediaID := seedManagedLinkedTask(t, db, "running")
	attempts := 0
	svc.cancelTaskAttempt = func(ctx context.Context, id int64) error {
		attempts++
		if attempts == 1 {
			return pretranscodeSQLiteError(t, sqlite3.SQLITE_BUSY_SNAPSHOT)
		}
		return svc.cancelTaskAttemptTx(ctx, id)
	}
	if err := svc.CancelTask(taskID); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "cancelled", "cancelled", "cancelled", "cancelled", "cancelled")
}

func TestCancelTaskBusyExhaustionLeavesAllLayersUnchanged(t *testing.T) {
	db := newTestDB(t)
	svc, taskID, jobID, runID, stepID, mediaID := seedManagedLinkedTask(t, db, "running")
	svc.cancelTaskAttempt = func(context.Context, int64) error { return pretranscodeSQLiteError(t, sqlite3.SQLITE_BUSY) }
	if err := svc.CancelTask(taskID); !store.IsSQLiteBusy(err) {
		t.Fatalf("err=%v", err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "running", "running", "running", "processing", "processing")
}

func TestLinkedRequiredPrepareCancelTaskPersistsAdminRunIntentAtomically(t *testing.T) {
	db := newTestDB(t)
	svc, taskID, jobID, runID, stepID, mediaID := seedManagedLinkedTask(t, db, "running")
	if err := svc.CancelTask(taskID); err != nil {
		t.Fatal(err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "cancelled", "cancelled", "cancelled", "cancelled", "cancelled")

	var reason, runError string
	var finished sql.NullTime
	if err := db.QueryRow(`SELECT terminal_reason,error_message,finished_at FROM media_ingest_run WHERE id=?`, runID).Scan(&reason, &runError, &finished); err != nil {
		t.Fatal(err)
	}
	if reason != "admin_cancelled" || !finished.Valid {
		t.Fatalf("reason=%q error=%q finished=%v", reason, runError, finished)
	}
}

func TestLinkedRequiredPrepareCancelRejectsTerminalOrStaleTargets(t *testing.T) {
	for _, tc := range []struct {
		name, taskStatus, stepStatus string
		stale                        bool
	}{
		{name: "done", taskStatus: "done", stepStatus: "done"},
		{name: "failed", taskStatus: "failed", stepStatus: "failed"},
		{name: "cancelled", taskStatus: "cancelled", stepStatus: "cancelled"},
		{name: "stale generation", taskStatus: "running", stepStatus: "running", stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			svc, taskID, _, runID, stepID, mediaID := seedManagedLinkedTask(t, db, "running")
			if _, err := db.Exec(`UPDATE transcode_task SET status=? WHERE id=?`, tc.taskStatus, taskID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media_ingest_step SET status=? WHERE id=?`, tc.stepStatus, stepID); err != nil {
				t.Fatal(err)
			}
			if tc.stale {
				if _, err := db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=?`, mediaID); err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.CancelTask(taskID); !errors.Is(err, ErrTaskNotCancellable) {
				t.Fatalf("err=%v", err)
			}
			var runStatus, reason, taskStatus, stepStatus string
			if err := db.QueryRow(`SELECT status,terminal_reason FROM media_ingest_run WHERE id=?`, runID).Scan(&runStatus, &reason); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, taskID).Scan(&taskStatus); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus); err != nil {
				t.Fatal(err)
			}
			if runStatus != "processing" || reason != "" || taskStatus != tc.taskStatus || stepStatus != tc.stepStatus {
				t.Fatalf("run=%s reason=%q task=%s step=%s", runStatus, reason, taskStatus, stepStatus)
			}
		})
	}
}

func TestLinkedRequiredPrepareCancelRejectsDoneTargetWithoutCancellingOtherPendingWork(t *testing.T) {
	db := newTestDB(t)
	svc, taskID, _, runID, stepID, mediaID := seedManagedLinkedTask(t, db, "running")
	if _, err := db.Exec(`UPDATE transcode_task SET status='done' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='done' WHERE id=?`, stepID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'poster',1,'waiting')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	otherStep, _ := res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status) VALUES(?,?,?,1,'poster','waiting')`, mediaID, runID, otherStep); err != nil {
		t.Fatal(err)
	}
	if err = svc.CancelTask(taskID); !errors.Is(err, ErrTaskNotCancellable) {
		t.Fatalf("err=%v", err)
	}
	var runStatus, otherStepStatus, otherTaskStatus string
	if err = db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, otherStep).Scan(&otherStepStatus); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT status FROM post_ingest_task WHERE ingest_step_id=?`, otherStep).Scan(&otherTaskStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "processing" || otherStepStatus != "waiting" || otherTaskStatus != "waiting" {
		t.Fatalf("run=%s step=%s task=%s", runStatus, otherStepStatus, otherTaskStatus)
	}
}

func TestLinkedOptionalPrepareCancelDoesNotCancelRunOutcome(t *testing.T) {
	db := newTestDB(t)
	svc, taskID, jobID, runID, stepID, mediaID := seedManagedLinkedTask(t, db, "running")
	if _, err := db.Exec(`UPDATE media_ingest_step SET required=0 WHERE id=?`, stepID); err != nil {
		t.Fatal(err)
	}
	if err := svc.CancelTask(taskID); err != nil {
		t.Fatal(err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "cancelled", "cancelled", "cancelled", "published", "published")
	var reason string
	if err := db.QueryRow(`SELECT terminal_reason FROM media_ingest_run WHERE id=?`, runID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("terminal reason=%q", reason)
	}
}

func TestLinkedPrepareRetryTaskStartsNewRoundThenWorkerCanPublish(t *testing.T) {
	db := newTestDB(t)
	svc, taskID, jobID, runID, stepID, mediaID := seedManagedLinkedTask(t, db, "failed")
	if err := svc.RetryTask(taskID); err != nil {
		t.Fatal(err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "waiting", "waiting", "waiting", "processing", "degraded")
	var attempts int
	var lastError string
	var owner, lease, finished sql.NullString
	if err := db.QueryRow(`SELECT attempts,last_error,lease_owner,lease_until,finished_at FROM media_ingest_step WHERE id=?`, stepID).Scan(&attempts, &lastError, &owner, &lease, &finished); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || lastError != "" || owner.Valid || lease.Valid || finished.Valid {
		t.Fatalf("retry step=%d/%q owner=%v lease=%v finished=%v", attempts, lastError, owner, lease, finished)
	}
	if _, err := db.Exec(`UPDATE pretranscode_rendition_job SET status='running',lease_owner='retry-owner',lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	_, _ = db.Exec(`UPDATE transcode_task SET status='running',lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE id=?;UPDATE media_ingest_step SET status='running',attempts=1,lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE id=?`, w.claimOwner, taskID, w.claimOwner, stepID)
	if _, err := w.finalizeJobAndTaskTx(context.Background(), claimedJob{ID: jobID, TaskID: taskID, Owner: "retry-owner", Parent: publication.PrepareParentIdentity{TaskID: taskID, RunID: runID, StepID: stepID, MediaID: mediaID, Generation: 1, Owner: w.claimOwner}}, renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "/retry", Encoder: "libx264"}); err != nil {
		t.Fatal(err)
	}
	assertPrepareTerminalState(t, db, jobID, taskID, stepID, runID, mediaID, "done", "done", "done", "published", "published")
}

func TestLinkedPrepareRejectsPartialRenditionAndPauseOperations(t *testing.T) {
	db := newTestDB(t)
	svc, taskID, jobID, _, _, _ := seedManagedLinkedTask(t, db, "running")
	for name, call := range map[string]func() error{"cancel rendition": func() error { return svc.CancelRenditionJob(jobID) }, "retry rendition": func() error { return svc.RetryRenditionJob(jobID) }, "pause": func() error { return svc.PauseTask(taskID) }, "resume": func() error { return svc.ResumeTask(taskID) }} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrLinkedIngestTaskOperation) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestLegacyTaskManagementBehaviorRemainsAvailable(t *testing.T) {
	db := newTestDB(t)
	presetSvc := &PresetService{DB: db}
	preset, _ := presetSvc.CreatePreset(CreatePresetInput{Name: "legacy-management", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", AudioBitrate: "128k", Renditions: []Rendition{{Name: "720p", Height: 720, VideoBitrate: "2800k"}}})
	mid := seedVideo(t, db, t.TempDir(), "fid-legacy-management", "legacy")
	svc := &TaskService{DB: db, TranscodeDir: t.TempDir()}
	ids, err := svc.CreateTask([]int64{mid}, preset.ID, "normal")
	if err != nil {
		t.Fatal(err)
	}
	jobs, _ := svc.ListRenditionJobs(ids[0])
	jobID := jobs[0].ID
	if err = svc.CancelRenditionJob(jobID); err != nil {
		t.Fatal(err)
	}
	if err = svc.RetryRenditionJob(jobID); err != nil {
		t.Fatal(err)
	}
	if err = svc.PauseTask(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err = svc.ResumeTask(ids[0]); err != nil {
		t.Fatal(err)
	}
}

func TestLinkedManagementDeletionAPIsRejectWithoutMutation(t *testing.T) {
	for _, name := range []string{"delete", "remove", "batch"} {
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			svc, task, job, _, _, _ := seedManagedLinkedTask(t, db, "running")
			var err error
			if name == "delete" {
				err = svc.DeleteTask(task)
			} else if name == "remove" {
				err = svc.RemoveRenditionJob(job)
			} else {
				err = svc.BatchRemoveRenditionJobs([]int64{job})
			}
			if !errors.Is(err, ErrLinkedIngestTaskOperation) {
				t.Fatalf("err=%v", err)
			}
			var n int
			_ = db.QueryRow(`SELECT COUNT(*) FROM transcode_task WHERE id=?`, task).Scan(&n)
			if n != 1 {
				t.Fatal("mutated")
			}
		})
	}
}
