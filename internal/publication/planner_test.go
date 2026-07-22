package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"knox-media/internal/store"
	"reflect"
	"testing"
)

func openPlannerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedPlannerMedia(t *testing.T, db *sql.DB, fileType string, preview, encrypted, prepare int) (int64, int64, int64) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO library(name,type,path,preview_extract,encrypted_assets_enabled,jit_prepare_on_ingest) VALUES('planner','video','/planner',?,?,?)`, preview, encrypted, prepare)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_type) VALUES(?,?,?)`, libraryID, fmt.Sprintf("planner-%d-%s", libraryID, fileType), fileType)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	mediaID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'done','manual')`, libraryID)
	if err != nil {
		t.Fatalf("insert scan task: %v", err)
	}
	scanID, _ := res.LastInsertId()
	return libraryID, mediaID, scanID
}

func planAndCommit(t *testing.T, db *sql.DB, p *Planner, media NewMedia) Run {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.PlanNewMediaTx(context.Background(), tx, media)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("plan: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return run
}

func TestPlannerVideoSnapshotsRequiredSteps(t *testing.T) {
	db := openPlannerTestDB(t)
	libraryID, mediaID, scanID := seedPlannerMedia(t, db, "video", 1, 1, 1)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	wantSteps := []StepType{StepPoster, StepScrape, StepPreview, StepKeyframe, StepSubtitle, StepAtrack, StepEncrypt}
	if run.ID == 0 || run.MediaID != mediaID || run.Generation != 1 || !reflect.DeepEqual(run.Steps, wantSteps) {
		t.Fatalf("run=%+v want steps=%v", run, wantSteps)
	}
	var raw string
	if err := db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	wantJSON := fmt.Sprintf(`{"library_id":%d,"file_type":"video","preview":true,"subtitle":true,"atrack":true,"encrypt":true,"steps":["poster","scrape","preview","keyframe","subtitle","atrack","encrypt"]}`, libraryID)
	if raw != wantJSON {
		t.Fatalf("snapshot=%s\nwant    =%s", raw, wantJSON)
	}
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.LibraryID != libraryID || !reflect.DeepEqual(snapshot.Steps, wantSteps) {
		t.Fatalf("decoded snapshot=%+v", snapshot)
	}
	var state string
	var generation int64
	if err := db.QueryRow(`SELECT publication_state,ingest_generation FROM media WHERE id=?`, mediaID).Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	if state != string(StateProcessing) || generation != 1 {
		t.Fatalf("media state=%s generation=%d", state, generation)
	}
}

func TestPlannerDisabledFeaturesAreOmitted(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 1, 1)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	want := []StepType{StepPoster, StepScrape, StepKeyframe}
	if !reflect.DeepEqual(run.Steps, want) {
		t.Fatalf("steps=%v want %v", run.Steps, want)
	}
}

func TestPlannerCommunityBuildOmitsPrepare(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 1)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if !reflect.DeepEqual(run.Steps, []StepType{StepPoster, StepScrape, StepKeyframe}) {
		t.Fatalf("community steps=%v", run.Steps)
	}
}

func TestPlannerQueueRowsLinkExactStepsAndGeneration(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 1, 1, 1)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	rows, err := db.Query(`SELECT q.task_type,s.step_type,q.ingest_run_id,q.ingest_step_id,s.id,q.generation FROM post_ingest_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE q.media_id=? ORDER BY q.id`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []StepType
	for rows.Next() {
		var taskType, stepType StepType
		var runID, linkedStepID, stepID, generation int64
		if err := rows.Scan(&taskType, &stepType, &runID, &linkedStepID, &stepID, &generation); err != nil {
			t.Fatal(err)
		}
		if taskType != stepType || runID != run.ID || linkedStepID != stepID || generation != run.Generation {
			t.Fatalf("bad queue link task=%s step=%s run=%d/%d stepID=%d/%d gen=%d", taskType, stepType, runID, run.ID, linkedStepID, stepID, generation)
		}
		got = append(got, taskType)
	}
	want := []StepType{StepPoster, StepPreview, StepKeyframe, StepSubtitle, StepAtrack, StepEncrypt}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queued=%v want %v", got, want)
	}
}

func TestPlannerNonVideoLeavesPublishedWithoutPlan(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "image", 1, 1, 1)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "image"})
	if run.ID != 0 {
		t.Fatalf("non-video run=%+v", run)
	}
	var state string
	var generation, runs, steps, queued int
	if err := db.QueryRow(`SELECT publication_state,ingest_generation FROM media WHERE id=?`, mediaID).Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=?`, mediaID).Scan(&steps)
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=?`, mediaID).Scan(&queued)
	if state != string(StatePublished) || generation != 0 || runs+steps+queued != 0 {
		t.Fatalf("state=%s gen=%d counts=%d/%d/%d", state, generation, runs, steps, queued)
	}
}

func TestPlannerValidatesInputsAndFileTypeHint(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	p := NewPlanner(PlanOptions{})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tx, _ := db.Begin()
	defer tx.Rollback()
	cases := []struct {
		name  string
		ctx   context.Context
		tx    *sql.Tx
		media NewMedia
	}{
		{"cancelled context", cancelled, tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"}},
		{"nil transaction", context.Background(), nil, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"}},
		{"invalid media", context.Background(), tx, NewMedia{}},
		{"mismatched hint", context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "image"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.PlanNewMediaTx(tc.ctx, tc.tx, tc.media)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.name == "cancelled context" && !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPlannerRepeatedPlansUseUniqueGenerations(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	p := NewPlanner(PlanOptions{})
	first := planAndCommit(t, db, p, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	second := planAndCommit(t, db, p, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if first.Generation != 1 || second.Generation != 2 {
		t.Fatalf("generations=%d,%d", first.Generation, second.Generation)
	}
}

func TestPlannerCallerRollbackRemovesEntirePlan(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 1, 1, 1)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"}); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var state string
	var generation, runs, steps, queued int
	if err := db.QueryRow(`SELECT publication_state,ingest_generation FROM media WHERE id=?`, mediaID).Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=?`, mediaID).Scan(&steps)
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=?`, mediaID).Scan(&queued)
	if state != string(StatePublished) || generation != 0 || runs+steps+queued != 0 {
		t.Fatalf("rollback left state=%s generation=%d rows=%d/%d/%d", state, generation, runs, steps, queued)
	}
}
