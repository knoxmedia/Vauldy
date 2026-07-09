package pretranscode

import (
	"database/sql"
	"testing"
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
