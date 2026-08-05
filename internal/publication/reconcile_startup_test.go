package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"knox-media/internal/store"
)

func seedActiveV1(t *testing.T, dbPath, fileType, state string) (*Planner, int64) {
	t.Helper()
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	libType := "video"
	if fileType == "image" {
		libType = "photo"
	}
	r, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('legacy',?,?)`, libType, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lid, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state,published_at,ingest_generation) VALUES(?,'f','x',?,'active',?,CASE WHEN ? IN ('published','degraded') THEN CURRENT_TIMESTAMP END,1)`, lid, fileType, state, state)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := r.LastInsertId()
	r, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json,policy_version) VALUES(?,1,'scan','processing',0,'{}',1)`, mid)
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := r.LastInsertId()
	if _, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,lease_owner) VALUES(?,?,1,'poster',1,'running','old')`, rid, mid); err != nil {
		t.Fatal(err)
	}
	return NewPlanner(PlanOptions{}), mid
}

func TestActiveV1ReplacementVideoAndPhotoCurrentPolicy(t *testing.T) {
	for _, tc := range []struct{ typ, state, required string }{{"video", "published", "poster"}, {"image", "degraded", "thumbnail"}} {
		t.Run(tc.typ, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "v1.sqlite")
			planner, mid := seedActiveV1(t, path, tc.typ, tc.state)
			db, _ := store.OpenSQLite(path)
			defer db.Close()
			n, err := ReconcileStartupPublicationV2(context.Background(), db, planner)
			if err != nil || n != 1 {
				t.Fatalf("n=%d err=%v", n, err)
			}
			var generation, policy, preserve int
			var oldStatus, reason, step string
			if err = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mid).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT status,terminal_reason FROM media_ingest_run WHERE media_id=? AND generation=1`, mid).Scan(&oldStatus, &reason); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT r.policy_version,r.preserve_visibility,s.step_type FROM media_ingest_run r JOIN media_ingest_step s ON s.run_id=r.id WHERE r.media_id=? AND r.generation=2 AND s.required=1 ORDER BY s.id LIMIT 1`, mid).Scan(&policy, &preserve, &step); err != nil {
				t.Fatal(err)
			}
			if generation != 2 || policy != CurrentPolicyVersion || preserve != 1 || step != tc.required || oldStatus != "cancelled" || reason != "superseded_by_policy_v2" {
				t.Fatalf("gen=%d policy=%d preserve=%d step=%s old=%s reason=%s", generation, policy, preserve, step, oldStatus, reason)
			}
		})
	}
}

func TestActiveV1ReplacementConcurrentIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "race.sqlite")
	planner, mid := seedActiveV1(t, path, "video", "published")
	db1, _ := store.OpenSQLite(path)
	defer db1.Close()
	db2, _ := store.OpenSQLite(path)
	defer db2.Close()
	start := make(chan struct{})
	var wg sync.WaitGroup
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for _, db := range []*sql.DB{db1, db2} {
		wg.Add(1)
		go func(db *sql.DB) {
			defer wg.Done()
			<-start
			n, e := ReconcileStartupPublicationV2(context.Background(), db, planner)
			counts <- n
			errs <- e
		}(db)
	}
	close(start)
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var runs int
	_ = db1.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=? AND policy_version=?`, mid, CurrentPolicyVersion).Scan(&runs)
	if total != 1 || runs != 1 {
		t.Fatalf("total=%d runs=%d", total, runs)
	}
	n, err := ReconcileStartupPublicationV2(context.Background(), db1, planner)
	if err != nil || n != 0 {
		t.Fatalf("second n=%d err=%v", n, err)
	}
}

func TestActiveV1ReplacementNewEncryptionHidesVideoAndPhoto(t *testing.T) {
	for _, typ := range []string{"video", "image"} {
		t.Run(typ, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "security.sqlite")
			planner, mid := seedActiveV1(t, path, typ, "published")
			db, _ := store.OpenSQLite(path)
			defer db.Close()
			_, _ = db.Exec(`UPDATE library SET encrypted_assets_enabled=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mid)
			planner.options.EncryptGlobal = true
			n, err := ReplaceActiveV1Runs(context.Background(), db, planner)
			if err != nil || n != 1 {
				t.Fatalf("n=%d err=%v", n, err)
			}
			var preserve int
			var state string
			_ = db.QueryRow(`SELECT preserve_visibility FROM media_ingest_run WHERE media_id=? AND generation=2`, mid).Scan(&preserve)
			_ = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mid).Scan(&state)
			if preserve != 0 || state != "processing" {
				t.Fatalf("preserve=%d state=%s", preserve, state)
			}
		})
	}
}

func TestValidateCurrentV2RejectsEmptyAndMismatchedSnapshots(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{{"empty", `UPDATE media_ingest_run SET config_snapshot_json='{}'`}, {"required", `UPDATE media_ingest_step SET required=0 WHERE step_type='poster'`}, {"dependency", `DELETE FROM media_ingest_step_dependency`}} {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
			run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			if _, err := db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			err := ValidateAggregateCurrentV2(context.Background(), db)
			if err == nil {
				t.Fatalf("run %d accepted malformed %s", run.ID, tc.name)
			}
		})
	}
}

func TestValidateAggregateCurrentV2RepairsMissingPostIngestQueues(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='done',attempts=1,finished_at=CURRENT_TIMESTAMP WHERE run_id=? AND step_type='poster'`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM post_ingest_task WHERE ingest_run_id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAggregateCurrentV2(context.Background(), db); err != nil {
		t.Fatalf("validate after missing queues: %v", err)
	}
	var posterStatus string
	var posterAttempts, queues, steps int
	if err := db.QueryRow(`SELECT status,attempts FROM post_ingest_task WHERE ingest_run_id=? AND task_type='poster'`, run.ID).Scan(&posterStatus, &posterAttempts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE ingest_run_id=?`, run.ID).Scan(&queues); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=? AND step_type IN ('poster','thumbnail','preview','keyframe','subtitle','atrack','encrypt')`, run.ID).Scan(&steps); err != nil {
		t.Fatal(err)
	}
	if posterStatus != "done" || posterAttempts != 1 || queues != steps || steps < 1 {
		t.Fatalf("repaired poster=%s/%d queues=%d steps=%d", posterStatus, posterAttempts, queues, steps)
	}
}

func TestReconcileOrphanFailedQueueStateReopensFalseFailedAndCancelsOptionalOrphans(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 1, 0)
	if _, err := db.Exec(`UPDATE library SET encrypted_assets_enabled=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mid); err != nil {
		t.Fatal(err)
	}
	run := planAndCommit(t, db, NewPlanner(PlanOptions{EncryptGlobal: true}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='done',finished_at=CURRENT_TIMESTAMP WHERE run_id=? AND step_type='poster';
UPDATE post_ingest_task SET status='done',finished_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND task_type='poster';
UPDATE media_ingest_run SET status='failed',error_message='required step exhausted',finished_at=CURRENT_TIMESTAMP WHERE id=?;
UPDATE media SET publication_state='failed',publication_error='required step exhausted',ingest_generation=? WHERE id=?`, run.ID, run.ID, run.ID, run.Generation, mid); err != nil {
		t.Fatal(err)
	}

	n, err := ReconcileOrphanFailedQueueState(context.Background(), db)
	if err != nil || n < 1 {
		t.Fatalf("reopen n=%d err=%v", n, err)
	}
	var runState, mediaState, encryptStatus string
	if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, run.ID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mid).Scan(&mediaState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE ingest_run_id=? AND task_type='encrypt'`, run.ID).Scan(&encryptStatus); err != nil {
		t.Fatal(err)
	}
	if runState != "processing" || mediaState != "processing" || encryptStatus != "waiting" {
		t.Fatalf("run=%s media=%s encrypt=%s", runState, mediaState, encryptStatus)
	}

	db2, run2, media2 := aggregateFixture(t, "failed", 1, map[string]string{"poster": "done", "encrypt": "failed"})
	if _, err := db2.Exec(`UPDATE media_ingest_run SET policy_version=2,error_message='encrypt exhausted',finished_at=CURRENT_TIMESTAMP WHERE id=?`, run2); err != nil {
		t.Fatal(err)
	}
	if _, err := db2.Exec(`UPDATE media SET publication_state='failed',publication_error='encrypt exhausted' WHERE id=?`, media2); err != nil {
		t.Fatal(err)
	}
	res, err := db2.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'preview',0,'waiting')`, run2, media2)
	if err != nil {
		t.Fatal(err)
	}
	previewStep, _ := res.LastInsertId()
	if _, err := db2.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,max_attempts) VALUES(?,?,?,1,'preview','waiting',3)`, media2, run2, previewStep); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileOrphanFailedQueueState(context.Background(), db2); err != nil {
		t.Fatal(err)
	}
	var previewStatus string
	if err := db2.QueryRow(`SELECT status FROM post_ingest_task WHERE ingest_step_id=?`, previewStep).Scan(&previewStatus); err != nil {
		t.Fatal(err)
	}
	if previewStatus != "cancelled" {
		t.Fatalf("orphan preview=%s want cancelled", previewStatus)
	}
}

func TestReconcileSupersededQueueTasksCancelsWaitingAndRunning(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	if _, err := db.Exec(`UPDATE media_ingest_run SET status='published',finished_at=CURRENT_TIMESTAMP,superseded_at=CURRENT_TIMESTAMP,superseded_by_generation=99 WHERE id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	var stepID int64
	if err := db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='poster'`, run.ID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	var posterTaskID int64
	if err := db.QueryRow(`SELECT id FROM post_ingest_task WHERE ingest_step_id=?`, stepID).Scan(&posterTaskID); err != nil {
		t.Fatal(err)
	}

	n, err := ReconcileSupersededQueueTasks(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("expected at least 1 changed, got %d", n)
	}

	var taskStatus string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, posterTaskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "cancelled" {
		t.Fatalf("superseded queue task status=%s want cancelled", taskStatus)
	}

	var stepStatus string
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, stepID).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if stepStatus != "cancelled" {
		t.Fatalf("superseded step status=%s want cancelled", stepStatus)
	}
}

func TestReconcileSupersededQueueTasksLeavesCurrentRun(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})

	if _, err := ReconcileSupersededQueueTasks(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var waiting int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE ingest_run_id=? AND status='waiting'`, run.ID).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if waiting == 0 {
		t.Fatal("current run waiting tasks must not be cancelled")
	}
}

func TestValidateCurrentV2RejectsExactQueueSemanticMismatches(t *testing.T) {
	cases := []struct{ name, mutation string }{
		{"post task type", `UPDATE post_ingest_task SET task_type='encrypt' WHERE task_type='poster'`},
		{"post extra wrong execution", `INSERT INTO post_ingest_task(media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,task_type,status) SELECT media_id,scan_task_id,ingest_run_id,ingest_step_id,generation,'poster_repair',status FROM post_ingest_task WHERE task_type='poster'`},
		{"post generation", `UPDATE post_ingest_task SET generation=generation+1 WHERE task_type='poster'`},
		{"post media", `UPDATE post_ingest_task SET media_id=media_id+100 WHERE task_type='poster'`},
		{"post run", `UPDATE post_ingest_task SET ingest_run_id=ingest_run_id+100 WHERE task_type='poster'`},
		{"scrape source", `UPDATE scrape_task SET source='manual'`},
		{"scrape generation", `UPDATE scrape_task SET generation=generation+1`},
		{"scrape media", `UPDATE scrape_task SET media_id=media_id+100`},
		{"scrape run", `UPDATE scrape_task SET ingest_run_id=ingest_run_id+100`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
			planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			_, _ = db.Exec("PRAGMA foreign_keys=OFF")
			if _, err := db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			if err := ValidateAggregateCurrentV2(context.Background(), db); err == nil {
				t.Fatal("accepted invalid queue semantics")
			}
		})
	}
}

func TestValidateAggregateCurrentV2RepairsDesyncedPostIngestStatus(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
	planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='waiting',attempts=0,last_error='' WHERE task_type='subtitle'; UPDATE media_ingest_step SET status='failed',attempts=1,last_error='stale' WHERE step_type='subtitle_extract'`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAggregateCurrentV2(context.Background(), db); err != nil {
		t.Fatalf("validate after status desync: %v", err)
	}
	var stepStatus, queueStatus string
	if err := db.QueryRow(`SELECT s.status,p.status FROM media_ingest_step s JOIN post_ingest_task p ON p.ingest_step_id=s.id WHERE s.step_type='subtitle_extract' AND s.media_id=?`, mid).Scan(&stepStatus, &queueStatus); err != nil {
		t.Fatal(err)
	}
	if stepStatus != "waiting" || queueStatus != "waiting" {
		t.Fatalf("step=%s queue=%s want both waiting", stepStatus, queueStatus)
	}
}

func TestValidateCurrentV2RejectsPrepareWrongTypeAndIdentity(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{{"type", `UPDATE transcode_task SET task_type='manual'`}, {"generation", `UPDATE transcode_task SET generation=generation+1`}, {"media", `UPDATE transcode_task SET media_id=media_id+100`}, {"run", `UPDATE transcode_task SET ingest_run_id=ingest_run_id+100`}} {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 1)
			planner := NewPlanner(PlanOptions{PreparePlanner: &recordingPreparePlanner{}, Capabilities: NewCapabilityMatrix([]string{"prepare"})})
			run := planAndCommit(t, db, planner, NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			var stepID int64
			var fileID string
			if err := db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='prepare'`, run.ID).Scan(&stepID); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT file_id FROM media WHERE id=?`, mid).Scan(&fileID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO transcode_task(file_id,media_id,status,task_type,ingest_run_id,ingest_step_id,generation) VALUES(?,?,'waiting','pretranscode',?,?,1)`, fileID, mid, run.ID, stepID); err != nil {
				t.Fatal(err)
			}
			_, _ = db.Exec(`PRAGMA foreign_keys=OFF`)
			if _, err := db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			if err := ValidateAggregateCurrentV2(context.Background(), db); err == nil {
				t.Fatal("accepted invalid prepare queue")
			}
		})
	}
}

func TestPublicationColumnExistsTxUsesCallerTransaction(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE publication_probe (id INTEGER PRIMARY KEY, started_at TEXT)`); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		column string
		want   bool
	}{
		{column: "started_at", want: true},
		{column: "completed_at", want: false},
	} {
		got, err := publicationColumnExistsTx(ctx, tx, "publication_probe", tc.column)
		if err != nil {
			t.Fatalf("column %q: %v", tc.column, err)
		}
		if got != tc.want {
			t.Fatalf("column %q exists=%v want %v", tc.column, got, tc.want)
		}
	}
}

func TestStartupRepairsMissingCompatibilityQueueForV3LogicalNode(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	if _, err := db.Exec(`DELETE FROM post_ingest_task WHERE ingest_run_id=? AND task_type='subtitle'`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAggregateCurrentV2(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE q.ingest_run_id=? AND q.task_type='subtitle' AND s.step_type='subtitle_extract'`, run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestStartupRejectsV3PersistedGraphDrift(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{{"requiredness", `UPDATE media_ingest_step SET required=1 WHERE run_id=? AND step_type='preview'`}, {"edge identity", `UPDATE media_ingest_step_dependency SET depends_on_step_id=(SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='poster') WHERE step_id=(SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='preview')`}} {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 1, 0, 0)
			run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			var err error
			if tc.name == "edge identity" {
				_, err = db.Exec(tc.mutation, run.ID, run.ID)
			} else {
				_, err = db.Exec(tc.mutation, run.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateAggregateCurrentV2(context.Background(), db); err == nil {
				t.Fatal("accepted persisted graph drift")
			}
		})
	}
}

func TestStartupRejectsUnavailableV3RecognitionWithoutRepair(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
	_, _ = db.Exec(`UPDATE library SET subtitle_recognize=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mid)
	registry := fakeExecutableRegistry{StepSubtitleRecognize: fakeExecutableAdapter(StepSubtitleRecognize)}
	run := planAndCommit(t, db, NewPlanner(PlanOptions{ExecutableAdapters: registry}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	_, _ = db.Exec(`DELETE FROM post_ingest_task WHERE ingest_run_id=? AND task_type='subtitle_recognize'`, run.ID)
	err := ValidateAggregateCurrentPolicy(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "adapter unavailable") || !strings.Contains(err.Error(), "current policy v3") {
		t.Fatalf("err=%v", err)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE ingest_run_id=? AND task_type='subtitle_recognize'`, run.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("created rows=%d", count)
	}
}

func TestStartupV3AdapterFailuresPrecedeAllQueueMutation(t *testing.T) {
	for _, step := range []StepType{StepSubtitleRecognize, StepAIAnalysis} {
		t.Run(string(step), func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
			adapters := fakeExecutableRegistry{StepSubtitleRecognize: fakeExecutableAdapter(StepSubtitleRecognize), StepAIAnalysis: fakeExecutableAdapter(StepAIAnalysis)}
			column := "subtitle_recognize"
			if step == StepAIAnalysis {
				column = "ai_analysis"
			}
			_, _ = db.Exec(`UPDATE library SET `+column+`=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mid)
			run := planAndCommit(t, db, NewPlanner(PlanOptions{ExecutableAdapters: adapters}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			_, _ = db.Exec(`DELETE FROM post_ingest_task WHERE ingest_run_id=? AND task_type='subtitle'`, run.ID)
			_, _ = db.Exec(`DELETE FROM post_ingest_task WHERE ingest_run_id=? AND task_type=?`, run.ID, string(step))
			err := ValidateAggregateCurrentPolicy(context.Background(), db)
			if err == nil || !strings.Contains(err.Error(), "adapter unavailable") {
				t.Fatalf("err=%v", err)
			}
			var repaired int
			_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE ingest_run_id=? AND task_type='subtitle'`, run.ID).Scan(&repaired)
			if repaired != 0 {
				t.Fatalf("queue repaired before admission: %d", repaired)
			}
		})
	}
}

func TestStartupRejectsEffectiveOptionGraphOmissionWithoutRepair(t *testing.T) {
	for _, step := range []StepType{StepSubtitleRecognize, StepAIAnalysis} {
		t.Run(string(step), func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
			adapters := fakeExecutableRegistry{StepSubtitleRecognize: fakeExecutableAdapter(StepSubtitleRecognize), StepAIAnalysis: fakeExecutableAdapter(StepAIAnalysis)}
			column := "subtitle_recognize"
			if step == StepAIAnalysis {
				column = "ai_analysis"
			}
			_, _ = db.Exec(`UPDATE library SET `+column+`=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mid)
			run := planAndCommit(t, db, NewPlanner(PlanOptions{ExecutableAdapters: adapters}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			var raw string
			_ = db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw)
			var snapshot ConfigSnapshot
			_ = json.Unmarshal([]byte(raw), &snapshot)
			filtered := snapshot.Graph.Nodes[:0]
			for _, node := range snapshot.Graph.Nodes {
				if node.Step != step {
					filtered = append(filtered, node)
				}
			}
			snapshot.Graph.Nodes = filtered
			updated, _ := json.Marshal(snapshot)
			_, _ = db.Exec(`UPDATE media_ingest_run SET config_snapshot_json=? WHERE id=?`, string(updated), run.ID)
			_, _ = db.Exec(`DELETE FROM post_ingest_task WHERE ingest_run_id=? AND task_type='subtitle'`, run.ID)
			err := ValidateAggregateCurrentPolicy(context.Background(), db, adapters)
			if err == nil || !strings.Contains(err.Error(), "effective processing options differ from graph") {
				t.Fatalf("err=%v", err)
			}
			var repaired int
			_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE ingest_run_id=? AND task_type='subtitle'`, run.ID).Scan(&repaired)
			if repaired != 0 {
				t.Fatalf("repaired=%d", repaired)
			}
		})
	}
}

func TestStartupRejectsMalformedV2EdgeBeforeQueueRepair(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	var raw string
	_ = db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw)
	var snapshot ConfigSnapshot
	_ = json.Unmarshal([]byte(raw), &snapshot)
	snapshot.PolicyVersion = PolicyV2
	rawV2, _ := json.Marshal(snapshot)
	_, _ = db.Exec(`UPDATE media_ingest_run SET policy_version=2,config_snapshot_json=? WHERE id=?`, string(rawV2), run.ID)
	var scrape, visible int64
	_ = db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='scrape'`, run.ID).Scan(&scrape)
	_ = db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='media_visible'`, run.ID).Scan(&visible)
	_, _ = db.Exec(`DELETE FROM media_ingest_step_dependency WHERE step_id=?`, scrape)
	_, _ = db.Exec(`INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(?,?,'success')`, scrape, scrape)
	_, _ = db.Exec(`DELETE FROM post_ingest_task WHERE ingest_run_id=? AND task_type='poster'`, run.ID)
	err := ValidateAggregateCurrentPolicy(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "legacy policy v2 graph") || !strings.Contains(err.Error(), "self-edge") {
		t.Fatalf("err=%v", err)
	}
	var queues int
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE ingest_run_id=? AND task_type='poster'`, run.ID).Scan(&queues)
	if queues != 0 {
		t.Fatalf("queue repaired before rejection: %d", queues)
	}
}

func TestStartupValidatesAllRunsBeforeRepairingAny(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid1, scan1 := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run1 := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid1, ScanTaskID: scan1, FileType: "video"})
	_, _ = db.Exec(`DELETE FROM post_ingest_task WHERE ingest_run_id=? AND task_type='poster'`, run1.ID)
	_, _ = db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, mid1)
	_, mid2, scan2 := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run2 := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid2, ScanTaskID: scan2, FileType: "video"})
	_, _ = db.Exec(`UPDATE media_ingest_step SET required=1 WHERE run_id=? AND step_type='scrape'`, run2.ID)
	err := ValidateAggregateCurrentPolicy(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "persisted policy v3 graph differs") {
		t.Fatalf("err=%v", err)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE ingest_run_id=? AND task_type='poster'`, run1.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("first run mutated: %d", count)
	}
	var visibleStatus string
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE run_id=? AND step_type='media_visible'`, run1.ID).Scan(&visibleStatus)
	if visibleStatus != "waiting" {
		t.Fatalf("visibility repaired before all preflight validation: %s", visibleStatus)
	}
}

func TestStartupFatalQueueSemanticsLeavesEarlierRepairableRunUntouched(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid1, scan1 := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run1 := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid1, ScanTaskID: scan1, FileType: "video"})
	_, _ = db.Exec(`DELETE FROM post_ingest_task WHERE ingest_run_id=? AND task_type='poster'`, run1.ID)
	_, mid2, scan2 := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run2 := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid2, ScanTaskID: scan2, FileType: "video"})
	_, _ = db.Exec(`UPDATE post_ingest_task SET task_type='thumbnail' WHERE ingest_run_id=? AND task_type='poster'`, run2.ID)
	err := ValidateAggregateCurrentPolicy(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "queue semantics") {
		t.Fatalf("err=%v", err)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE ingest_run_id=? AND task_type='poster'`, run1.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("run1 mutated=%d", count)
	}
}

func TestStartupFatalEvidenceSemanticsLeavesEarlierDesyncUntouched(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid1, scan1 := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run1 := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid1, ScanTaskID: scan1, FileType: "video"})
	_, _ = db.Exec(`UPDATE post_ingest_task SET status='done' WHERE ingest_run_id=? AND task_type='poster'`, run1.ID)
	_, mid2, scan2 := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run2 := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid2, ScanTaskID: scan2, FileType: "video"})
	var step int64
	_ = db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='poster'`, run2.ID).Scan(&step)
	_, _ = db.Exec(`UPDATE media_ingest_step SET status='done' WHERE id=?`, step)
	_, errInsert := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('bad-evidence',?,?,?, ?,'owner','fp','poster','committed','/tmp/bad','{}')`, mid2, run2.ID, step, run2.Generation)
	if errInsert == nil {
		_, errInsert = db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,verified_at,stage_id) VALUES(?,?,?,? ,'thumbnail','fp','{}',CURRENT_TIMESTAMP,'bad-evidence')`, run2.ID, step, mid2, run2.Generation)
	}
	if errInsert != nil {
		t.Fatal(errInsert)
	}
	err := ValidateAggregateCurrentPolicy(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "evidence semantics") {
		t.Fatalf("err=%v", err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE run_id=? AND step_type='poster'`, run1.ID).Scan(&status)
	if status == "done" {
		t.Fatal("run1 desync was mutated")
	}
}

func TestReconcileStartupFinalizesDependencyPlanCompletionAndBarrier(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	stmts := []string{
		`UPDATE media_ingest_step SET status='done',finished_at=CURRENT_TIMESTAMP WHERE run_id=? AND required=1`,
		`UPDATE post_ingest_task SET status='done',finished_at=CURRENT_TIMESTAMP WHERE ingest_run_id=? AND ingest_step_id IN (SELECT id FROM media_ingest_step WHERE run_id=? AND required=1)`,
		`UPDATE scrape_task SET status='done',progress=100,finished_at=CURRENT_TIMESTAMP WHERE ingest_run_id=?`,
		`UPDATE media_ingest_step SET status='done',finished_at=CURRENT_TIMESTAMP WHERE run_id=? AND required=0`,
		`UPDATE media_ingest_run SET status='published',finished_at=CURRENT_TIMESTAMP WHERE id=?`,
		`UPDATE media SET publication_state='published',published_at=COALESCE(published_at,CURRENT_TIMESTAMP) WHERE id=?`,
	}
	args := [][]any{
		{run.ID},
		{run.ID, run.ID},
		{run.ID},
		{run.ID},
		{run.ID},
		{mid},
	}
	for i, stmt := range stmts {
		if _, err := db.Exec(stmt, args[i]...); err != nil {
			t.Fatalf("seed stmt %d: %v", i, err)
		}
	}
	seen := 0
	SetRetirementBarrierProbeForTest(func(id int64) {
		if id == run.ID {
			seen++
		}
	})
	t.Cleanup(ClearRetirementBarrierProbeForTest)
	if err := ValidateAggregateCurrentPolicy(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("retirement barrier calls=%d", seen)
	}
	var all, waiting int
	var mediaPub string
	if err := db.QueryRow(`SELECT all_terminal,waiting_count FROM media_plan_completion WHERE run_id=?`, run.ID).Scan(&all, &waiting); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mid).Scan(&mediaPub); err != nil {
		t.Fatal(err)
	}
	if all != 1 || waiting != 0 || mediaPub != "published" {
		t.Fatalf("all=%d waiting=%d media=%s", all, waiting, mediaPub)
	}
	if err := ValidateAggregateCurrentPolicy(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Fatalf("idempotent barrier calls=%d", seen)
	}
}
func TestStartupReconcilesVisibleBarrierAndUnblocksOptionalClaims(t *testing.T) {
	for _, state := range []string{"published", "degraded"} {
		t.Run(state, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 1, 0, 0)
			run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			stmts := []string{
				`UPDATE media_ingest_step SET status='done',finished_at=CURRENT_TIMESTAMP WHERE run_id=?`,
				`UPDATE post_ingest_task SET status='done',finished_at=CURRENT_TIMESTAMP WHERE ingest_run_id=?`,
				`UPDATE scrape_task SET status='done',finished_at=CURRENT_TIMESTAMP WHERE ingest_run_id=?`,
				`UPDATE media_ingest_step SET status='waiting',finished_at=NULL WHERE run_id=? AND step_type IN ('media_visible','preview','scrape')`,
				`UPDATE post_ingest_task SET status='waiting',finished_at=NULL WHERE ingest_run_id=? AND task_type='preview'`,
				`UPDATE scrape_task SET status='waiting',finished_at=NULL WHERE ingest_run_id=?`,
				`UPDATE media_ingest_run SET status=?,finished_at=CURRENT_TIMESTAMP WHERE id=?`,
				`UPDATE media SET publication_state=?,published_at=CURRENT_TIMESTAMP WHERE id=?`,
			}
			args := [][]any{{run.ID}, {run.ID}, {run.ID}, {run.ID}, {run.ID}, {run.ID}, {state, run.ID}, {state, mid}}
			for i, stmt := range stmts {
				if _, err := db.Exec(stmt, args[i]...); err != nil {
					t.Fatalf("seed stmt %d: %v", i, err)
				}
			}

			if err := ValidateAggregateCurrentPolicy(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			var visibleStatus string
			var finished, updated sql.NullTime
			if err := db.QueryRow(`SELECT status,finished_at,updated_at FROM media_ingest_step WHERE run_id=? AND step_type='media_visible'`, run.ID).Scan(&visibleStatus, &finished, &updated); err != nil {
				t.Fatal(err)
			}
			if visibleStatus != "done" || !finished.Valid || !updated.Valid {
				t.Fatalf("visible status=%s finished=%v updated=%v", visibleStatus, finished.Valid, updated.Valid)
			}
			registry := NewCapabilityMatrix([]string{"preview", "scrape"})
			preview, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "preview", Owner: "preview-worker", Registry: registry})
			if err != nil || preview == nil {
				t.Fatalf("preview claim=%+v err=%v", preview, err)
			}
			scrape, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueueScrape, TaskType: "scrape", Owner: "scrape-worker", Registry: registry})
			if err != nil || scrape == nil {
				t.Fatalf("scrape claim=%+v err=%v", scrape, err)
			}

			count, err := ReconcileVisibleMediaSteps(context.Background(), db)
			if err != nil || count != 0 {
				t.Fatalf("idempotent reconcile count=%d err=%v", count, err)
			}
		})
	}
}

func TestReconcileVisibleBarrierExcludesFailedAndCancelledRuns(t *testing.T) {
	for _, runStatus := range []string{"failed", "cancelled"} {
		t.Run(runStatus, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", 1, 0, 0)
			run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			if _, err := db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, mid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media_ingest_run SET status=? WHERE id=?`, runStatus, run.ID); err != nil {
				t.Fatal(err)
			}
			count, err := ReconcileVisibleMediaSteps(context.Background(), db)
			if err != nil || count != 0 {
				t.Fatalf("reconcile count=%d err=%v", count, err)
			}
			var status string
			if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE run_id=? AND step_type='media_visible'`, run.ID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "waiting" {
				t.Fatalf("media_visible status=%s", status)
			}
			claim, err := ClaimEligible(context.Background(), db, ClaimRequest{Family: QueuePostIngest, TaskType: "preview", Owner: "worker", Registry: NewCapabilityMatrix([]string{"preview"})})
			if err != nil || claim != nil {
				t.Fatalf("claim=%+v err=%v", claim, err)
			}
		})
	}
}

func TestReconcileCompletedPostIngestDomainWorkAdoptsStrongEvidenceIdempotently(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 1, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	if _, err := db.Exec(`UPDATE preview_task SET status='ready',sprite_path='sprite.jpg',vtt_path='preview.vtt' WHERE media_id=?; UPDATE subtitle_task SET status='done',finished_at=CURRENT_TIMESTAMP WHERE media_id=?`, mid, mid); err != nil {
		t.Fatal(err)
	}

	n, err := ReconcileCompletedPostIngestDomainWork(context.Background(), db)
	if err != nil || n != 2 {
		t.Fatalf("adopted=%d err=%v", n, err)
	}
	for _, step := range []StepType{StepPreview, StepSubtitleExtract} {
		var stepStatus, queueStatus string
		var stepLease, queueLease sql.NullString
		var stepFinished, queueFinished sql.NullString
		if err := db.QueryRow(`SELECT s.status,q.status,s.lease_owner,q.lease_owner,s.finished_at,q.finished_at FROM media_ingest_step s JOIN post_ingest_task q ON q.ingest_step_id=s.id WHERE s.run_id=? AND s.step_type=?`, run.ID, step).Scan(&stepStatus, &queueStatus, &stepLease, &queueLease, &stepFinished, &queueFinished); err != nil {
			t.Fatal(err)
		}
		if stepStatus != "done" || queueStatus != "done" || stepLease.Valid || queueLease.Valid || !stepFinished.Valid || !queueFinished.Valid {
			t.Fatalf("%s step=%s queue=%s stepLease=%v queueLease=%v finished=%v/%v", step, stepStatus, queueStatus, stepLease, queueLease, stepFinished, queueFinished)
		}
	}
	if n, err = ReconcileCompletedPostIngestDomainWork(context.Background(), db); err != nil || n != 0 {
		t.Fatalf("second adopted=%d err=%v", n, err)
	}
}

func TestReconcileCompletedPostIngestDomainWorkRejectsWeakOrStaleEvidence(t *testing.T) {
	cases := []struct {
		name     string
		preview  int
		subtitle bool
		evidence string
		mutate   string
	}{
		{name: "preview ready missing artifacts", preview: 1, evidence: `UPDATE preview_task SET status='ready',sprite_path=NULL,vtt_path=NULL WHERE media_id=?`},
		{name: "preview pending with artifacts", preview: 1, evidence: `UPDATE preview_task SET status='waiting',sprite_path='sprite.jpg',vtt_path=NULL WHERE media_id=?`},
		{name: "subtitle pending", subtitle: true, evidence: `UPDATE subtitle_task SET status='pending' WHERE media_id=?`},
		{name: "subtitle ready missing artifact", subtitle: true, evidence: `INSERT INTO media_subtitle(media_id,dedupe_key,source_kind,vtt_path,status) VALUES(?,'en','embedded','','ready')`},
		{name: "noncurrent generation", preview: 1, evidence: `UPDATE preview_task SET status='ready',sprite_path='sprite.jpg',vtt_path=NULL WHERE media_id=?`, mutate: `UPDATE media SET ingest_generation=ingest_generation+1 WHERE id=?`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mid, scan := seedPlannerMedia(t, db, "video", tc.preview, 0, 0)
			run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: tc.subtitle}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
			if _, err := db.Exec(tc.evidence, mid); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != "" {
				if _, err := db.Exec(tc.mutate, mid); err != nil {
					t.Fatal(err)
				}
			}
			n, err := ReconcileCompletedPostIngestDomainWork(context.Background(), db)
			if err != nil || n != 0 {
				t.Fatalf("adopted=%d err=%v", n, err)
			}
			step := StepPreview
			if tc.subtitle {
				step = StepSubtitleExtract
			}
			var stepStatus, queueStatus string
			if err := db.QueryRow(`SELECT s.status,q.status FROM media_ingest_step s JOIN post_ingest_task q ON q.ingest_step_id=s.id WHERE s.run_id=? AND s.step_type=?`, run.ID, step).Scan(&stepStatus, &queueStatus); err != nil {
				t.Fatal(err)
			}
			if stepStatus != "waiting" || queueStatus != "waiting" {
				t.Fatalf("step=%s queue=%s", stepStatus, queueStatus)
			}
		})
	}
}

func TestValidateAggregateCurrentPolicyConvergesExistingFailedRun(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 1, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if _, err := db.Exec(`UPDATE media_ingest_run SET status='failed',finished_at=CURRENT_TIMESTAMP,error_message='poster: context deadline exceeded' WHERE id=?;
UPDATE media SET publication_state='failed',publication_error='poster: context deadline exceeded' WHERE id=?;
UPDATE media_ingest_step SET status=CASE WHEN step_type='poster' THEN 'failed' ELSE 'waiting' END,last_error=CASE WHEN step_type='poster' THEN 'context deadline exceeded' ELSE '' END,finished_at=CASE WHEN step_type='poster' THEN CURRENT_TIMESTAMP ELSE NULL END WHERE run_id=?;
UPDATE post_ingest_task SET status=CASE WHEN task_type='poster' THEN 'failed' ELSE 'waiting' END,last_error=CASE WHEN task_type='poster' THEN 'context deadline exceeded' ELSE '' END,finished_at=CASE WHEN task_type='poster' THEN CURRENT_TIMESTAMP ELSE NULL END WHERE ingest_run_id=?;
UPDATE scrape_task SET status='waiting',progress=0,message='',finished_at=NULL WHERE ingest_run_id=?`, run.ID, mediaID, run.ID, run.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAggregateCurrentPolicy(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	assertTerminalRunConverged(t, db, run.ID)
	var eligible int
	if err := db.QueryRow(`SELECT
 (SELECT COUNT(*) FROM post_ingest_task q JOIN media_ingest_run r ON r.id=q.ingest_run_id WHERE q.ingest_run_id=? AND q.status='waiting' AND r.status='processing')+
 (SELECT COUNT(*) FROM scrape_task q JOIN media_ingest_run r ON r.id=q.ingest_run_id WHERE q.ingest_run_id=? AND q.status='waiting' AND r.status='processing')`, run.ID, run.ID).Scan(&eligible); err != nil {
		t.Fatal(err)
	}
	if eligible != 0 {
		t.Fatalf("eligible terminal-run tasks=%d", eligible)
	}
}
