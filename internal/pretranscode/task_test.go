package pretranscode

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
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
