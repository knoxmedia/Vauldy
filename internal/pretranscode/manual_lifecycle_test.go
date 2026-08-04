package pretranscode

import (
	"context"
	"errors"
	"testing"
)

func legacyClaimedJob(t *testing.T, w *Worker) (*claimedJob, error) {
	t.Helper()
	j, _, _, _, _, _, _, e := w.claimNextJob()
	return j, e
}
func TestManualTaskLifecycleCompleteRenewProgress(t *testing.T) {
	db := newTestDB(t)
	p, _ := (&PresetService{DB: db}).CreatePreset(CreatePresetInput{Name: "manual-ok", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", Renditions: []Rendition{{Name: "x", Height: 360, VideoBitrate: "1k"}}})
	m := seedVideo(t, db, t.TempDir(), "manual-ok", "ok")
	ids, e := (&TaskService{DB: db, TranscodeDir: t.TempDir()}).CreateTask([]int64{m}, p.ID, "normal")
	if e != nil {
		t.Fatal(e)
	}
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	job, e := legacyClaimedJob(t, w)
	if e != nil {
		t.Fatal(e)
	}
	if job.LegacyParent.TaskID != ids[0] || job.LegacyParent.Owner == "" || job.Parent.TaskID != 0 {
		t.Fatalf("identity=%+v linked=%+v", job.LegacyParent, job.Parent)
	}
	if e = w.renewJobLease(context.Background(), *job); e != nil {
		t.Fatal(e)
	}
	if e = w.updateJobProgress(context.Background(), *job, 47); e != nil {
		t.Fatal(e)
	}
	if terminal, e := w.finalizeJobAndTaskTx(context.Background(), *job, renditionJobTerminal{Status: "done", Progress: 100, OutputPath: "out"}); e != nil || !terminal {
		t.Fatalf("terminal=%v err=%v", terminal, e)
	}
	var task, js string
	var progress int
	if e = db.QueryRow(`SELECT t.status,j.status,j.progress FROM transcode_task t JOIN pretranscode_rendition_job j ON j.task_id=t.id WHERE t.id=?`, ids[0]).Scan(&task, &js, &progress); e != nil {
		t.Fatal(e)
	}
	if task != "done" || js != "done" || progress != 100 {
		t.Fatalf("%s/%s/%d", task, js, progress)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step`).Scan(&n)
	if n != 0 {
		t.Fatalf("publication rows=%d", n)
	}
}
func TestManualTaskLifecycleFailure(t *testing.T) {
	db := newTestDB(t)
	p, _ := (&PresetService{DB: db}).CreatePreset(CreatePresetInput{Name: "manual-fail", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", Renditions: []Rendition{{Name: "x", Height: 360, VideoBitrate: "1k"}}})
	m := seedVideo(t, db, t.TempDir(), "manual-fail", "fail")
	ids, _ := (&TaskService{DB: db, TranscodeDir: t.TempDir()}).CreateTask([]int64{m}, p.ID, "normal")
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	job, e := legacyClaimedJob(t, w)
	if e != nil {
		t.Fatal(e)
	}
	if terminal, e := w.finalizeJobAndTaskTx(context.Background(), *job, renditionJobTerminal{Status: "failed", ErrorMessage: "boom"}); e != nil || !terminal {
		t.Fatalf("terminal=%v err=%v", terminal, e)
	}
	var task, js string
	_ = db.QueryRow(`SELECT t.status,j.status FROM transcode_task t JOIN pretranscode_rendition_job j ON j.task_id=t.id WHERE t.id=?`, ids[0]).Scan(&task, &js)
	if task != "failed" || js != "failed" {
		t.Fatalf("%s/%s", task, js)
	}
}
func TestManualTaskLifecycleRejectsStaleOwnerAndMalformedLink(t *testing.T) {
	db := newTestDB(t)
	p, _ := (&PresetService{DB: db}).CreatePreset(CreatePresetInput{Name: "manual-stale", OutputFormat: "hls", VideoCodec: "libx264", AudioCodec: "aac", Renditions: []Rendition{{Name: "x", Height: 360, VideoBitrate: "1k"}}})
	m := seedVideo(t, db, t.TempDir(), "manual-stale", "stale")
	ids, _ := (&TaskService{DB: db, TranscodeDir: t.TempDir()}).CreateTask([]int64{m}, p.ID, "normal")
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	job, e := legacyClaimedJob(t, w)
	if e != nil {
		t.Fatal(e)
	}
	stale := *job
	stale.LegacyParent.Owner = "stale"
	if e = w.renewJobLease(context.Background(), stale); !errors.Is(e, ErrJobOwnershipLost) {
		t.Fatalf("renew=%v", e)
	}
	if e = w.updateJobProgress(context.Background(), stale, 10); !errors.Is(e, ErrJobOwnershipLost) {
		t.Fatalf("progress=%v", e)
	}
	if _, e = w.finalizeJobAndTaskTx(context.Background(), stale, renditionJobTerminal{Status: "done"}); !errors.Is(e, ErrJobOwnershipLost) {
		t.Fatalf("final=%v", e)
	}
	_, _ = db.Exec(`UPDATE transcode_task SET ingest_run_id=99,ingest_step_id=99,generation=1,media_id=? WHERE id=?`, m, ids[0])
	if _, _, _, _, _, _, _, e = w.claimNextJob(); e == nil {
		t.Fatal("partial/malformed linked task fell back to legacy")
	}
}
