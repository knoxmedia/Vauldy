package pretranscode

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"knox-media/internal/publication"
	"knox-media/internal/store"
)

type prepareCancelSnapshot struct{ task, step, job, run, media, taskOwner, stepOwner string }

func optionalPrepareFixture(t *testing.T, status string) (*sql.DB, *TaskService, int64, int64, int64, int64, int64) {
	t.Helper()
	db := newTestDB(t)
	svc, task, job, run, step, media := seedManagedLinkedTask(t, db, status)
	owner := any(nil)
	jobOwner := any(nil)
	if status == "running" {
		owner = "test-parent"
		jobOwner = "job-owner"
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET required=0,status=?,lease_owner=? WHERE id=?`, status, owner, step); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcode_task SET status=?,lease_owner=? WHERE id=?`, status, owner, task); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE pretranscode_rendition_job SET status=?,lease_owner=? WHERE id=?`, status, jobOwner, job); err != nil {
		t.Fatal(err)
	}
	return db, svc, task, job, run, step, media
}
func snapPrepareCancel(t *testing.T, db *sql.DB, task, job, run, step, media int64) prepareCancelSnapshot {
	t.Helper()
	var s prepareCancelSnapshot
	if err := db.QueryRow(`SELECT t.status,s.status,j.status,r.status,m.publication_state,COALESCE(t.lease_owner,''),COALESCE(s.lease_owner,'') FROM transcode_task t JOIN media_ingest_step s ON s.id=? JOIN pretranscode_rendition_job j ON j.id=? JOIN media_ingest_run r ON r.id=? JOIN media m ON m.id=? WHERE t.id=?`, step, job, run, media, task).Scan(&s.task, &s.step, &s.job, &s.run, &s.media, &s.taskOwner, &s.stepOwner); err != nil {
		t.Fatal(err)
	}
	return s
}
func assertCancelRejectedUnchanged(t *testing.T, db *sql.DB, svc *TaskService, task, job, run, step, media int64) {
	t.Helper()
	before := snapPrepareCancel(t, db, task, job, run, step, media)
	if err := svc.CancelTask(task); !errors.Is(err, ErrTaskNotCancellable) {
		t.Fatalf("err=%v", err)
	}
	after := snapPrepareCancel(t, db, task, job, run, step, media)
	if before != after {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}

func TestOptionalPrepareCancelRejectsStaleGeneration(t *testing.T) {
	db, svc, task, job, run, step, media := optionalPrepareFixture(t, "waiting")
	_, _ = db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=?`, media)
	assertCancelRejectedUnchanged(t, db, svc, task, job, run, step, media)
}
func TestOptionalPrepareCancelRejectsSupersededRun(t *testing.T) {
	db, svc, task, job, run, step, media := optionalPrepareFixture(t, "running")
	_, _ = db.Exec(`UPDATE media_ingest_run SET superseded_at=CURRENT_TIMESTAMP WHERE id=?`, run)
	assertCancelRejectedUnchanged(t, db, svc, task, job, run, step, media)
}
func TestOptionalPrepareCancelRejectsRelinkedIdentity(t *testing.T) {
	db, svc, task, job, run, step, media := optionalPrepareFixture(t, "running")
	r, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,lease_owner)VALUES(?,?,1,'scrape',0,'running','test-parent')`, run, media)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := r.LastInsertId()
	if _, err = db.Exec(`UPDATE transcode_task SET ingest_step_id=? WHERE id=?`, other, task); err != nil {
		t.Fatal(err)
	}
	assertCancelRejectedUnchanged(t, db, svc, task, job, run, step, media)
}
func TestOptionalPrepareCancelCASRaceRollsBack(t *testing.T) {
	db, svc, task, job, run, step, media := optionalPrepareFixture(t, "running")
	before := snapPrepareCancel(t, db, task, job, run, step, media)
	svc.beforeCancelCAS = func(tx store.ImmediateConnTx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE media SET ingest_generation=2 WHERE id=?`, media)
		return err
	}
	if err := svc.CancelTask(task); !errors.Is(err, ErrTaskNotCancellable) {
		t.Fatalf("err=%v", err)
	}
	after := snapPrepareCancel(t, db, task, job, run, step, media)
	if before != after {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
	var generation int64
	if err := db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, media).Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("generation=%d err=%v", generation, err)
	}
}
func TestOptionalPrepareCancelWaitingAndRunning(t *testing.T) {
	for _, status := range []string{"waiting", "running"} {
		t.Run(status, func(t *testing.T) {
			db, svc, task, job, run, step, media := optionalPrepareFixture(t, status)
			if err := svc.CancelTask(task); err != nil {
				t.Fatal(err)
			}
			got := snapPrepareCancel(t, db, task, job, run, step, media)
			if got.task != "cancelled" || got.step != "cancelled" || got.job != "cancelled" {
				t.Fatalf("%+v", got)
			}
		})
	}
}
func TestPrepareCancelCommitCancelsAllActiveRenditions(t *testing.T) {
	db, svc, task, _, _, _, _ := optionalPrepareFixture(t, "running")
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 2, 1)
	var ids []int64
	for i := 0; i < 2; i++ {
		r, e := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status)VALUES(?,?,'running')`, task, "active")
		if e != nil {
			t.Fatal(e)
		}
		id, _ := r.LastInsertId()
		ids = append(ids, id)
	}
	cancelled := make(chan int, 2)
	w.mu.Lock()
	for _, id := range ids {
		id := id
		ctx, c := context.WithCancel(context.Background())
		w.running[id] = &activeRendition{taskID: task, cancel: c, token: &struct{}{}}
		go func() { <-ctx.Done(); cancelled <- int(id) }()
	}
	w.mu.Unlock()
	svc.CancelActive = w.CancelParent
	if err := svc.CancelTask(task); err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for range ids {
		seen[<-cancelled] = true
	}
	if len(seen) != 2 {
		t.Fatal(seen)
	}
}
func TestPrepareCancelRollbackDoesNotCancelProcesses(t *testing.T) {
	db, svc, task, job, run, step, media := optionalPrepareFixture(t, "running")
	before := snapPrepareCancel(t, db, task, job, run, step, media)
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	ctx, c := context.WithCancel(context.Background())
	defer c()
	w.mu.Lock()
	w.running[job] = &activeRendition{taskID: task, cancel: c, token: &struct{}{}}
	w.mu.Unlock()
	called := 0
	svc.CancelActive = func(id int64) { called++; w.CancelParent(id) }
	sentinel := errors.New("rollback")
	svc.beforeCancelCAS = func(store.ImmediateConnTx) error { return sentinel }
	if err := svc.CancelTask(task); !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	if called != 0 {
		t.Fatalf("CancelActive called=%d", called)
	}
	select {
	case <-ctx.Done():
		t.Fatal("rollback cancelled process")
	default:
	}
	after := snapPrepareCancel(t, db, task, job, run, step, media)
	if before != after {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}
func TestPrepareRenewExtendsParentStepAndRendition(t *testing.T) {
	db := newTestDB(t)
	task, _, step, _ := seedLinkedPrepareTerminal(t, db, 1)
	r, e := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status,lease_owner,lease_until)VALUES(?,'x','running','job-owner',datetime(CURRENT_TIMESTAMP,'+1 second'))`, task)
	if e != nil {
		t.Fatal(e)
	}
	jobID, _ := r.LastInsertId()
	job := exactClaimedJob(t, db, jobID, task, "job-owner")
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	if err := w.renewJobLease(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT (julianday(j.lease_until)>julianday('now','+60 seconds'))+(julianday(t.lease_until)>julianday('now','+60 seconds'))+(julianday(s.lease_until)>julianday('now','+60 seconds')) FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id JOIN media_ingest_step s ON s.id=? WHERE j.id=?`, step, jobID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("extended=%d err=%v", n, err)
	}
}
func TestPrepareMissingImmutableParentRejected(t *testing.T) {
	db := newTestDB(t)
	task, _, _, _ := seedLinkedPrepareTerminal(t, db, 1)
	r, _ := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status,lease_owner)VALUES(?,'x','running','job')`, task)
	id, _ := r.LastInsertId()
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	_, err := w.finalizeJobAndTaskTx(context.Background(), claimedJob{ID: id, TaskID: task, Owner: "job"}, renditionJobTerminal{Status: "done"})
	if !errors.Is(err, ErrJobOwnershipLost) {
		t.Fatalf("err=%v", err)
	}
}
func TestPrepareLeaseLossCancelsActiveExecution(t *testing.T) {
	db := newTestDB(t)
	task, _, step, _ := seedLinkedPrepareTerminal(t, db, 1)
	r, _ := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status,lease_owner)VALUES(?,'x','running','job')`, task)
	id, _ := r.LastInsertId()
	job := exactClaimedJob(t, db, id, task, "job")
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	w.leaseRenewInterval = time.Millisecond
	execCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	lost := make(chan error, 1)
	started := make(chan struct{})
	blocked := make(chan struct{})
	go func() { close(started); <-execCtx.Done(); close(blocked) }()
	<-started
	_, _ = db.Exec(`UPDATE media_ingest_step SET lease_owner='other' WHERE id=?`, step)
	go w.renewJobLeaseLoop(execCtx, job, done, cancel, lost)
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("production lease loop did not cancel execution")
	}
	if !errors.Is(<-lost, ErrJobOwnershipLost) {
		t.Fatal("wrong lease result")
	}
	close(done)
}
func TestPrepareSecondWorkerCannotAdoptLiveParent(t *testing.T) {
	db := newTestDB(t)
	task, _, _, _ := seedLinkedPrepareTerminal(t, db, 1)
	_, _ = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status)VALUES(?,'x','waiting')`, task)
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	w.claimOwner = "second"
	if _, _, _, _, _, _, _, err := w.claimNextJob(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err=%v", err)
	}
}
func TestRecoverExpiredPrepareParentThenExactReclaim(t *testing.T) {
	db := newTestDB(t)
	task, _, step, _ := seedLinkedPrepareTerminal(t, db, 1)
	var old string
	_ = db.QueryRow(`SELECT lease_owner FROM transcode_task WHERE id=?`, task).Scan(&old)
	r, _ := db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status,config_snapshot_json)VALUES(?,'x','waiting','{"preset":{"id":1,"name":"x","output_format":"hls","video_codec":"libx264","audio_codec":"aac"},"rendition":{"id":1,"name":"x","height":360,"video_bitrate":"1k"}}')`, task)
	jobID, _ := r.LastInsertId()
	_, _ = db.Exec(`UPDATE transcode_task SET lease_until=datetime('now','-1 second') WHERE id=?;UPDATE media_ingest_step SET lease_until=datetime('now','-1 second') WHERE id=?`, task, step)
	if n, e := RecoverExpiredPrepareParents(context.Background(), db, 10); e != nil || n != 1 {
		t.Fatalf("recover=%d err=%v", n, e)
	}
	registry := publication.NewCapabilityMatrix([]string{"prepare"})
	payload, e := publication.ClaimEligible(context.Background(), db, publication.ClaimRequest{Family: publication.QueuePrepare, TaskType: "prepare", Owner: "reclaimer", Registry: registry})
	if e != nil || payload == nil {
		t.Fatalf("claim=%+v err=%v", payload, e)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1, registry)
	parent := publication.PrepareParentIdentity{TaskID: payload.QueueID, RunID: payload.RunID.Int64, StepID: payload.StepID.Int64, MediaID: payload.MediaID, Generation: payload.Generation.Int64, Owner: payload.Owner}
	w.parentClaims[parent.TaskID] = parent
	job, _, _, _, _, _, _, e := w.claimNextJob()
	if e != nil {
		t.Fatal(e)
	}
	if job.ID != jobID || parent.Owner == old {
		t.Fatalf("job=%+v old=%q parent=%+v", job, old, parent)
	}
	var ts, ss, js, to, so, jo string
	if e = db.QueryRow(`SELECT t.status,s.status,j.status,t.lease_owner,s.lease_owner,j.lease_owner FROM transcode_task t JOIN media_ingest_step s ON s.id=? JOIN pretranscode_rendition_job j ON j.id=? WHERE t.id=?`, step, jobID, task).Scan(&ts, &ss, &js, &to, &so, &jo); e != nil {
		t.Fatal(e)
	}
	if ts != "running" || ss != "running" || js != "running" || to != parent.Owner || so != parent.Owner || jo != job.Owner {
		t.Fatalf("states=%s/%s/%s owners=%q/%q/%q", ts, ss, js, to, so, jo)
	}
}

var _ = publication.PrepareParentIdentity{}

func TestPrepareWorkerDrainsTwoSequentialParents(t *testing.T) {
	db := newTestDB(t)
	p, _ := (&PresetService{DB: db}).CreatePreset(CreatePresetInput{Name: "seq", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", Renditions: []Rendition{{Name: "x", Height: 360, VideoBitrate: "1k"}}})
	m1 := seedVideo(t, db, t.TempDir(), "seq1", "one")
	m2 := seedVideo(t, db, t.TempDir(), "seq2", "two")
	ids, e := (&TaskService{DB: db, TranscodeDir: t.TempDir()}).CreateTask([]int64{m1, m2}, p.ID, "normal")
	if e != nil {
		t.Fatal(e)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	a, _, _, _, _, _, _, e := w.claimNextJob()
	if e != nil {
		t.Fatal(e)
	}
	_, _ = db.Exec(`UPDATE pretranscode_rendition_job SET status='done',lease_owner=NULL WHERE id=?;UPDATE transcode_task SET status='done',lease_owner=NULL WHERE id=?`, a.ID, a.TaskID)
	b, _, _, _, _, _, _, e := w.claimNextJob()
	if e != nil {
		t.Fatal(e)
	}
	if a.TaskID == b.TaskID || !((a.TaskID == ids[0] && b.TaskID == ids[1]) || (a.TaskID == ids[1] && b.TaskID == ids[0])) {
		t.Fatalf("claims=%d,%d ids=%v", a.TaskID, b.TaskID, ids)
	}
}
func TestPrepareTerminalOrNoJobParentDoesNotStarveNext(t *testing.T) {
	for _, state := range []string{"failed", "cancelled", "nojob"} {
		t.Run(state, func(t *testing.T) {
			db := newTestDB(t)
			stale, _, _, _ := seedLinkedPrepareTerminal(t, db, 1)
			if state != "nojob" {
				_, _ = db.Exec(`UPDATE transcode_task SET status=? WHERE id=?`, state, stale)
			}
			p, _ := (&PresetService{DB: db}).CreatePreset(CreatePresetInput{Name: "next" + state, OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", Renditions: []Rendition{{Name: "x", Height: 360, VideoBitrate: "1k"}}})
			mid := seedVideo(t, db, t.TempDir(), "next"+state, "next")
			ids, e := (&TaskService{DB: db, TranscodeDir: t.TempDir()}).CreateTask([]int64{mid}, p.ID, "normal")
			if e != nil {
				t.Fatal(e)
			}
			w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
			var parent publication.PrepareParentIdentity
			if err := db.QueryRow(`SELECT id,ingest_run_id,ingest_step_id,media_id,generation,lease_owner FROM transcode_task WHERE id=?`, stale).Scan(&parent.TaskID, &parent.RunID, &parent.StepID, &parent.MediaID, &parent.Generation, &parent.Owner); err != nil {
				t.Fatal(err)
			}
			w.parentClaims[stale] = parent
			job, _, _, _, _, _, _, e := w.claimNextJob()
			if e != nil {
				t.Fatal(e)
			}
			if job.TaskID != ids[0] {
				t.Fatalf("claimed=%d want=%d", job.TaskID, ids[0])
			}
		})
	}
}
