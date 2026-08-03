package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"knox-media/internal/postingest"
	"knox-media/internal/publication"
	"knox-media/internal/scancoord"
	"knox-media/internal/scanner"
	"knox-media/internal/store"
	"knox-media/pkg/ffprobe"
)

func TestScan100MediaEnqueuesWithoutFFmpegOrGoroutineFanout(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "production-scan.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	res, err := db.Exec(`INSERT INTO library(name,type,path,preview_extract,encrypted_assets_enabled) VALUES('production','video',?,1,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(filepath.Join(root, "video-"+strconv.Itoa(i)+".mp4"), []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	planner := publication.NewPlanner(publication.PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true})
	var probes atomic.Int64
	sc := &scanner.Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		probes.Add(1)
		return &ffprobe.Summary{}, nil
	}}
	coordinator, err := scancoord.New(db, scancoord.Options{
		LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second,
		OwnerInstanceID: "production-test", Scanner: sc,
		OnMediaDiscoveredTx: scancoord.MediaDiscoveredTxFunc(postingest.NewScanMediaDiscoveredTxCallback(planner)),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	result, err := coordinator.Submit(context.Background(), scancoord.ScanRequest{LibraryID: libraryID, Source: scancoord.SourceManual, Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	waitForScanTask(t, db, result.TaskID)
	if probes.Load() != 100 {
		t.Fatalf("probes=%d want 100", probes.Load())
	}
	var mediaCount, runCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE library_id=?`, libraryID).Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 100 {
		t.Fatalf("media=%d want 100", mediaCount)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE scan_task_id=?`, result.TaskID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 100 {
		t.Fatalf("runs=%d want 100", runCount)
	}
	rows, err := db.Query(`SELECT step_type,COUNT(*) FROM media_ingest_step GROUP BY step_type`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for rows.Next() {
		var step string
		var count int
		if err := rows.Scan(&step, &count); err != nil {
			t.Fatal(err)
		}
		got[step] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	wantSteps := []string{"atrack_extract", "encrypt", "media_visible", "poster", "preview", "scrape", "subtitle_extract"}
	sort.Strings(wantSteps)
	if len(got) != len(wantSteps) {
		t.Fatalf("step types=%v want %v", got, wantSteps)
	}
	for _, step := range wantSteps {
		if got[step] != 100 {
			t.Fatalf("step=%s rows=%d want 100", step, got[step])
		}
	}
	assertPostIngestOwnership(t, db, result.TaskID, 500)
	assertPerMediaPublicationLinks(t, db, libraryID, result.TaskID)
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+5 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if delta := runtime.NumGoroutine() - before; delta > 5 {
		t.Fatalf("goroutine delta=%d want <=5 after convergence wait", delta)
	}
}

func waitForScanTask(t *testing.T, db *sql.DB, taskID int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var status string
		if err := db.QueryRow(`SELECT status FROM scan_task WHERE id=?`, taskID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "done" {
			return
		}
		if status == "failed" || status == "cancelled" {
			t.Fatalf("scan status=%s", status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan task %d timed out in %s", taskID, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertPostIngestOwnership(t *testing.T, db *sql.DB, taskID int64, want int) {
	t.Helper()
	var total, owned int
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN scan_task_id=? THEN 1 ELSE 0 END),0) FROM post_ingest_task`, taskID).Scan(&total, &owned); err != nil {
		t.Fatal(err)
	}
	if total != want || owned != want {
		t.Fatalf("rows=%d owned=%d want %d", total, owned, want)
	}
	for _, typ := range []postingest.TaskType{postingest.TaskPoster, postingest.TaskPreview, postingest.TaskSubtitle, postingest.TaskAtrack, postingest.TaskEncrypt} {
		var count, typeOwned int
		if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN scan_task_id=? THEN 1 ELSE 0 END),0) FROM post_ingest_task WHERE task_type=?`, taskID, typ).Scan(&count, &typeOwned); err != nil {
			t.Fatal(err)
		}
		if count != 100 || typeOwned != 100 {
			t.Fatalf("type=%s rows=%d owned=%d want 100", typ, count, typeOwned)
		}
	}
}

func assertPerMediaPublicationLinks(t *testing.T, db *sql.DB, libraryID, taskID int64) {
	t.Helper()
	wantSteps := map[string]bool{"poster": true, "scrape": true, "preview": true, "subtitle_extract": true, "atrack_extract": true, "encrypt": true, "media_visible": true}
	wantQueue := map[string]bool{"poster": true, "preview": true, "subtitle": true, "atrack": true, "encrypt": true}
	rows, err := db.Query(`SELECT id FROM media WHERE library_id=? ORDER BY id`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	mediaCount := 0
	for rows.Next() {
		var mediaID int64
		if err := rows.Scan(&mediaID); err != nil {
			t.Fatal(err)
		}
		mediaCount++
		var runID, runMediaID, generation, runTaskID int64
		if err := db.QueryRow(`SELECT id,media_id,generation,scan_task_id FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runID, &runMediaID, &generation, &runTaskID); err != nil {
			t.Fatalf("media %d run: %v", mediaID, err)
		}
		if runMediaID != mediaID || runTaskID != taskID {
			t.Fatalf("media %d run linkage media=%d task=%d", mediaID, runMediaID, runTaskID)
		}
		stepRows, err := db.Query(`SELECT step_type FROM media_ingest_step WHERE run_id=? AND media_id=? AND generation=?`, runID, mediaID, generation)
		if err != nil {
			t.Fatal(err)
		}
		steps := map[string]bool{}
		for stepRows.Next() {
			var stepType string
			if err := stepRows.Scan(&stepType); err != nil {
				_ = stepRows.Close()
				t.Fatal(err)
			}
			steps[stepType] = true
		}
		if err := stepRows.Err(); err != nil {
			_ = stepRows.Close()
			t.Fatal(err)
		}
		_ = stepRows.Close()
		if len(steps) != len(wantSteps) {
			t.Fatalf("media %d steps=%v", mediaID, steps)
		}
		for stepType := range wantSteps {
			if !steps[stepType] {
				t.Fatalf("media %d missing step %s", mediaID, stepType)
			}
		}
		queueRows, err := db.Query(`SELECT q.media_id,q.ingest_run_id,q.ingest_step_id,q.task_type,q.generation,q.scan_task_id,r.media_id,s.run_id,s.media_id,s.generation,s.step_type FROM post_ingest_task q JOIN media_ingest_run r ON r.id=q.ingest_run_id JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE q.media_id=? ORDER BY q.id`, mediaID)
		if err != nil {
			t.Fatal(err)
		}
		queued := map[string]bool{}
		for queueRows.Next() {
			var qMediaID, qRunID, qStepID, qGeneration, qTaskID int64
			var runMediaID, stepRunID, stepMediaID, stepGeneration int64
			var taskType, stepType string
			if err := queueRows.Scan(&qMediaID, &qRunID, &qStepID, &taskType, &qGeneration, &qTaskID, &runMediaID, &stepRunID, &stepMediaID, &stepGeneration, &stepType); err != nil {
				_ = queueRows.Close()
				t.Fatal(err)
			}
			if qMediaID != mediaID || qRunID != runID || qStepID <= 0 || qGeneration != generation || qTaskID != taskID || runMediaID != mediaID || stepRunID != runID || stepMediaID != mediaID || stepGeneration != generation {
				t.Fatalf("media %d invalid queue link", mediaID)
			}
			switch stepType {
			case "subtitle_extract":
				if taskType != "subtitle" {
					t.Fatalf("media %d subtitle_extract queue type=%s", mediaID, taskType)
				}
			case "atrack_extract":
				if taskType != "atrack" {
					t.Fatalf("media %d atrack_extract queue type=%s", mediaID, taskType)
				}
			case "keyframe_extract":
				if taskType != "keyframe" {
					t.Fatalf("media %d keyframe_extract queue type=%s", mediaID, taskType)
				}
			default:
				if taskType != stepType {
					t.Fatalf("media %d queue type=%s step=%s", mediaID, taskType, stepType)
				}
			}
			queued[taskType] = true
		}
		if err := queueRows.Err(); err != nil {
			_ = queueRows.Close()
			t.Fatal(err)
		}
		_ = queueRows.Close()
		if len(queued) != len(wantQueue) {
			t.Fatalf("media %d queue=%v", mediaID, queued)
		}
		for taskType := range wantQueue {
			if !queued[taskType] {
				t.Fatalf("media %d missing queue task %s", mediaID, taskType)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if mediaCount != 100 {
		t.Fatalf("media=%d want 100", mediaCount)
	}
}

func TestRestartRecoversStartupQueueAndResumesPublication(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart-publication.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('restart','video',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'done','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,title,file_path,file_type,status) VALUES(?,'restart-video','Restart',?,'video','active')`, libraryID, filepath.Join(root, "restart.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := publication.NewPlanner(publication.PlanOptions{}).PlanNewMediaTx(context.Background(), tx, publication.NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE post_ingest_task SET max_attempts=2 WHERE ingest_run_id=? AND task_type='poster';
UPDATE media_ingest_step SET max_attempts=2 WHERE run_id=? AND step_type='poster'`, run.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	oldQueue := postingest.NewQueue(db, "before-restart", nil)
	claimed, err := oldQueue.Claim(context.Background(), postingest.TaskPoster)
	if err != nil || claimed == nil {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if _, err = db.Exec(`UPDATE post_ingest_task SET lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restarted := postingest.NewQueue(db, "after-restart", nil)
	if err = recoverStartupTasks(context.Background(), db, restarted, startupRecoveryRoots(t, "")); err != nil {
		t.Fatal(err)
	}
	resumed, err := restarted.Claim(context.Background(), postingest.TaskPoster)
	if err != nil || resumed == nil || resumed.ID != claimed.ID {
		t.Fatalf("resumed=%v err=%v", resumed, err)
	}
	if err = restarted.Complete(context.Background(), *resumed); err != nil {
		t.Fatal(err)
	}
	var taskState, stepState, mediaState string
	if err = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, claimed.ID).Scan(&taskState); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT status FROM media_ingest_step WHERE run_id=? AND step_type='poster'`, run.ID).Scan(&stepState); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&mediaState); err != nil {
		t.Fatal(err)
	}
	if taskState != "done" || stepState != "done" || mediaState != "published" {
		t.Fatalf("task=%s step=%s media=%s", taskState, stepState, mediaState)
	}
}

func TestScannerCallbackRegressionUsesCallbacksField(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "callback-regression.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "callback.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('callbacks','video',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	calls := 0
	sc := &scanner.Scanner{DB: db, SkipHash: true, OnMediaAdded: func(int64, string, string) { panic("scanner field callback ran") }, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) { return &ffprobe.Summary{}, nil }}
	added, err := sc.ScanLibraryFoldersWithContextAndCallbacks(context.Background(), libraryID, []string{root}, scanner.ScanCallbacks{OnMediaAdded: func(context.Context, int64, string, string) error { calls++; return nil }})
	if err != nil || added != 1 || calls != 1 {
		t.Fatalf("added=%d calls=%d err=%v", added, calls, err)
	}
}

type phase1ScanExec publication.StepType

func (a phase1ScanExec) TaskType() publication.StepType                 { return publication.StepType(a) }
func (phase1ScanExec) Execute(context.Context, int64) error             { return nil }

type phase1ScanRegistry map[publication.StepType]publication.ExecutableTaskAdapter

func (r phase1ScanRegistry) Adapter(step publication.StepType) (publication.ExecutableTaskAdapter, bool) {
	a, ok := r[step]
	return a, ok
}

func TestScanLibraryClosureDrivenPlanIncludesRecognitionGraph(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "phase1-closure-scan.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "closure.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path,subtitle_recognize,ai_analysis,encrypted_assets_enabled) VALUES('closure','video',?,1,1,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	adapters := phase1ScanRegistry{
		publication.StepSubtitleRecognize: phase1ScanExec(publication.StepSubtitleRecognize),
		publication.StepAIAnalysis:        phase1ScanExec(publication.StepAIAnalysis),
	}
	planner := publication.NewPlanner(publication.PlanOptions{
		EncryptGlobal:             true,
		ExecutableAdapters:        adapters,
		EncryptedSourceStrategies: publication.DefaultEncryptedSourceStrategies(),
	})
	sc := &scanner.Scanner{DB: db, SkipHash: true, ProbePath: func(context.Context, int64, string) (*ffprobe.Summary, error) {
		return &ffprobe.Summary{}, nil
	}}
	coordinator, err := scancoord.New(db, scancoord.Options{
		LeaseDuration: time.Minute, HeartbeatInterval: 20 * time.Second,
		OwnerInstanceID: "phase1-closure", Scanner: sc,
		OnMediaDiscoveredTx: scancoord.MediaDiscoveredTxFunc(postingest.NewScanMediaDiscoveredTxCallback(planner)),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Submit(context.Background(), scancoord.ScanRequest{LibraryID: libraryID, Source: scancoord.SourceManual, Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	waitForScanTask(t, db, result.TaskID)

	rows, err := db.Query(`SELECT step_type,COUNT(*) FROM media_ingest_step GROUP BY step_type`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for rows.Next() {
		var step string
		var count int
		if err := rows.Scan(&step, &count); err != nil {
			t.Fatal(err)
		}
		got[step] = count
	}
	_ = rows.Close()
	for _, want := range []string{"poster", "encrypt", "scrape", "subtitle_extract", "atrack_extract", "subtitle_recognize", "ai_analysis", "media_visible"} {
		if got[want] != 1 {
			t.Fatalf("step=%s count=%d want 1 (got=%v)", want, got[want], got)
		}
	}
	var edgeCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step_dependency d
JOIN media_ingest_step child ON child.id=d.step_id
JOIN media_ingest_step parent ON parent.id=d.depends_on_step_id
WHERE child.step_type='ai_analysis' AND parent.step_type='subtitle_recognize' AND d.dependency_kind='success'`).Scan(&edgeCount); err != nil || edgeCount != 1 {
		t.Fatalf("ai<-recognize edge count=%d err=%v", edgeCount, err)
	}
}
