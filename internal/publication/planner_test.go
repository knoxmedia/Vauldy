package publication

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"knox-media/internal/coreiface"
	"knox-media/internal/libraryprocessing"
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
	wantSteps := []StepType{StepPoster, StepEncrypt, StepMediaVisible, StepScrape, StepPreview, StepSubtitleExtract, StepAtrackExtract, StepPrepare}
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
	want := []StepType{StepPoster, StepMediaVisible, StepScrape}
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
		if executionTaskType(stepType) != taskType || runID != run.ID || linkedStepID != stepID || generation != run.Generation {
			t.Fatalf("bad queue link task=%s step=%s run=%d/%d stepID=%d/%d gen=%d", taskType, stepType, runID, run.ID, linkedStepID, stepID, generation)
		}
		got = append(got, taskType)
	}
	want := []StepType{StepPoster, StepEncrypt, StepPreview, StepSubtitle, StepAtrack}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queued=%v want %v", got, want)
	}
}

func TestPlannerCreatesMissingDomainRowsForAlignedQueueSteps(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 1, 0, 0)

	planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})

	for table, want := range map[string]string{
		"preview_task":  "waiting",
		"subtitle_task": "pending",
	} {
		var got string
		if err := db.QueryRow(`SELECT status FROM `+table+` WHERE media_id=?`, mediaID).Scan(&got); err != nil {
			t.Fatalf("%s row: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s status=%q want %q", table, got, want)
		}
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
	wantOptional := []StepType{StepMediaVisible, StepScrape, StepPreview, StepSubtitleExtract, StepPrepare}
	var raw string
	if err := db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.PolicyVersion != CurrentPolicyVersion || !reflect.DeepEqual(snapshot.RequiredSteps, wantRequired) || !reflect.DeepEqual(snapshot.OptionalSteps, wantOptional) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if !reflect.DeepEqual(run.Steps, []StepType{StepPoster, StepEncrypt, StepMediaVisible, StepScrape, StepPreview, StepSubtitleExtract, StepPrepare}) {
		t.Fatalf("steps=%v", run.Steps)
	}
}

func TestPlannerV2PhotoMatrix(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "image", 0, 1, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{EncryptGlobal: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "image"})
	if !reflect.DeepEqual(run.Steps, []StepType{StepThumbnail, StepEncrypt, StepMediaVisible, StepScrape}) {
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
	if snapshot.PolicyVersion != CurrentPolicyVersion || !reflect.DeepEqual(snapshot.RequiredSteps, []StepType{StepThumbnail, StepEncrypt}) || !reflect.DeepEqual(snapshot.OptionalSteps, []StepType{StepMediaVisible, StepScrape}) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestPlannerV2PersistsDependenciesBeforeCommit(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 1, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{EncryptGlobal: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	var kind string
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id WHERE s.run_id=? AND d.dependency_kind='success'`, run.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("step dependencies=%d", count)
	}
	if err := db.QueryRow(`SELECT d.dependency_kind FROM media_ingest_step_dependency d JOIN media_ingest_step s ON s.id=d.step_id WHERE s.run_id=? AND s.step_type='encrypt'`, run.ID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "success" {
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
	run, err := NewPlanner(PlanOptions{EncryptGlobal: true}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	rows, err := tx.Query(`SELECT id FROM media_ingest_step WHERE run_id=? ORDER BY id`, run.ID)
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
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(ids) < 2 {
		t.Fatal("expected two steps")
	}
	bad := []struct {
		name string
		deps []Dependency
		ids  map[StepType]int64
	}{
		{"self", []Dependency{{Step: StepPoster, Kind: DependencySuccess, DependsOn: stepPtr(StepPoster)}}, map[StepType]int64{StepPoster: ids[0]}},
		{"missing target", []Dependency{{Step: StepPoster, Kind: DependencySuccess, DependsOn: stepPtr(StepThumbnail)}}, map[StepType]int64{StepPoster: ids[0]}},
	}
	for _, tc := range bad {
		if err := insertDependenciesTx(context.Background(), tx, tc.deps, tc.ids, mediaID, run.Generation, run.ID); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
	_ = tx.Rollback()
	for _, q := range []string{`SELECT COUNT(*) FROM media_ingest_run`, `SELECT COUNT(*) FROM media_ingest_step`, `SELECT COUNT(*) FROM media_ingest_step_dependency`, `SELECT COUNT(*) FROM post_ingest_task`} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s=%d", q, n)
		}
	}
	var generation int
	if err := db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 0 {
		t.Fatalf("generation=%d", generation)
	}
}

func TestInsertDependenciesRejectsCrossIdentityAndCycle(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaA, scanA := seedPlannerMedia(t, db, "video", 0, 0, 0)
	_, mediaB, _ := seedPlannerMedia(t, db, "video", 0, 0, 0)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewPlanner(PlanOptions{}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaA, ScanTaskID: scanA, FileType: "video"})
	if err != nil {
		t.Fatal(err)
	}
	var poster, scrape int64
	if err := tx.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='poster'`, run.ID).Scan(&poster); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='scrape'`, run.ID).Scan(&scrape); err != nil {
		t.Fatal(err)
	}
	insertForeign := func(runID, mediaID, generation int64, typ string) int64 {
		res, err := tx.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,?,?,0,'waiting')`, runID, mediaID, generation, typ)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	res, err := tx.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(?,99,'repair','processing','{}',2)`, mediaB)
	if err != nil {
		t.Fatal(err)
	}
	foreignRun, _ := res.LastInsertId()
	crossRun := insertForeign(foreignRun, mediaB, 99, "poster")
	for _, tc := range []struct {
		name                     string
		target                   int64
		media, generation, runID int64
	}{
		{"cross-run", crossRun, mediaA, run.Generation, run.ID},
		{"cross-media", crossRun, mediaA, 99, foreignRun},
		{"cross-generation", crossRun, mediaB, run.Generation, foreignRun},
	} {
		deps := []Dependency{{Step: StepPoster, Kind: DependencySuccess, DependsOn: stepPtr(StepScrape)}}
		if err := insertDependenciesTx(context.Background(), tx, deps, map[StepType]int64{StepPoster: poster, StepScrape: tc.target}, tc.media, tc.generation, tc.runID); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
	if _, err := tx.Exec(`INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(?,?,'success')`, poster, scrape); err != nil {
		t.Fatal(err)
	}
	cycle := []Dependency{{Step: StepScrape, Kind: DependencySuccess, DependsOn: stepPtr(StepPoster)}}
	if err := insertDependenciesTx(context.Background(), tx, cycle, map[StepType]int64{StepPoster: poster, StepScrape: scrape}, mediaA, run.Generation, run.ID); err == nil {
		t.Fatal("expected cycle error")
	}
	batch := []Dependency{{Step: StepScrape, Kind: DependencySuccess}, {Step: StepPoster, Kind: DependencySuccess, DependsOn: stepPtr(StepPoster)}}
	if err := insertDependenciesTx(context.Background(), tx, batch, map[StepType]int64{StepPoster: poster, StepScrape: scrape}, mediaA, run.Generation, run.ID); err == nil {
		t.Fatal("expected second batch edge failure")
	}
	_ = tx.Rollback()
	for _, q := range []string{`SELECT COUNT(*) FROM media_ingest_run`, `SELECT COUNT(*) FROM media_ingest_step`, `SELECT COUNT(*) FROM media_ingest_step_dependency`, `SELECT COUNT(*) FROM post_ingest_task`} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("after rollback %s=%d", q, n)
		}
	}
	var generation int
	if err := db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaA).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 0 {
		t.Fatalf("generation=%d", generation)
	}
}

func planReplacementAndCommit(t *testing.T, db *sql.DB, p *Planner, mediaID int64, opts ReplacementOptions) ReplacementResult {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.PlanReplacementTx(context.Background(), tx, mediaID, opts)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("plan replacement: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

func loadPlannerSnapshot(t *testing.T, db *sql.DB, runID int64) ConfigSnapshot {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPlannerManualRetryUsesCurrentPolicy(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 1, 1)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if _, err := db.Exec(`UPDATE media SET publication_state='published' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE library SET preview_extract=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID); err != nil {
		t.Fatal(err)
	}
	prepare := &recordingPreparePlanner{}
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true, EncryptGlobal: true, PreparePlanner: prepare, Capabilities: NewCapabilityMatrix([]string{"prepare"})}), mediaID, ReplacementOptions{Reason: PlanReasonManualRetry, ExpectedGeneration: first.Generation})
	want := []StepType{StepPoster, StepEncrypt, StepMediaVisible, StepScrape, StepPreview, StepSubtitleExtract, StepAtrackExtract, StepPrepare}
	if result.OldGeneration != first.Generation || result.NewGeneration != first.Generation+1 || !reflect.DeepEqual(result.Run.Steps, want) {
		t.Fatalf("result=%+v want steps=%v", result, want)
	}
	var reason string
	var preserve int
	if err := db.QueryRow(`SELECT reason,preserve_visibility FROM media_ingest_run WHERE id=?`, result.Run.ID).Scan(&reason, &preserve); err != nil {
		t.Fatal(err)
	}
	if reason != string(PlanReasonManualRetry) || preserve != 0 {
		t.Fatalf("reason=%q preserve=%d", reason, preserve)
	}
	var state string
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(StateProcessing) {
		t.Fatalf("manual retry state=%q", state)
	}
	if prepare.calls != 1 || prepare.runID != result.Run.ID || prepare.generation != result.NewGeneration {
		t.Fatalf("prepare=%+v result=%+v", prepare, result)
	}
}

func TestReplacementLoadsDBFileTypeVideoAndPhoto(t *testing.T) {
	for _, tc := range []struct {
		fileType string
		want     []StepType
	}{{"video", []StepType{StepPoster, StepMediaVisible, StepScrape}}, {"image", []StepType{StepThumbnail, StepMediaVisible, StepScrape}}, {"audio", []StepType{StepPoster, StepMediaVisible, StepScrape}}, {"document", []StepType{StepPoster, StepMediaVisible, StepScrape}}} {
		t.Run(tc.fileType, func(t *testing.T) {
			db := openPlannerTestDB(t)
			_, mediaID, _ := seedPlannerMedia(t, db, tc.fileType, 0, 0, 0)
			result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: 0, PreserveVisibility: true})
			if !reflect.DeepEqual(result.Run.Steps, tc.want) {
				t.Fatalf("steps=%v want=%v", result.Run.Steps, tc.want)
			}
			snapshot := loadPlannerSnapshot(t, db, result.Run.ID)
			if snapshot.FileType != tc.fileType {
				t.Fatalf("snapshot file type=%q", snapshot.FileType)
			}
		})
	}
}

func TestReplacementOmitsOldKeyframeAtrack(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	for _, step := range []StepType{StepKeyframe, StepAtrack} {
		if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,?,?,1,'failed')`, first.ID, mediaID, first.Generation, step); err != nil {
			t.Fatal(err)
		}
	}
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{ATrackAuto: true}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: first.Generation, PreserveVisibility: true})
	for _, step := range result.Run.Steps {
		if step == StepKeyframe || step == StepAtrack {
			t.Fatalf("copied old step %q", step)
		}
	}
}

func TestReplacementReflectsChangedEncryptionPreviewCapabilities(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 1)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if _, err := db.Exec(`UPDATE library SET preview_extract=1, encrypted_assets_enabled=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID); err != nil {
		t.Fatal(err)
	}
	prepare := &recordingPreparePlanner{}
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{EncryptGlobal: true, PreparePlanner: prepare, Capabilities: NewCapabilityMatrix([]string{"prepare"})}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: first.Generation})
	want := []StepType{StepPoster, StepEncrypt, StepMediaVisible, StepScrape, StepPreview, StepPrepare}
	if !reflect.DeepEqual(result.Run.Steps, want) {
		t.Fatalf("steps=%v want=%v", result.Run.Steps, want)
	}
	snapshot := loadPlannerSnapshot(t, db, result.Run.ID)
	if !snapshot.Encrypt || !snapshot.PreviewExtract || !snapshot.Prepare {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestReplacementCASRollsBackConcurrentLoser(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	winner := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: first.Generation})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewPlanner(PlanOptions{}).PlanReplacementTx(context.Background(), tx, mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: first.Generation})
	if !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("err=%v want generation conflict", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var generation, runs int64
	if err := db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if generation != winner.NewGeneration || runs != 2 {
		t.Fatalf("generation=%d runs=%d winner=%+v", generation, runs, winner)
	}
}

func TestReplacementDoesNotCopyOldSteps(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if _, err := db.Exec(`UPDATE media SET publication_state='published' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='failed',last_error='old failure' WHERE run_id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: first.Generation, PreserveVisibility: true})
	var oldFailed, newWaiting, copiedErrors int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=? AND status='failed'`, first.ID).Scan(&oldFailed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=? AND status='waiting'`, result.Run.ID).Scan(&newWaiting); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=? AND last_error<>''`, result.Run.ID).Scan(&copiedErrors); err != nil {
		t.Fatal(err)
	}
	if oldFailed == 0 || newWaiting != len(result.Run.Steps) || copiedErrors != 0 {
		t.Fatalf("oldFailed=%d newWaiting=%d copiedErrors=%d", oldFailed, newWaiting, copiedErrors)
	}
	var state string
	if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(StatePublished) {
		t.Fatalf("preserved state=%q", state)
	}
}

func TestPlanReplacementTxRequiresSQLTransaction(t *testing.T) {
	var replacement func(context.Context, *sql.Tx, int64, ReplacementOptions) (ReplacementResult, error) = NewPlanner(PlanOptions{}).PlanReplacementTx
	if replacement == nil {
		t.Fatal("replacement planner method is nil")
	}
}

func TestPlanReplacementRejectsNilTransaction(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("PlanReplacementTx panicked: %v", recovered)
		}
	}()

	_, err := NewPlanner(PlanOptions{}).PlanReplacementTx(context.Background(), nil, 1, ReplacementOptions{Reason: PlanReasonRepair})
	if err == nil {
		t.Fatal("expected nil transaction error")
	}
	if got, want := err.Error(), "publication planner: nil transaction"; got != want {
		t.Fatalf("error=%q want %q", got, want)
	}
}

func TestPlannerEffectiveOptionsSnapshotAndAIAdmission(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	if _, err := db.Exec(`UPDATE library SET ai_analysis=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewPlanner(PlanOptions{}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("err=%v", err)
	}
	_ = tx.Rollback()
	var generation, runs, queue int
	_ = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=?`, mediaID).Scan(&queue)
	if generation+runs+queue != 0 {
		t.Fatalf("admission failure persisted generation=%d runs=%d queue=%d", generation, runs, queue)
	}

	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewPlanner(PlanOptions{Capabilities: NewCapabilityMatrix([]string{string(StepAIAnalysis)})}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("advertised AI err=%v", err)
	}
	_ = tx.Rollback()

}

func TestPlannerOldGenerationSnapshotAndTopologyRemainImmutable(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	var before string
	_ = db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, first.ID).Scan(&before)
	var beforeSteps int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=?`, first.ID).Scan(&beforeSteps)
	_, _ = db.Exec(`UPDATE library SET keyframe_extract=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID)
	second := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonRepair, ExpectedGeneration: first.Generation, PreserveVisibility: true})
	var after string
	_ = db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, first.ID).Scan(&after)
	var afterSteps int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=?`, first.ID).Scan(&afterSteps)
	if before != after || beforeSteps != afterSteps {
		t.Fatalf("old generation mutated snapshot=%v steps=%d/%d", before != after, beforeSteps, afterSteps)
	}
	if !containsStep(second.Run.Steps, StepKeyframeExtract) {
		t.Fatalf("new steps=%v", second.Run.Steps)
	}
}

func containsStep(steps []StepType, want StepType) bool {
	for _, step := range steps {
		if step == want {
			return true
		}
	}
	return false
}

func TestPlannerLegacyAutoDefaultsDoNotChangeExplicitProvenance(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{SubtitleAuto: true, ATrackAuto: true}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	snapshot := loadPlannerSnapshot(t, db, run.ID)
	if snapshot.ProcessingExplicit.SubtitleExtract || snapshot.ProcessingExplicit.ATrackExtract || len(snapshot.ProcessingProvenance.Explicit) != 0 {
		t.Fatalf("canonical explicit/provenance=%+v/%+v", snapshot.ProcessingExplicit, snapshot.ProcessingProvenance)
	}
	if !reflect.DeepEqual(snapshot.LegacyOptionDefaults, []string{"subtitle_extract", "atrack_extract"}) || !containsStep(run.Steps, StepSubtitleExtract) || !containsStep(run.Steps, StepAtrackExtract) {
		t.Fatalf("legacy defaults=%v steps=%v", snapshot.LegacyOptionDefaults, run.Steps)
	}
}

func TestConfigSnapshotJSONUsesDistinctProcessingKeys(t *testing.T) {
	snapshot := ConfigSnapshot{PolicyVersion: PolicyV3, ProcessingProvenance: libraryprocessing.Provenance{Explicit: []string{"preview"}}, LegacyOptionDefaults: []string{"subtitle_extract"}}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if _, ok := object["processing_provenance"]; !ok {
		t.Fatalf("missing processing_provenance: %s", raw)
	}
	if _, ok := object["legacy_option_defaults"]; !ok {
		t.Fatalf("missing legacy_option_defaults: %s", raw)
	}
	if _, duplicate := object["ProcessingProvenance"]; duplicate {
		t.Fatalf("wrong implicit key: %s", raw)
	}
	var decoded ConfigSnapshot
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.ProcessingProvenance, snapshot.ProcessingProvenance) || !reflect.DeepEqual(decoded.LegacyOptionDefaults, snapshot.LegacyOptionDefaults) {
		t.Fatalf("decoded=%+v", decoded)
	}
}

func TestPlannerRecognitionRequiresExecutableAdapter(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	_, _ = db.Exec(`UPDATE library SET subtitle_recognize=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID)
	tx, _ := db.BeginTx(context.Background(), nil)
	_, err := NewPlanner(PlanOptions{Capabilities: NewCapabilityMatrix([]string{string(StepSubtitleRecognize)})}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	_ = tx.Rollback()
	if !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("err=%v", err)
	}
	var generation, rows int
	_ = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&rows)
	if generation != 0 || rows != 0 {
		t.Fatalf("generation=%d rows=%d", generation, rows)
	}
}

type fakeExecutableAdapter StepType

func (a fakeExecutableAdapter) TaskType() StepType                 { return StepType(a) }
func (fakeExecutableAdapter) Execute(context.Context, int64) error { return nil }

type fakeExecutableRegistry map[StepType]ExecutableTaskAdapter

func (r fakeExecutableRegistry) Adapter(step StepType) (ExecutableTaskAdapter, bool) {
	a, ok := r[step]
	return a, ok
}

func TestPlannerTypedRecognitionAdapterAdmissionContract(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	_, _ = db.Exec(`UPDATE library SET subtitle_recognize=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mediaID)
	registry := fakeExecutableRegistry{StepSubtitleRecognize: fakeExecutableAdapter(StepSubtitleRecognize)}
	run := planAndCommit(t, db, NewPlanner(PlanOptions{ExecutableAdapters: registry}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	if !containsStep(run.Steps, StepSubtitleRecognize) {
		t.Fatalf("steps=%v", run.Steps)
	}
}

func TestPlannerCleanupEligibleWithDefaultSourceStrategies(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 1, 0, 0)
	_, _ = db.Exec(`UPDATE library SET cleanup_local_source_after_package=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mid)
	run := planAndCommit(t, db, NewPlanner(PlanOptions{EncryptedSourceStrategies: DefaultEncryptedSourceStrategies()}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	var raw string
	_ = db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw)
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.EncryptedSourceStrategies) == 0 {
		t.Fatal("expected frozen strategies")
	}
	if snapshot.EncryptedSourceStrategies[StepPreview].Strategy != EncryptedSourceStreamDecrypt {
		t.Fatalf("preview strategy=%+v", snapshot.EncryptedSourceStrategies[StepPreview])
	}
}

func TestPlannerCleanupEligibleRequiresFutureSourceStrategyRegistry(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 1, 0, 0)
	_, _ = db.Exec(`UPDATE library SET cleanup_local_source_after_package=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mid)
	tx, _ := db.BeginTx(context.Background(), nil)
	_, err := NewPlanner(PlanOptions{}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	_ = tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "lacks validated encrypted-source strategy") {
		t.Fatalf("err=%v", err)
	}
	var generation, rows int
	_ = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mid).Scan(&generation)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mid).Scan(&rows)
	if generation != 0 || rows != 0 {
		t.Fatalf("generation=%d rows=%d", generation, rows)
	}
}

func TestPlannerSnapshotsDeterministicFakeSourceStrategies(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mid, scan := seedPlannerMedia(t, db, "video", 1, 0, 0)
	_, _ = db.Exec(`UPDATE library SET cleanup_local_source_after_package=1 WHERE id=(SELECT library_id FROM media WHERE id=?)`, mid)
	registry := fakeSourceRegistry{StepPoster: {Strategy: EncryptedSourceDerivative, Validated: true}, StepScrape: {Strategy: EncryptedSourceDerivative, Validated: true}, StepPreview: {Strategy: EncryptedSourceStreamDecrypt, Validated: true}}
	run := planAndCommit(t, db, NewPlanner(PlanOptions{EncryptedSourceStrategies: registry}), NewMedia{MediaID: mid, ScanTaskID: scan, FileType: "video"})
	var raw string
	_ = db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE id=?`, run.ID).Scan(&raw)
	var snapshot ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.EncryptedSourceStrategies, map[StepType]EncryptedSourceContract{StepPoster: {Strategy: EncryptedSourceDerivative, Validated: true}, StepScrape: {Strategy: EncryptedSourceDerivative, Validated: true}, StepPreview: {Strategy: EncryptedSourceStreamDecrypt, Validated: true}}) {
		t.Fatalf("strategies=%v", snapshot.EncryptedSourceStrategies)
	}
}

func TestConfigSnapshotJSONDeterministicAcrossEquivalentStrategyMaps(t *testing.T) {
	makeSnapshot := func(reverse bool) ConfigSnapshot {
		m := map[StepType]EncryptedSourceContract{}
		if reverse {
			m[StepPreview] = EncryptedSourceContract{Strategy: EncryptedSourceStreamDecrypt, Validated: true}
			m[StepPoster] = EncryptedSourceContract{Strategy: EncryptedSourceDerivative, Validated: true}
		} else {
			m[StepPoster] = EncryptedSourceContract{Strategy: EncryptedSourceDerivative, Validated: true}
			m[StepPreview] = EncryptedSourceContract{Strategy: EncryptedSourceStreamDecrypt, Validated: true}
		}
		return ConfigSnapshot{PolicyVersion: PolicyV3, EncryptedSourceStrategies: m}
	}
	a, _ := json.Marshal(makeSnapshot(false))
	b, _ := json.Marshal(makeSnapshot(true))
	if !bytes.Equal(a, b) {
		t.Fatalf("snapshot bytes differ:\n%s\n%s", a, b)
	}
}

func seedIngestItem(t *testing.T, db *sql.DB, libraryID int64, submissionKey, canonicalPath string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO ingest_item(submission_key,source,library_id,canonical_path,path_key,state) VALUES(?,'upload',?,?,'path_key_'||?,'done')`, submissionKey, libraryID, canonicalPath, submissionKey)
	if err != nil {
		t.Fatalf("insert ingest_item: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestPlannerEventOriginProducesSameTopologyAsScan(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	itemID := seedIngestItem(t, db, 1, "evt-topo", "/evt")
	p := NewPlanner(PlanOptions{})
	scanRun := planAndCommit(t, db, p, NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	eventRun := planAndCommit(t, db, p, NewMedia{MediaID: mediaID, IngestItemID: itemID, FileType: "video"})
	scanSnap := loadPlannerSnapshot(t, db, scanRun.ID)
	eventSnap := loadPlannerSnapshot(t, db, eventRun.ID)
	if !reflect.DeepEqual(scanSnap.RequiredSteps, eventSnap.RequiredSteps) || !reflect.DeepEqual(scanSnap.OptionalSteps, eventSnap.OptionalSteps) {
		t.Fatalf("topology differs:\nscan  required=%v optional=%v\nevent required=%v optional=%v", scanSnap.RequiredSteps, scanSnap.OptionalSteps, eventSnap.RequiredSteps, eventSnap.OptionalSteps)
	}
	// Compare graph structure (node steps and edge relationships), ignoring generation numbers.
	if len(scanSnap.Graph.Nodes) != len(eventSnap.Graph.Nodes) || len(scanSnap.Graph.Edges) != len(eventSnap.Graph.Edges) {
		t.Fatalf("graph size differs: scan nodes=%d edges=%d event nodes=%d edges=%d", len(scanSnap.Graph.Nodes), len(scanSnap.Graph.Edges), len(eventSnap.Graph.Nodes), len(eventSnap.Graph.Edges))
	}
	for i, node := range scanSnap.Graph.Nodes {
		if node.Step != eventSnap.Graph.Nodes[i].Step || node.Required != eventSnap.Graph.Nodes[i].Required {
			t.Fatalf("node %d differs: scan=%+v event=%+v", i, node, eventSnap.Graph.Nodes[i])
		}
	}
	for i, edge := range scanSnap.Graph.Edges {
		other := eventSnap.Graph.Edges[i]
		if edge.Step != other.Step || edge.Kind != other.Kind || (edge.DependsOn == nil) != (other.DependsOn == nil) {
			t.Fatalf("edge %d differs: scan=%+v event=%+v", i, edge, other)
		}
		if edge.DependsOn != nil && other.DependsOn != nil && *edge.DependsOn != *other.DependsOn {
			t.Fatalf("edge %d target differs: scan=%s event=%s", i, *edge.DependsOn, *other.DependsOn)
		}
	}
	if scanRun.Generation == eventRun.Generation {
		t.Fatalf("generations should differ: scan=%d event=%d", scanRun.Generation, eventRun.Generation)
	}
}

func TestPlannerUploadOriginPersistsIngestItemLinkage(t *testing.T) {
	db := openPlannerTestDB(t)
	libraryID, mediaID, _ := seedPlannerMedia(t, db, "video", 0, 0, 0)
	itemID := seedIngestItem(t, db, libraryID, "upload-link", "/up")
	p := NewPlanner(PlanOptions{})
	run := planAndCommit(t, db, p, NewMedia{MediaID: mediaID, IngestItemID: itemID, FileType: "video"})
	var linkedItemID sql.NullInt64
	var reason string
	if err := db.QueryRow(`SELECT ingest_item_id,reason FROM media_ingest_run WHERE id=?`, run.ID).Scan(&linkedItemID, &reason); err != nil {
		t.Fatal(err)
	}
	if !linkedItemID.Valid || linkedItemID.Int64 != itemID {
		t.Fatalf("ingest_item_id=%v want %d", linkedItemID, itemID)
	}
	if reason != string(PlanReasonUpload) {
		t.Fatalf("reason=%q want %q", reason, PlanReasonUpload)
	}
}

func TestPlannerSourceReplacementReasonValidates(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	first := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonSourceReplaced, ExpectedGeneration: first.Generation})
	var reason string
	if err := db.QueryRow(`SELECT reason FROM media_ingest_run WHERE id=?`, result.Run.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != string(PlanReasonSourceReplaced) {
		t.Fatalf("reason=%q want %q", reason, PlanReasonSourceReplaced)
	}
}

func TestPlannerScanOriginRequiresScanTaskID(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, _ := seedPlannerMedia(t, db, "video", 0, 0, 0)
	tx, _ := db.BeginTx(context.Background(), nil)
	defer tx.Rollback()
	_, err := NewPlanner(PlanOptions{}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, FileType: "video"})
	if err == nil {
		t.Fatal("expected scan origin to require scan task id")
	}
}

func TestPlannerEventOriginRejectsZeroIngestItemID(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, _ := seedPlannerMedia(t, db, "video", 0, 0, 0)
	tx, _ := db.BeginTx(context.Background(), nil)
	defer tx.Rollback()
	_, err := NewPlanner(PlanOptions{}).PlanNewMediaTx(context.Background(), tx, NewMedia{MediaID: mediaID, FileType: "video"})
	if err == nil {
		t.Fatal("expected event origin to require valid ingest item id or scan task id")
	}
}

func TestPlannerReplacementSourceReplacedSupersedesOldGeneration(t *testing.T) {
	db := openPlannerTestDB(t)
	_, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	old := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "video"})
	result := planReplacementAndCommit(t, db, NewPlanner(PlanOptions{}), mediaID, ReplacementOptions{Reason: PlanReasonSourceReplaced, ExpectedGeneration: old.Generation})
	if result.OldGeneration != old.Generation || result.NewGeneration != old.Generation+1 {
		t.Fatalf("generations old=%d new=%d want old=%d new=%d", result.OldGeneration, result.NewGeneration, old.Generation, old.Generation+1)
	}
	var supersededBy sql.NullInt64
	if err := db.QueryRow(`SELECT superseded_by_generation FROM media_ingest_run WHERE id=?`, old.ID).Scan(&supersededBy); err != nil {
		t.Fatal(err)
	}
	if !supersededBy.Valid || supersededBy.Int64 != result.NewGeneration {
		t.Fatalf("superseded=%v want %d", supersededBy, result.NewGeneration)
	}
}

func TestReconcileStartupValidatesIngestRunLinkage(t *testing.T) {
	db := openPlannerTestDB(t)
	libraryID, mediaID, scanID := seedPlannerMedia(t, db, "video", 0, 0, 0)
	itemID := seedIngestItem(t, db, libraryID, "reconcile-link", "/reconcile")
	run := planAndCommit(t, db, NewPlanner(PlanOptions{}), NewMedia{MediaID: mediaID, IngestItemID: itemID, ScanTaskID: scanID, FileType: "video"})
	var linkedItemID sql.NullInt64
	if err := db.QueryRow(`SELECT ingest_item_id FROM media_ingest_run WHERE id=?`, run.ID).Scan(&linkedItemID); err != nil {
		t.Fatal(err)
	}
	if !linkedItemID.Valid || linkedItemID.Int64 != itemID {
		t.Fatalf("linked ingest_item_id=%v want %d", linkedItemID, itemID)
	}
	// ValidateAggregateCurrentPolicy should still pass with the linkage in place.
	if err := ValidateAggregateCurrentPolicy(context.Background(), db); err != nil {
		t.Fatalf("validate with linkage: %v", err)
	}
}
