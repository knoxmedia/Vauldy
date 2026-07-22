package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"knox-media/internal/store"
)

func openRetryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "retry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type retryPreparePlanner struct {
	err   error
	calls int
}

func (p *retryPreparePlanner) PlanIngestPrepareTx(ctx context.Context, tx store.SQLExecutor, mediaID, runID, stepID, generation int64) error {
	p.calls++
	return p.err
}

func seedTerminalRetry(t *testing.T, db *sql.DB, state string) (mediaID, runID int64, oldSnapshot string) {
	t.Helper()
	oldSnapshot = `{"policy_version":2,"library_id":1,"file_type":"video","steps":["poster","keyframe","atrack","prepare"],"required_steps":["poster","keyframe","atrack"],"optional_steps":["prepare"]}`
	res, err := db.Exec(`INSERT INTO library(name,type,path,preview_extract,encrypted_assets_enabled,jit_prepare_on_ingest) VALUES('retry','video','/retry',1,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_type,publication_state,publication_error,ingest_generation) VALUES(?,'retry-v2','video',?,'old outcome',1)`, libraryID, state)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version,error_message,finished_at) VALUES(?,1,'scan',?,?,2,'old run outcome',CURRENT_TIMESTAMP)`, mediaID, state, oldSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ = res.LastInsertId()
	for _, step := range []string{"poster", "keyframe", "atrack", "prepare"} {
		res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,last_error) VALUES(?,?,1,?,1,?,'old step outcome')`, runID, mediaID, step, state)
		if err != nil {
			t.Fatal(err)
		}
		stepID, _ := res.LastInsertId()
		if step == "prepare" {
			task, e := db.Exec(`INSERT INTO transcode_task(file_id,status,task_type,media_id,ingest_run_id,ingest_step_id,generation) VALUES('old','failed','pretranscode',?,?,?,1)`, mediaID, runID, stepID)
			if e != nil {
				t.Fatal(e)
			}
			taskID, _ := task.LastInsertId()
			if _, e = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,config_snapshot_json) VALUES(?,1,'old','failed','{"old":true}')`, taskID); e != nil {
				t.Fatal(e)
			}
		}
	}
	return
}

func currentRetryPlanner(prepare *retryPreparePlanner) *Planner {
	return NewPlanner(PlanOptions{SubtitleAuto: true, EncryptGlobal: true, PreparePlanner: prepare, Capabilities: NewCapabilityMatrix([]string{"prepare"})})
}

func TestRetryIngestFailedCreatesCurrentPolicyManualRetryGeneration(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, oldRun, oldSnapshot := seedTerminalRetry(t, db, "failed")
	prepare := &retryPreparePlanner{}
	if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(prepare)); err != nil {
		t.Fatal(err)
	}
	var generation int64
	var state, mediaErr string
	if err := db.QueryRow(`SELECT ingest_generation,publication_state,publication_error FROM media WHERE id=?`, mediaID).Scan(&generation, &state, &mediaErr); err != nil {
		t.Fatal(err)
	}
	if generation != 2 || state != "processing" || mediaErr != "" {
		t.Fatalf("media=%d/%s/%q", generation, state, mediaErr)
	}
	var reason, status, snapshot string
	var preserve int
	if err := db.QueryRow(`SELECT reason,status,preserve_visibility,config_snapshot_json FROM media_ingest_run WHERE media_id=? AND generation=2`, mediaID).Scan(&reason, &status, &preserve, &snapshot); err != nil {
		t.Fatal(err)
	}
	if reason != "manual_retry" || status != "processing" || preserve != 0 || snapshot == oldSnapshot {
		t.Fatalf("new run=%s/%s/%d snapshotEqual=%v", reason, status, preserve, snapshot == oldSnapshot)
	}
	var oldStatus, oldError string
	if err := db.QueryRow(`SELECT status,error_message FROM media_ingest_run WHERE id=?`, oldRun).Scan(&oldStatus, &oldError); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "failed" || oldError != "old run outcome" {
		t.Fatalf("old run changed=%s/%q", oldStatus, oldError)
	}
}

func TestRetryIngestCancelledCreatesCurrentPolicyManualRetryGeneration(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, oldRun, _ := seedTerminalRetry(t, db, "cancelled")
	if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
		t.Fatal(err)
	}
	var generation int
	var oldStatus string
	_ = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation)
	_ = db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, oldRun).Scan(&oldStatus)
	if generation != 2 || oldStatus != "cancelled" {
		t.Fatalf("generation=%d old=%s", generation, oldStatus)
	}
}

func TestRetryIngestAddsNewlyRequiredEncrypt(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, _ := seedTerminalRetry(t, db, "failed")
	if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
		t.Fatal(err)
	}
	var required int
	if err := db.QueryRow(`SELECT required FROM media_ingest_step WHERE media_id=? AND generation=2 AND step_type='encrypt'`, mediaID).Scan(&required); err != nil {
		t.Fatal(err)
	}
	if required != 1 {
		t.Fatalf("encrypt required=%d", required)
	}
}

func TestRetryIngestDropsRemovedOptionalAndOldKeyframeAtrack(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, _ := seedTerminalRetry(t, db, "failed")
	if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT step_type FROM media_ingest_step WHERE media_id=? AND generation=2 ORDER BY id`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	want := []string{"poster", "encrypt", "scrape", "preview", "subtitle", "prepare"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("steps=%v want=%v", got, want)
	}
}

func TestRetryIngestDoesNotCopyOldSnapshotOrPrepareRows(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, oldSnapshot := seedTerminalRetry(t, db, "failed")
	if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
		t.Fatal(err)
	}
	var snapshot string
	_ = db.QueryRow(`SELECT config_snapshot_json FROM media_ingest_run WHERE media_id=? AND generation=2`, mediaID).Scan(&snapshot)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(snapshot), &parsed); err != nil {
		t.Fatal(err)
	}
	var copied int
	_ = db.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id WHERE t.media_id=? AND t.generation=2 AND j.config_snapshot_json='{"old":true}'`, mediaID).Scan(&copied)
	if snapshot == oldSnapshot || copied != 0 {
		t.Fatalf("snapshotEqual=%v copiedPrepare=%d", snapshot == oldSnapshot, copied)
	}
}

func TestRetryIngestConcurrentOneGeneration(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, _ := seedTerminalRetry(t, db, "failed")
	db.SetMaxOpenConns(2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{}))
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	conflicts := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrNoRetryableWork) || errors.Is(err, ErrGenerationConflict) {
			conflicts++
		} else {
			t.Fatalf("retry err=%v", err)
		}
	}
	var generation, runs int
	_ = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=? AND generation=2`, mediaID).Scan(&runs)
	if successes != 1 || conflicts != 1 || generation != 2 || runs != 1 {
		t.Fatalf("success=%d conflict=%d generation=%d runs=%d", successes, conflicts, generation, runs)
	}
}

func TestRetryIngestPlannerFailureRollback(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, _ := seedTerminalRetry(t, db, "failed")
	prepare := &retryPreparePlanner{err: errors.New("prepare plan failed")}
	if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(prepare)); err == nil || !errors.Is(err, prepare.err) {
		t.Fatalf("err=%v", err)
	}
	var generation, runs int
	var state, mediaErr string
	_ = db.QueryRow(`SELECT ingest_generation,publication_state,publication_error FROM media WHERE id=?`, mediaID).Scan(&generation, &state, &mediaErr)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs)
	if generation != 1 || runs != 1 || state != "failed" || mediaErr != "old outcome" {
		t.Fatalf("rollback=%d/%d/%s/%q", generation, runs, state, mediaErr)
	}
}

func TestRetryIngestHistoricalEvidenceStagesImmutable(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, runID, _ := seedTerminalRetry(t, db, "failed")
	var stepID int64
	_ = db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? ORDER BY id LIMIT 1`, runID).Scan(&stepID)
	_, err := db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('old-stage',?,?,?,1,'owner','fp','poster','committed','/old','{}'); INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,verified_at,stage_id) VALUES(?,?,?,1,'poster','fp','{}',CURRENT_TIMESTAMP,'old-stage')`, mediaID, runID, stepID, runID, stepID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"media_asset_stage_journal", "media_ingest_evidence"} {
		var old, new int
		_ = db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE run_id=?`, runID).Scan(&old)
		_ = db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE media_id=? AND generation=2`, mediaID).Scan(&new)
		if old != 1 || new != 0 {
			t.Fatalf("%s old=%d new=%d", table, old, new)
		}
	}
}
