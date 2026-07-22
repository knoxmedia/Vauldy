package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"knox-media/internal/coreiface"
	"knox-media/internal/store"
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
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true, PreparePlanner: &recordingPreparePlanner{}, Capabilities: NewCapabilityMatrix([]string{"prepare"})}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	wantSteps := []StepType{StepPoster, StepEncrypt, StepScrape, StepPreview, StepSubtitle, StepPrepare}
	if run.ID == 0 || run.MediaID != mediaID || run.Generation != 1 || !reflect.DeepEqual(run.Steps, wantSteps) {
		t.Fatalf("run=%+v want steps=%v", run, wantSteps)
	}
	var raw string
	if err := db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
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
	want := []StepType{StepPoster, StepScrape}
	if !reflect.DeepEqual(run.Steps, want) {
		t.Fatalf("steps=%v want %v", run.Steps, want)
	}
}

func TestPlannerCommunityBuildOmitsPrepare(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 1)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{PrepareAvailable: false}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	for _, step := range run.Steps {
		if step == StepPrepare {
			t.Fatal("community build planned prepare")
		}
	}
}

func TestPlannerQueueRowsLinkExactStepsAndGeneration(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 1, 1, 1)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true, PreparePlanner: &recordingPreparePlanner{}, Capabilities: NewCapabilityMatrix([]string{"prepare"})}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
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
	want := []StepType{StepPoster, StepEncrypt, StepPreview, StepSubtitle}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queued=%v want %v", got, want)
	}
}

func TestPlannerNonVideoLeavesPublishedWithoutPlan(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "image", 1, 1, 1)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true, PreparePlanner: &recordingPreparePlanner{}, Capabilities: NewCapabilityMatrix([]string{"prepare"})}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "image"})
	if run.ID == 0 {
		t.Fatalf("photo run=%+v", run)
	}
	var state string
	var generation, runs, steps, queued int
	if err := db.QueryRow(`SELECT publication_state,ingest_generation FROM media WHERE id=?`, mediaID).Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=?`, mediaID).Scan(&steps)
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=?`, mediaID).Scan(&queued)
	if state != string(StateProcessing) || generation != 1 || runs == 0 || steps == 0 || queued == 0 {
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
	if _, err := NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true, PreparePlanner: &recordingPreparePlanner{}, Capabilities: NewCapabilityMatrix([]string{"prepare"})}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"}); err != nil {
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

type recordingPreparePlanner struct {
	calls                              int
	runID, stepID, mediaID, generation int64
	err                                error
}

func (p *recordingPreparePlanner) PlanIngestPrepareTx(_ context.Context, _ store.SQLExecutor, mediaID, runID, stepID, generation int64) error {
	p.calls++
	p.mediaID, p.runID, p.stepID, p.generation = mediaID, runID, stepID, generation
	return p.err
}

var _ coreiface.IngestPreparePlanner = (*recordingPreparePlanner)(nil)

func TestPlannerRequiresPrepareWhenEnterpriseCapabilityAndLibraryFlagEnabled(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 1)
	capability := &recordingPreparePlanner{}
	run := planAndCommit(t, db, NewPlanner(PlanOptions{PreparePlanner: capability, Capabilities: NewCapabilityMatrix([]string{"prepare"})}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if len(run.Steps) == 0 || run.Steps[len(run.Steps)-1] != StepPrepare {
		t.Fatalf("steps=%v, want final required prepare", run.Steps)
	}
	if capability.calls != 1 || capability.mediaID != mediaID || capability.runID != run.ID || capability.generation != run.Generation || capability.stepID == 0 {
		t.Fatalf("prepare callback=%+v run=%+v", capability, run)
	}
	var stepType, status string
	if err := db.QueryRow(`SELECT step_type,status FROM media_ingest_step WHERE id=? AND run_id=? AND media_id=? AND generation=? AND required=0`, capability.stepID, run.ID, mediaID, run.Generation).Scan(&stepType, &status); err != nil {
		t.Fatal(err)
	}
	if stepType != string(StepPrepare) || status != "waiting" {
		t.Fatalf("linked step=%s/%s", stepType, status)
	}
}

func TestPlannerPrepareCallbackFailureRollsBackEntirePlan(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 1)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	capability := &recordingPreparePlanner{err: errors.New("preset unavailable")}
	if _, err = NewPlanner(PlanOptions{PreparePlanner: capability, Capabilities: NewCapabilityMatrix([]string{"prepare"})}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"}); err == nil {
		t.Fatal("expected callback failure")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var generation, runs, steps int
	if err = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=?`, mediaID).Scan(&steps)
	if generation != 0 || runs != 0 || steps != 0 {
		t.Fatalf("rollback leaked generation=%d runs=%d steps=%d", generation, runs, steps)
	}
}

func TestPlannerLibraryFlagWithoutCapabilityOmitsPrepare(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 1)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{PrepareAvailable: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	for _, step := range run.Steps {
		if step == StepPrepare {
			t.Fatal("bool without capability planned prepare")
		}
	}
}

func TestPlannerV2VideoMatrix(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 1, 1, 1)
	p := NewPlanner(PlanOptions{SubtitleAuto: true, EncryptGlobal: true, PreparePlanner: &recordingPreparePlanner{}, Capabilities: NewCapabilityMatrix([]string{"prepare"})})
	run := planAndCommit(t, db, p, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	wantRequired := []StepType{StepPoster, StepEncrypt}
	wantOptional := []StepType{StepScrape, StepPreview, StepSubtitle, StepPrepare}
	var raw string
	if err := db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.PolicyVersion != PolicyV2 || !reflect.DeepEqual(snapshot.RequiredSteps, wantRequired) || !reflect.DeepEqual(snapshot.OptionalSteps, wantOptional) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if !reflect.DeepEqual(run.Steps, []StepType{StepPoster, StepEncrypt, StepScrape, StepPreview, StepSubtitle, StepPrepare}) {
		t.Fatalf("steps=%v", run.Steps)
	}
}

func TestPlannerV2PhotoMatrix(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "image", 0, 1, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{EncryptGlobal: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "image"})
	if !reflect.DeepEqual(run.Steps, []StepType{StepThumbnail, StepEncrypt, StepScrape}) {
		t.Fatalf("steps=%v", run.Steps)
	}
	var raw string
	if err := db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.PolicyVersion != PolicyV2 || !reflect.DeepEqual(snapshot.RequiredSteps, []StepType{StepThumbnail, StepEncrypt}) || !reflect.DeepEqual(snapshot.OptionalSteps, []StepType{StepScrape}) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestPlannerV2PersistsDependenciesBeforeCommit(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 1, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{EncryptGlobal: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	var kind string
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id WHERE s.run_id=? AND d.dependency_kind='step_done'`, run.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("step dependencies=%d", count)
	}
	if err := db.QueryRow(`SELECT d.dependency_kind FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id WHERE s.run_id=? AND s.step_type='encrypt'`, run.ID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "step_done" {
		t.Fatalf("kind=%s", kind)
	}
}

func TestPlannerV2OmitsScanKeyframeAndAtrack(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{ATrackAuto: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	for _, step := range run.Steps {
		if step == StepKeyframe || step == StepAtrack {
			t.Fatalf("scan step=%s", step)
		}
	}
}

func TestPlannerV2CarriesMetadataPartialDiagnostics(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	attempt := MetadataAttempt{Attempted: true, Fields: []string{"title"}, Errors: []MetadataDiagnostic{{Source: "probe", Message: "duration unavailable"}}}
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video", MetadataAttempt: attempt})
	var raw string
	if err := db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Metadata, attempt) {
		t.Fatalf("metadata=%+v", snapshot.Metadata)
	}
}

func TestPlannerRejectsInvalidDependencyGraphAtomically(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 1, 0)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewPlanner(PlanOptions{EncryptGlobal: true}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	var ids []int64
	rows, err := tx.Query(`SELECT id FROM media_ingest_step ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if len(ids) < 2 {
		t.Fatal("expected two steps")
	}
	if err = validateDependencyTx(context.Background(), tx, ids[0], ids[0], mediaID, 1, 1); err == nil {
		t.Fatal("expected self-edge rejection")
	}
	_ = tx.Rollback()
	var runs, deps int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step_dependency`).Scan(&deps); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || deps != 0 {
		t.Fatalf("partial rows runs=%d deps=%d", runs, deps)
	}
}
