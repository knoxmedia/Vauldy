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
	"time"
	"unsafe"

	"knox-media/internal/coreiface"
	"knox-media/internal/store"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_type,publication_state,publication_error,ingest_generation,published_at) VALUES(?,'retry-v2','video',?,'old outcome',1,'2026-07-01 02:03:04')`, libraryID, state)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,policy_version,error_message,finished_at) VALUES(?,1,'scan',?,?,2,'old run outcome',CURRENT_TIMESTAMP)`, mediaID, state, oldSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ = res.LastInsertId()
	stepState := state
	if state == "degraded" {
		stepState = "failed"
	}
	for _, step := range []string{"poster", "keyframe", "atrack", "prepare"} {
		res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,last_error) VALUES(?,?,1,?,1,?,'old step outcome')`, runID, mediaID, step, stepState)
		if err != nil {
			t.Fatal(err)
		}
		stepID, _ := res.LastInsertId()
		if step == "prepare" {
			task, e := db.Exec(`INSERT INTO transcode_task(file_id,status,task_type,media_id,ingest_run_id,ingest_step_id,generation) VALUES('old','failed','pretranscode',?,?,?,1)`, mediaID, runID, stepID)
			if e != nil {
				t.Fatal(e)
			}
			if enterprisePrepareTablesPresent(t, db) {
				taskID, _ := task.LastInsertId()
				if _, e = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,config_snapshot_json) VALUES(?,1,'old','failed','{"old":true}')`, taskID); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	return
}

func currentRetryPlanner(prepare *retryPreparePlanner) *Planner {
	return NewPlanner(PlanOptions{SubtitleAuto: true, EncryptGlobal: true, PreparePlanner: prepare, Capabilities: NewCapabilityMatrix([]string{"prepare"})})
}

func retrySQLiteError(t *testing.T, code int) error {
	t.Helper()
	err := &sqlite.Error{}
	field := reflect.ValueOf(err).Elem().FieldByName("code")
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().SetInt(int64(code))
	return err
}

func TestRetryIngestDoesNotRetryUncertainBusyCommit(t *testing.T) {
	original := retryIngestAttemptFn
	t.Cleanup(func() { retryIngestAttemptFn = original })
	busy := retrySQLiteError(t, sqlite3.SQLITE_BUSY)
	uncertain := &store.ImmediateCommitError{Cause: busy}
	attempts := 0
	retryIngestAttemptFn = func(context.Context, *sql.DB, int64, *Planner) error {
		attempts++
		return uncertain
	}

	err := RetryIngest(context.Background(), openRetryTestDB(t), 1, NewPlanner(PlanOptions{}))
	var got *store.ImmediateCommitError
	if attempts != 1 || !errors.As(err, &got) || got != uncertain || errors.Is(err, ErrNoRetryableWork) {
		t.Fatalf("attempts=%d err=%v typed=%v same=%v", attempts, err, got != nil, got == uncertain)
	}
}

func TestRetryIngestRetriesPrecommitBusy(t *testing.T) {
	original := retryIngestAttemptFn
	t.Cleanup(func() { retryIngestAttemptFn = original })
	busy := retrySQLiteError(t, sqlite3.SQLITE_BUSY)
	attempts := 0
	retryIngestAttemptFn = func(context.Context, *sql.DB, int64, *Planner) error {
		attempts++
		if attempts == 1 {
			return busy
		}
		return nil
	}
	if err := RetryIngest(context.Background(), openRetryTestDB(t), 1, NewPlanner(PlanOptions{})); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestRetryIngestDegradedCreatesVisibilityPreservingReplacement(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, oldRun, oldSnapshot := seedTerminalRetry(t, db, "degraded")
	if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
		t.Fatal(err)
	}
	var generation, preserve int
	var mediaState, mediaErr, publishedAt, reason, runState, snapshot string
	if err := db.QueryRow(`SELECT ingest_generation,publication_state,publication_error,published_at FROM media WHERE id=?`, mediaID).Scan(&generation, &mediaState, &mediaErr, &publishedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT reason,status,preserve_visibility,config_snapshot_json FROM media_ingest_run WHERE media_id=? AND generation=2`, mediaID).Scan(&reason, &runState, &preserve, &snapshot); err != nil {
		t.Fatal(err)
	}
	if generation != 2 || mediaState != "degraded" || mediaErr != "old outcome" || publishedAt != "2026-07-01T02:03:04Z" {
		t.Fatalf("media=%d/%s/%q/%s", generation, mediaState, mediaErr, publishedAt)
	}
	if reason != "manual_retry" || runState != "processing" || preserve != 1 || snapshot == oldSnapshot {
		t.Fatalf("new run=%s/%s/%d snapshotEqual=%v", reason, runState, preserve, snapshot == oldSnapshot)
	}
	var oldStatus, oldError, oldFinished string
	if err := db.QueryRow(`SELECT status,error_message,finished_at FROM media_ingest_run WHERE id=?`, oldRun).Scan(&oldStatus, &oldError, &oldFinished); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "degraded" || oldError != "old run outcome" || oldFinished == "" {
		t.Fatalf("old run changed=%s/%q/%q", oldStatus, oldError, oldFinished)
	}
	var oldSteps, oldTasks, oldJobs int
	prepareTables := enterprisePrepareTablesPresent(t, db)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE run_id=? AND status='failed' AND attempts=0 AND last_error='old step outcome'`, oldRun).Scan(&oldSteps)
	_ = db.QueryRow(`SELECT COUNT(*) FROM transcode_task WHERE ingest_run_id=? AND status='failed'`, oldRun).Scan(&oldTasks)
	if prepareTables {
		_ = db.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id WHERE t.ingest_run_id=? AND j.status='failed' AND j.config_snapshot_json='{"old":true}'`, oldRun).Scan(&oldJobs)
	}
	if oldSteps != 4 || oldTasks != 1 || (prepareTables && oldJobs != 1) {
		t.Fatalf("old execution changed: steps=%d tasks=%d jobs=%d", oldSteps, oldTasks, oldJobs)
	}
}

func TestRetryIngestDegradedUsesCurrentPrepareCapability(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, oldRun, _ := seedTerminalRetry(t, db, "degraded")
	if enterprisePrepareTablesPresent(t, db) {
		if _, err := db.Exec(`DELETE FROM pretranscode_rendition_job WHERE task_id IN (SELECT id FROM transcode_task WHERE ingest_run_id=?)`, oldRun); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM transcode_task WHERE ingest_run_id=?; DELETE FROM media_ingest_step WHERE run_id=? AND step_type='prepare'; UPDATE media_ingest_run SET config_snapshot_json='{"policy_version":2,"library_id":1,"file_type":"video","steps":["poster"]}' WHERE id=?`, oldRun, oldRun, oldRun); err != nil {
		t.Fatal(err)
	}
	prepare := &retryPreparePlanner{}
	restore := coreiface.RegisterIngestPreparePlanner(prepare)
	t.Cleanup(restore)
	planner := NewPlanner(PlanOptions{PreparePlanner: coreiface.IngestPreparePlannerHandle(), Capabilities: NewCapabilityMatrix([]string{"prepare"})})
	if err := RetryIngest(context.Background(), db, mediaID, planner); err != nil {
		t.Fatal(err)
	}
	var steps, tasks int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=? AND generation=2 AND step_type='prepare'`, mediaID).Scan(&steps)
	_ = db.QueryRow(`SELECT COUNT(*) FROM transcode_task WHERE media_id=? AND generation=2`, mediaID).Scan(&tasks)
	if steps != 1 || tasks != 0 || prepare.calls != 1 {
		t.Fatalf("prepare steps=%d tasks=%d planner calls=%d", steps, tasks, prepare.calls)
	}
}

func TestRetryIngestDegradedOmitsPrepareWithoutBothCurrentConstraints(t *testing.T) {
	for _, tc := range []struct {
		name       string
		registered bool
		capability bool
	}{
		{name: "planner_missing", capability: true},
		{name: "capability_missing", registered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openRetryTestDB(t)
			mediaID, _, _ := seedTerminalRetry(t, db, "degraded")
			var plannerHandle coreiface.IngestPreparePlanner
			if tc.registered {
				prepare := &retryPreparePlanner{}
				restore := coreiface.RegisterIngestPreparePlanner(prepare)
				defer restore()
				plannerHandle = coreiface.IngestPreparePlannerHandle()
			}
			var caps []string
			if tc.capability {
				caps = []string{"prepare"}
			}
			planner := NewPlanner(PlanOptions{PreparePlanner: plannerHandle, Capabilities: NewCapabilityMatrix(caps)})
			if err := RetryIngest(context.Background(), db, mediaID, planner); err != nil {
				t.Fatal(err)
			}
			var count int
			_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_step WHERE media_id=? AND generation=2 AND step_type='prepare'`, mediaID).Scan(&count)
			if count != 0 {
				t.Fatalf("prepare steps=%d", count)
			}
		})
	}
}

func TestRetryIngestDegradedReplacementTerminalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, stepStatus, stepError, wantState, wantError string
	}{
		{name: "success", stepStatus: "done", wantState: "published"},
		{name: "failure", stepStatus: "failed", stepError: "new poster failure", wantState: "degraded", wantError: "poster: new poster failure; encrypt: new poster failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openRetryTestDB(t)
			mediaID, _, _ := seedTerminalRetry(t, db, "degraded")
			if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
				t.Fatal(err)
			}
			var runID int64
			if err := db.QueryRow(`SELECT id FROM media_ingest_run WHERE media_id=? AND generation=2`, mediaID).Scan(&runID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media_ingest_step SET status=?,last_error=? WHERE run_id=? AND required=1`, tc.stepStatus, tc.stepError, runID); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = AggregateTx(context.Background(), tx, runID); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var state, publicationError, publishedAt string
			if err = db.QueryRow(`SELECT publication_state,publication_error,published_at FROM media WHERE id=?`, mediaID).Scan(&state, &publicationError, &publishedAt); err != nil {
				t.Fatal(err)
			}
			if state != tc.wantState || publicationError != tc.wantError || publishedAt != "2026-07-01T02:03:04Z" {
				t.Fatalf("media=%s/%q/%s", state, publicationError, publishedAt)
			}
		})
	}
}

func TestRetryIngestAllowsMismatchedRunAndIdempotentProcessing(t *testing.T) {
	t.Run("degraded_failed_run_plans", func(t *testing.T) {
		db := openRetryTestDB(t)
		mediaID, runID, _ := seedTerminalRetry(t, db, "degraded")
		if _, err := db.Exec(`UPDATE media_ingest_run SET status='failed' WHERE id=?`, runID); err != nil {
			t.Fatal(err)
		}
		if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
			t.Fatalf("err=%v", err)
		}
		var generation, preserve int
		var mediaState, reason, runState string
		_ = db.QueryRow(`SELECT ingest_generation,publication_state FROM media WHERE id=?`, mediaID).Scan(&generation, &mediaState)
		_ = db.QueryRow(`SELECT reason,status,preserve_visibility FROM media_ingest_run WHERE media_id=? AND generation=?`, mediaID, generation).Scan(&reason, &runState, &preserve)
		if generation != 2 || mediaState != "degraded" || reason != "manual_retry" || runState != "processing" || preserve != 1 {
			t.Fatalf("generation=%d media=%s run=%s/%s preserve=%d", generation, mediaState, reason, runState, preserve)
		}
	})
	t.Run("failed_degraded_run_plans", func(t *testing.T) {
		db := openRetryTestDB(t)
		mediaID, runID, _ := seedTerminalRetry(t, db, "failed")
		if _, err := db.Exec(`UPDATE media_ingest_run SET status='degraded' WHERE id=?`, runID); err != nil {
			t.Fatal(err)
		}
		if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
			t.Fatalf("err=%v", err)
		}
		var generation, preserve int
		var mediaState, reason, runState string
		_ = db.QueryRow(`SELECT ingest_generation,publication_state FROM media WHERE id=?`, mediaID).Scan(&generation, &mediaState)
		_ = db.QueryRow(`SELECT reason,status,preserve_visibility FROM media_ingest_run WHERE media_id=? AND generation=?`, mediaID, generation).Scan(&reason, &runState, &preserve)
		if generation != 2 || mediaState != "processing" || reason != "manual_retry" || runState != "processing" || preserve != 0 {
			t.Fatalf("generation=%d media=%s run=%s/%s preserve=%d", generation, mediaState, reason, runState, preserve)
		}
	})
	t.Run("degraded_processing_is_idempotent", func(t *testing.T) {
		db := openRetryTestDB(t)
		mediaID, runID, _ := seedTerminalRetry(t, db, "degraded")
		if _, err := db.Exec(`UPDATE media_ingest_run SET status='processing',reason='manual_retry' WHERE id=?`, runID); err != nil {
			t.Fatal(err)
		}
		if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
			t.Fatalf("err=%v", err)
		}
		var generation, runs int
		_ = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation)
		_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs)
		if generation != 1 || runs != 1 {
			t.Fatalf("idempotent retry mutated state generation=%d runs=%d", generation, runs)
		}
	})
	t.Run("failed_processing_is_idempotent", func(t *testing.T) {
		db := openRetryTestDB(t)
		mediaID, runID, _ := seedTerminalRetry(t, db, "failed")
		if _, err := db.Exec(`UPDATE media_ingest_run SET status='processing',reason='manual_retry' WHERE id=?`, runID); err != nil {
			t.Fatal(err)
		}
		if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{})); err != nil {
			t.Fatalf("err=%v", err)
		}
		var generation, runs int
		_ = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation)
		_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs)
		if generation != 1 || runs != 1 {
			t.Fatalf("idempotent retry mutated state generation=%d runs=%d", generation, runs)
		}
	})
}

func TestRetryIngestDegradedRequiresPlanner(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, _ := seedTerminalRetry(t, db, "degraded")
	if err := RetryIngest(context.Background(), db, mediaID, nil); err == nil || errors.Is(err, ErrNoRetryableWork) {
		t.Fatalf("err=%v", err)
	}
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
	want := []string{"poster", "encrypt", "media_visible", "scrape", "preview", "subtitle_extract", "prepare"}
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
	if enterprisePrepareTablesPresent(t, db) {
		_ = db.QueryRow(`SELECT COUNT(*) FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id WHERE t.media_id=? AND t.generation=2 AND j.config_snapshot_json='{"old":true}'`, mediaID).Scan(&copied)
	}
	if snapshot == oldSnapshot || copied != 0 {
		t.Fatalf("snapshotEqual=%v copiedPrepare=%d", snapshot == oldSnapshot, copied)
	}
}

func TestRetryIngestCommitContentionHonorsRetryBudgetWithoutPartialReplacement(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, _ := seedTerminalRetry(t, db, "degraded")
	if _, err := db.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatal(err)
	}

	// In rollback-journal mode this reader permits BEGIN IMMEDIATE and all writes,
	// but prevents the writer from upgrading to EXCLUSIVE only at COMMIT.
	reader, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err = reader.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&state); err != nil {
		_ = reader.Rollback()
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(3 * time.Second)
		_ = reader.Rollback()
		close(released)
	}()

	started := time.Now()
	err = RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(&retryPreparePlanner{}))
	elapsed := time.Since(started)
	<-released
	if err == nil {
		t.Fatalf("retry unexpectedly committed after blocked commit in %v", elapsed)
	}
	var commitErr *store.ImmediateCommitError
	if !errors.As(err, &commitErr) {
		t.Fatalf("contention did not reach commit: err=%v", err)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("retry exceeded 2s policy budget: %v err=%v", elapsed, err)
	}
	var generation, runs int
	if err = db.QueryRow(`SELECT ingest_generation FROM media WHERE id=?`, mediaID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if generation != 1 || runs != 1 {
		t.Fatalf("partial replacement generation=%d runs=%d", generation, runs)
	}
	var busyTimeout int
	if err = db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 30000 {
		t.Fatalf("pooled busy_timeout=%d want restored 30000", busyTimeout)
	}
}

func TestRetryIngestConcurrentOneGeneration(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, _ := seedTerminalRetry(t, db, "degraded")
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
	if successes < 1 || successes+conflicts != 2 || generation != 2 || runs != 1 {
		t.Fatalf("success=%d conflict=%d generation=%d runs=%d", successes, conflicts, generation, runs)
	}
}

func TestRetryIngestPlannerFailureRollback(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, _ := seedTerminalRetry(t, db, "degraded")
	prepare := &retryPreparePlanner{err: errors.New("prepare plan failed")}
	if err := RetryIngest(context.Background(), db, mediaID, currentRetryPlanner(prepare)); err == nil || !errors.Is(err, prepare.err) {
		t.Fatalf("err=%v", err)
	}
	var generation, runs int
	var state, mediaErr string
	_ = db.QueryRow(`SELECT ingest_generation,publication_state,publication_error FROM media WHERE id=?`, mediaID).Scan(&generation, &state, &mediaErr)
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_run WHERE media_id=?`, mediaID).Scan(&runs)
	if generation != 1 || runs != 1 || state != "degraded" || mediaErr != "old outcome" {
		t.Fatalf("rollback=%d/%d/%s/%q", generation, runs, state, mediaErr)
	}
}

func TestRetryIngestHistoricalEvidenceStagesImmutable(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, runID, _ := seedTerminalRetry(t, db, "degraded")
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

func seedOptionalPostIngestRetry(t *testing.T, db *sql.DB, mediaState, runState, stepType string, required int) (mediaID, runID, stepID, queueID int64) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('optional-retry','video','/optional-retry')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_type,publication_state,published_at,publication_error,ingest_generation) VALUES(?,?,'video',?,'2026-07-01 00:00:00','preserve me',1)`, libraryID, "optional-retry-"+stepType, mediaState)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json,error_message,finished_at) VALUES(?,1,'scan',?,1,'{}','run outcome',CURRENT_TIMESTAMP)`, mediaID, runState)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error,lease_owner,lease_until,started_at,finished_at) VALUES(?,?,1,?,?, 'failed',3,3,'old step error','old-owner','2026-01-01','2026-01-01','2026-01-01')`, runID, mediaID, stepType, required)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,last_error,lease_owner,lease_until,started_at,finished_at) VALUES(?,?,?,1,?,'failed',3,3,'old queue error','old-owner','2026-01-01','2026-01-01','2026-01-01')`, mediaID, runID, stepID, stepType)
	if err != nil {
		t.Fatal(err)
	}
	queueID, _ = res.LastInsertId()
	return
}

func TestRetryOptionalPostIngestResetsOnlyTerminalOptionalWork(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, stepID, queueID := seedOptionalPostIngestRetry(t, db, "published", "published", "preview", 0)
	err := RetryOptionalPostIngest(context.Background(), db, OptionalPostIngestRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 42, Reason: "operator requested preview"})
	if err != nil {
		t.Fatal(err)
	}
	var qs, ss, rs, ms, qe, se, me string
	var qa, sa, qr int
	err = db.QueryRow(`SELECT q.status,s.status,r.status,m.publication_state,q.attempts,s.attempts,q.retry_round,q.last_error,s.last_error,m.publication_error FROM post_ingest_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id JOIN media_ingest_run r ON r.id=q.ingest_run_id JOIN media m ON m.id=q.media_id WHERE q.id=?`, queueID).Scan(&qs, &ss, &rs, &ms, &qa, &sa, &qr, &qe, &se, &me)
	if err != nil {
		t.Fatal(err)
	}
	if qs != "waiting" || ss != "waiting" || qa != 0 || sa != 0 || qr != 1 || qe != "" || se != "" {
		t.Fatalf("queue/step=%s/%s attempts=%d/%d round=%d errors=%q/%q", qs, ss, qa, sa, qr, qe, se)
	}
	if rs != "published" || ms != "published" || me != "preserve me" {
		t.Fatalf("outcome mutated run=%s media=%s error=%q", rs, ms, me)
	}
	var family, typ, reason, pqs, pss, pqe, pse string
	var actor, attempts, round int
	err = db.QueryRow(`SELECT task_family,task_type,actor_id,reason,previous_queue_status,previous_step_status,previous_attempts,previous_queue_error,previous_step_error,retry_round FROM media_ingest_optional_retry_audit WHERE step_id=?`, stepID).Scan(&family, &typ, &actor, &reason, &pqs, &pss, &attempts, &pqe, &pse, &round)
	if err != nil {
		t.Fatal(err)
	}
	if family != "post_ingest" || typ != "preview" || actor != 42 || reason != "operator requested preview" || pqs != "failed" || pss != "failed" || attempts != 3 || pqe != "old queue error" || pse != "old step error" || round != 1 {
		t.Fatalf("audit mismatch")
	}
}

func TestRetryOptionalPostIngestRejectsRequiredNonterminalAndStale(t *testing.T) {
	for _, tc := range []struct {
		name     string
		required int
		mutate   func(*sql.DB, int64, int64)
	}{
		{"required", 1, nil},
		{"nonterminal", 0, func(db *sql.DB, step, queue int64) {
			db.Exec(`UPDATE media_ingest_step SET status='waiting' WHERE id=?`, step)
			db.Exec(`UPDATE post_ingest_task SET status='waiting' WHERE id=?`, queue)
		}},
		{"stale", 0, func(db *sql.DB, step, queue int64) {
			db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=(SELECT media_id FROM media_ingest_step WHERE id=?)`, step)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openRetryTestDB(t)
			mediaID, _, stepID, queueID := seedOptionalPostIngestRetry(t, db, "published", "published", "subtitle", tc.required)
			if tc.mutate != nil {
				tc.mutate(db, stepID, queueID)
			}
			if err := RetryOptionalPostIngest(context.Background(), db, OptionalPostIngestRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 1, Reason: "test"}); !errors.Is(err, ErrNoRetryableWork) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRetryOptionalPostIngestAllowsExhaustedCancelledWork(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, stepID, queueID := seedOptionalPostIngestRetry(t, db, "degraded", "degraded", "subtitle", 0)
	if _, err := db.Exec(`UPDATE media_ingest_step SET status='cancelled' WHERE id=?`, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='cancelled',attempts=max_attempts WHERE id=?`, queueID); err != nil {
		t.Fatal(err)
	}
	if err := RetryOptionalPostIngest(context.Background(), db, OptionalPostIngestRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 9, Reason: "retry cancelled subtitle"}); err != nil {
		t.Fatal(err)
	}
	var queueStatus, stepStatus, runStatus, mediaStatus string
	if err := db.QueryRow(`SELECT q.status,s.status,r.status,m.publication_state FROM post_ingest_task q JOIN media_ingest_step s ON s.id=q.ingest_step_id JOIN media_ingest_run r ON r.id=q.ingest_run_id JOIN media m ON m.id=q.media_id WHERE q.id=?`, queueID).Scan(&queueStatus, &stepStatus, &runStatus, &mediaStatus); err != nil {
		t.Fatal(err)
	}
	if queueStatus != "waiting" || stepStatus != "waiting" || runStatus != "degraded" || mediaStatus != "degraded" {
		t.Fatalf("states queue=%s step=%s run=%s media=%s", queueStatus, stepStatus, runStatus, mediaStatus)
	}
}

func TestRetryOptionalPostIngestRejectsUnexhaustedQueue(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, stepID, queueID := seedOptionalPostIngestRetry(t, db, "published", "published", "preview", 0)
	if _, err := db.Exec(`UPDATE post_ingest_task SET attempts=2,max_attempts=3 WHERE id=?`, queueID); err != nil {
		t.Fatal(err)
	}
	if err := RetryOptionalPostIngest(context.Background(), db, OptionalPostIngestRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 1, Reason: "too early"}); !errors.Is(err, ErrNoRetryableWork) {
		t.Fatalf("err=%v", err)
	}
}
func TestRetryOptionalPostIngestConcurrentCreatesOneAudit(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, _, stepID, _ := seedOptionalPostIngestRetry(t, db, "degraded", "degraded", "subtitle", 0)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- RetryOptionalPostIngest(context.Background(), db, OptionalPostIngestRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 7, Reason: "concurrent"})
		}()
	}
	wg.Wait()
	close(errs)
	var successes int
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrNoRetryableWork) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d", successes)
	}
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_ingest_optional_retry_audit WHERE step_id=?`, stepID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}

func TestAggregatePreservesTerminalOutcomeAfterOptionalRetry(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, runID, stepID, _ := seedOptionalPostIngestRetry(t, db, "degraded", "degraded", "subtitle", 0)
	db.Exec(`UPDATE media SET publication_state='degraded',publication_error='media exact error',published_at='2026-07-02 03:04:05' WHERE id=?`, mediaID)
	db.Exec(`UPDATE media_ingest_run SET status='degraded',error_message='run exact error',finished_at='2026-07-02 03:04:05' WHERE id=?`, runID)
	if err := RetryOptionalPostIngest(context.Background(), db, OptionalPostIngestRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 7, Reason: "preserve"}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := AggregateTx(context.Background(), tx, runID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var runStatus, runErr, runFinished, mediaStatus, mediaErr, published string
	err = db.QueryRow(`SELECT r.status,r.error_message,r.finished_at,m.publication_state,m.publication_error,m.published_at FROM media_ingest_run r JOIN media m ON m.id=r.media_id WHERE r.id=?`, runID).Scan(&runStatus, &runErr, &runFinished, &mediaStatus, &mediaErr, &published)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "degraded" || runErr != "run exact error" || runFinished != "2026-07-02T03:04:05Z" || mediaStatus != "degraded" || mediaErr != "media exact error" || published != "2026-07-02T03:04:05Z" {
		t.Fatalf("status=%s/%s/%s media=%s/%s/%s", runStatus, runErr, runFinished, mediaStatus, mediaErr, published)
	}
}

func seedOptionalScrapeRetry(t *testing.T, db *sql.DB) (mediaID, runID, stepID, queueID int64) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('optional-scrape','video','/optional-scrape')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_type,publication_state,published_at,publication_error,ingest_generation) VALUES(?,'optional-scrape','video','published','2026-07-01 00:00:00','preserve scrape',1)`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json,error_message,finished_at) VALUES(?,1,'scan','published',1,'{}','run outcome',CURRENT_TIMESTAMP)`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error,lease_owner,lease_until,started_at,finished_at) VALUES(?,?,1,'scrape',0,'failed',?,?, 'old step error','old-owner','2026-01-01','2026-01-01','2026-01-01')`, runID, mediaID, DefaultNetworkMaxAttempts, DefaultNetworkMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scrape_task(media_id,source,status,fail_count,message,ingest_run_id,ingest_step_id,generation,lease_owner,lease_until,started_at,finished_at) VALUES(?,'auto','failed',?,'old queue error',?,?,1,'old-owner','2026-01-01','2026-01-01','2026-01-01')`, mediaID, DefaultNetworkMaxAttempts, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	queueID, _ = res.LastInsertId()
	return
}

func TestRetryOptionalPostIngestFinalizesBarrierPlanAndAggregate(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, runID, stepID, _ := seedOptionalPostIngestRetry(t, db, "published", "published", "preview", 0)
	seen := 0
	SetRetirementBarrierProbeForTest(func(id int64) {
		if id == runID {
			seen++
		}
	})
	t.Cleanup(ClearRetirementBarrierProbeForTest)
	if err := RetryOptionalPostIngest(context.Background(), db, OptionalPostIngestRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 11, Reason: "finalizer consistency"}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("retirement barrier calls=%d", seen)
	}
	var all, waiting int
	var pub, runState string
	if err := db.QueryRow(`SELECT all_terminal,waiting_count FROM media_plan_completion WHERE run_id=?`, runID).Scan(&all, &waiting); err != nil {
		t.Fatalf("plan completion missing: %v", err)
	}
	if err := db.QueryRow(`SELECT r.status,m.publication_state FROM media_ingest_run r JOIN media m ON m.id=r.media_id WHERE r.id=?`, runID).Scan(&runState, &pub); err != nil {
		t.Fatal(err)
	}
	if all != 0 || waiting != 1 || runState != "published" || pub != "published" {
		t.Fatalf("plan all=%d waiting=%d run=%s media=%s", all, waiting, runState, pub)
	}
}

func TestRetryOptionalScrapeFinalizesBarrierPlanAndAggregate(t *testing.T) {
	db := openRetryTestDB(t)
	mediaID, runID, stepID, _ := seedOptionalScrapeRetry(t, db)
	seen := 0
	SetRetirementBarrierProbeForTest(func(id int64) {
		if id == runID {
			seen++
		}
	})
	t.Cleanup(ClearRetirementBarrierProbeForTest)
	if err := RetryOptionalScrape(context.Background(), db, OptionalScrapeRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 12, Reason: "finalizer consistency"}, NewCapabilityMatrix([]string{"scrape"})); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("retirement barrier calls=%d", seen)
	}
	var all, waiting int
	var pub, runState, qs, ss string
	if err := db.QueryRow(`SELECT all_terminal,waiting_count FROM media_plan_completion WHERE run_id=?`, runID).Scan(&all, &waiting); err != nil {
		t.Fatalf("plan completion missing: %v", err)
	}
	if err := db.QueryRow(`SELECT r.status,m.publication_state,q.status,s.status FROM media_ingest_run r JOIN media m ON m.id=r.media_id JOIN scrape_task q ON q.ingest_run_id=r.id JOIN media_ingest_step s ON s.id=q.ingest_step_id WHERE r.id=?`, runID).Scan(&runState, &pub, &qs, &ss); err != nil {
		t.Fatal(err)
	}
	if all != 0 || waiting != 1 || runState != "published" || pub != "published" || qs != "waiting" || ss != "waiting" {
		t.Fatalf("plan all=%d waiting=%d run=%s media=%s queue=%s step=%s", all, waiting, runState, pub, qs, ss)
	}
}
