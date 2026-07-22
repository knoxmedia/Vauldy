package pretranscode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"knox-media/internal/publication"
)

func TestIngestPreparePlannerCreatesExecutableExactlyLinkedTask(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	mediaID := seedVideo(t, db, root, "fid-ingest-plan", "ingest plan")
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
	run, err := publication.NewPlanner(publication.PlanOptions{PreparePlanner: ingestPreparePlanner{}}).PlanNewMediaTx(context.Background(), tx, publication.NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var taskID, linkedRun, linkedStep, generation, presetID int64
	var taskType, status string
	if err = db.QueryRow(`SELECT id,task_type,status,ingest_run_id,ingest_step_id,generation,preset_id FROM transcode_task WHERE file_id='fid-ingest-plan'`).Scan(&taskID, &taskType, &status, &linkedRun, &linkedStep, &generation, &presetID); err != nil {
		t.Fatal(err)
	}
	var stepType string
	if err = db.QueryRow(`SELECT step_type FROM media_ingest_step WHERE id=?`, linkedStep).Scan(&stepType); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err = db.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job WHERE task_id=? AND status='waiting'`, taskID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if taskType != "pretranscode" || status != "waiting" || linkedRun != run.ID || generation != run.Generation || stepType != "prepare" || presetID == 0 || jobs == 0 {
		t.Fatalf("task=%d %s/%s link=%d/%d gen=%d preset=%d jobs=%d run=%+v", taskID, taskType, status, linkedRun, linkedStep, generation, presetID, jobs, run)
	}
}

func TestIngestPreparePlannerRejectsMissingPresetAndCallerCanRollback(t *testing.T) {
	db := newTestDB(t)
	mediaID := seedVideo(t, db, t.TempDir(), "fid-no-preset", "none")
	if _, err := db.Exec(`DELETE FROM transcode_preset`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = (ingestPreparePlanner{}).PlanIngestPrepareTx(context.Background(), tx, mediaID, 1, 1, 1); err == nil {
		t.Fatal("expected no preset error")
	}
	_ = tx.Rollback()
}

func TestIngestPreparePlannerRejectsMismatchedStepLinkage(t *testing.T) {
	db := newTestDB(t)
	mediaID := seedVideo(t, db, t.TempDir(), "fid-bad-link", "bad link")
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = (ingestPreparePlanner{}).PlanIngestPrepareTx(context.Background(), tx, mediaID, 99, 88, 1); err == nil {
		t.Fatal("expected mismatched link error")
	}
	_ = tx.Rollback()
}

func TestIngestPrepareSnapshotSurvivesPresetUpdate(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	mediaID := seedVideo(t, db, root, "snapshot-media", "Snapshot")
	var presetID int64
	if err := db.QueryRow(`SELECT id FROM transcode_preset WHERE is_builtin=1 ORDER BY sort_order,id LIMIT 1`).Scan(&presetID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,1,'scan','processing',0,'{}')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'prepare',1,'waiting')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	tx, _ := db.Begin()
	if err = (ingestPreparePlanner{}).PlanIngestPrepareTx(context.Background(), tx, mediaID, runID, stepID, 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var oldJSON string
	if err = db.QueryRow(`SELECT config_snapshot_json FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id WHERE t.ingest_run_id=? ORDER BY j.id LIMIT 1`, runID).Scan(&oldJSON); err != nil {
		t.Fatal(err)
	}
	var old struct {
		Rendition struct {
			Name   string `json:"name"`
			Height int    `json:"height"`
		} `json:"rendition"`
	}
	if err = json.Unmarshal([]byte(oldJSON), &old); err != nil {
		t.Fatal(err)
	}
	_, err = (&PresetService{DB: db}).UpdatePreset(presetID, CreatePresetInput{Name: "mutated", OutputFormat: "hls", VideoCodec: "libx265", AudioCodec: "aac", AudioBitrate: "64k", HWFallback: true, Renditions: []Rendition{{Name: "mutated", Height: 111, VideoBitrate: "1k"}}})
	if err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err = db.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id WHERE t.ingest_run_id=?`, runID).Scan(&jobs); err != nil || jobs == 0 {
		t.Fatalf("snapshot jobs=%d err=%v", jobs, err)
	}
	w := NewWorker(db, nil, "ffmpeg", root, 1, 1)
	_, p, r, _, _, _, _, err := w.claimNextJob()
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != old.Rendition.Name || r.Height != old.Rendition.Height || p.VideoCodec == "libx265" {
		t.Fatalf("hydrated current preset: p=%+v r=%+v old=%+v", p, r, old)
	}
}

func TestLinkedIngestPrepareNeverFallsBackWhenSnapshotMissing(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	mediaID := seedVideo(t, db, root, "missing-snapshot", "Missing Snapshot")
	res, _ := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,1,'scan','processing',0,'{}')`, mediaID)
	runID, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'prepare',1,'waiting')`, runID, mediaID)
	stepID, _ := res.LastInsertId()
	tx, _ := db.Begin()
	if err := (ingestPreparePlanner{}).PlanIngestPrepareTx(context.Background(), tx, mediaID, runID, stepID, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE pretranscode_rendition_job SET config_snapshot_json=NULL`); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, "ffmpeg", root, 1, 1)
	if _, _, _, _, _, _, _, err := w.claimNextJob(); err == nil || !strings.Contains(err.Error(), "immutable snapshot") {
		t.Fatalf("err=%v want immutable snapshot error", err)
	}
}

func TestRecoverLinkedIngestPrepareUsesTaskSnapshot(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	mediaID := seedVideo(t, db, root, "recover-snapshot", "Recover Snapshot")
	res, _ := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,1,'scan','processing',0,'{}')`, mediaID)
	runID, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'prepare',1,'waiting')`, runID, mediaID)
	stepID, _ := res.LastInsertId()
	tx, _ := db.Begin()
	if err := (ingestPreparePlanner{}).PlanIngestPrepareTx(context.Background(), tx, mediaID, runID, stepID, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var taskID int64
	var snapshot string
	if err := db.QueryRow(`SELECT t.id,m.ingest_jobs_snapshot_json FROM transcode_task t JOIN pretranscode_task_meta m ON m.task_id=t.id WHERE t.ingest_run_id=?`, runID).Scan(&taskID, &snapshot); err != nil {
		t.Fatal(err)
	}
	var oldCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job WHERE task_id=?`, taskID).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM pretranscode_rendition_job WHERE task_id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcode_preset SET video_codec='mutated'`); err != nil {
		t.Fatal(err)
	}
	svc := &TaskService{DB: db}
	if got := svc.RecoverOrphanedTasks(); got != 1 {
		t.Fatalf("fixed=%d", got)
	}
	var gotCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job WHERE task_id=? AND config_snapshot_json IS NOT NULL`, taskID).Scan(&gotCount); err != nil || gotCount != oldCount {
		t.Fatalf("jobs=%d want=%d err=%v snapshot=%s", gotCount, oldCount, err, snapshot)
	}
	if got := svc.RecoverOrphanedTasks(); got != 0 {
		t.Fatalf("second fixed=%d", got)
	}
}

func TestRecoverLinkedMalformedSnapshotFailsStepAndDegrades(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	mediaID := seedVideo(t, db, root, "recover-bad", "Recover Bad")
	_, _ = db.Exec(`UPDATE media SET ingest_generation=1,publication_state='processing' WHERE id=?`, mediaID)
	res, _ := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,1,'scan','processing',1,'{}')`, mediaID)
	runID, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'prepare',1,'waiting')`, runID, mediaID)
	stepID, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO transcode_task(file_id,status,task_type,media_id,ingest_run_id,ingest_step_id,generation) SELECT file_id,'waiting','pretranscode',id,?,?,1 FROM media WHERE id=?`, runID, stepID, mediaID)
	taskID, _ := res.LastInsertId()
	_, _ = db.Exec(`INSERT INTO pretranscode_task_meta(task_id,preset_id,output_format,ingest_jobs_snapshot_json) VALUES(?,1,'hls','{bad')`, taskID)
	svc := &TaskService{DB: db}
	if got := svc.RecoverOrphanedTasks(); got != 0 {
		t.Fatalf("fixed=%d", got)
	}
	var task, step, run, media string
	if err := db.QueryRow(`SELECT t.status,s.status,r.status,m.publication_state FROM transcode_task t JOIN media_ingest_step s ON s.id=t.ingest_step_id JOIN media_ingest_run r ON r.id=t.ingest_run_id JOIN media m ON m.id=r.media_id WHERE t.id=?`, taskID).Scan(&task, &step, &run, &media); err != nil {
		t.Fatal(err)
	}
	if task != "failed" || step != "failed" || run != "degraded" || media != "degraded" {
		t.Fatalf("states=%s/%s/%s/%s", task, step, run, media)
	}
	if got := svc.RecoverOrphanedTasks(); got != 0 {
		t.Fatalf("second fixed=%d", got)
	}
}
