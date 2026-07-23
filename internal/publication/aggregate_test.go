package publication

import (
	"context"
	"database/sql"
	"knox-media/internal/store"
	"testing"
	"time"
)

func aggregateDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func aggregateFixture(t *testing.T, state string, generation int64, steps map[string]string) (db *sql.DB, runID, mediaID int64) {
	t.Helper()
	db = aggregateDB(t)
	r, e := db.Exec("INSERT INTO library(name,type,path) VALUES('a','video','/a')")
	if e != nil {
		t.Fatal(e)
	}
	lid, _ := r.LastInsertId()
	r, e = db.Exec("INSERT INTO media(library_id,file_id,file_type,publication_state,ingest_generation) VALUES(?,?,?,'processing',?)", lid, "f", "video", generation)
	if e != nil {
		t.Fatal(e)
	}
	mediaID, _ = r.LastInsertId()
	r, e = db.Exec("INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json) VALUES(?,?,'scan',?,0,'{}')", mediaID, generation, state)
	if e != nil {
		t.Fatal(e)
	}
	runID, _ = r.LastInsertId()
	for typ, st := range steps {
		if _, e = db.Exec("INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,?, ?,1,?)", runID, mediaID, generation, typ, st); e != nil {
			t.Fatal(e)
		}
	}
	return
}
func aggregateCall(t *testing.T, db *sql.DB, runID int64) {
	t.Helper()
	tx, e := db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = AggregateTx(context.Background(), tx, runID); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
}
func mediaState(t *testing.T, db *sql.DB, id int64) (string, sql.NullTime, string) {
	var s, e string
	var p sql.NullTime
	if err := db.QueryRow("SELECT publication_state,published_at,publication_error FROM media WHERE id=?", id).Scan(&s, &p, &e); err != nil {
		t.Fatal(err)
	}
	return s, p, e
}
func TestAggregatePublishesWhenRequiredStepsDone(t *testing.T) {
	db, r, m := aggregateFixture(t, "processing", 1, map[string]string{"poster": "done", "scrape": "skipped"})
	aggregateCall(t, db, r)
	s, p, e := mediaState(t, db, m)
	if s != "published" || !p.Valid || e != "" {
		t.Fatalf("got %s %v %q", s, p, e)
	}
	aggregateCall(t, db, r)
	_, p2, _ := mediaState(t, db, m)
	if !p2.Valid || p2.Time != p.Time {
		t.Fatal("published_at changed")
	}
}
func TestAggregateInitialRequiredExhaustionFailsClosed(t *testing.T) {
	for _, stepType := range []string{"poster", "thumbnail", "encrypt"} {
		t.Run(stepType, func(t *testing.T) {
			db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{stepType: "failed"})
			if _, err := db.Exec(`UPDATE media_ingest_step SET attempts=max_attempts,last_error=? WHERE run_id=?`, stepType+" exhausted", runID); err != nil {
				t.Fatal(err)
			}

			aggregateCall(t, db, runID)

			var runState string
			if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runState); err != nil {
				t.Fatal(err)
			}
			state, publishedAt, publicationError := mediaState(t, db, mediaID)
			if runState != "failed" || state != "failed" || publishedAt.Valid || publicationError == "" {
				t.Fatalf("run=%s media=%s published_at=%v error=%q", runState, state, publishedAt, publicationError)
			}
		})
	}
}

func TestAggregateNonPreservingRequiredFailureClearsPriorPublishedAt(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{"encrypt": "failed"})
	if _, err := db.Exec(`UPDATE media SET publication_state='processing',published_at='2026-07-01 02:03:04' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET attempts=max_attempts,last_error='encrypt exhausted' WHERE run_id=?`, runID); err != nil {
		t.Fatal(err)
	}

	aggregateCall(t, db, runID)

	state, publishedAt, _ := mediaState(t, db, mediaID)
	if state != "failed" || publishedAt.Valid {
		t.Fatalf("media=%s published_at=%v, want failed with null publish time", state, publishedAt)
	}
}

func TestAggregateRepairRequiredFailureDegradesAndPreservesPublication(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{"poster": "failed"})
	const priorPublishedAt = "2026-07-01 02:03:04"
	if _, err := db.Exec(`UPDATE media_ingest_run SET preserve_visibility=1 WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET publication_state='published',published_at=? WHERE id=?`, priorPublishedAt, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET attempts=max_attempts,last_error='poster exhausted' WHERE run_id=?`, runID); err != nil {
		t.Fatal(err)
	}

	aggregateCall(t, db, runID)

	var runState string
	if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	state, publishedAt, publicationError := mediaState(t, db, mediaID)
	if runState != "degraded" || state != "degraded" || !publishedAt.Valid || publishedAt.Time.Format("2006-01-02 15:04:05") != priorPublishedAt || publicationError != "poster exhausted" {
		t.Fatalf("run=%s media=%s published_at=%v error=%q", runState, state, publishedAt, publicationError)
	}
}

func TestAggregateRequiredPendingRespectsRepairVisibility(t *testing.T) {
	for _, tc := range []struct {
		name, initial, want string
		preserve            int
		wantPublishedAt     bool
	}{
		{name: "initial ingest hidden", initial: "processing", want: "processing"},
		{name: "repair remains visible", initial: "published", want: "published", preserve: 1, wantPublishedAt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{"poster": "waiting"})
			if _, err := db.Exec(`UPDATE media_ingest_run SET preserve_visibility=? WHERE id=?`, tc.preserve, runID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media SET publication_state=?,published_at=CASE WHEN ? THEN '2026-07-01 02:03:04' ELSE NULL END WHERE id=?`, tc.initial, tc.wantPublishedAt, mediaID); err != nil {
				t.Fatal(err)
			}

			aggregateCall(t, db, runID)

			state, publishedAt, _ := mediaState(t, db, mediaID)
			if state != tc.want || publishedAt.Valid != tc.wantPublishedAt {
				t.Fatalf("media=%s published_at=%v", state, publishedAt)
			}
		})
	}
}
func TestAggregateExplicitCancellationIntentControlsVisibility(t *testing.T) {
	for _, tc := range []struct {
		name            string
		preserve        int
		wantMedia       string
		wantPublishedAt bool
		wantError       string
	}{
		{name: "initial cancellation hides media", wantMedia: "cancelled", wantError: "scan_cancelled"},
		{name: "repair cancellation remains visible", preserve: 1, wantMedia: "degraded", wantPublishedAt: true, wantError: "admin_cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, runID, mediaID := aggregateFixture(t, "cancelled", 1, map[string]string{"poster": "cancelled"})
			reason := tc.wantError
			if _, err := db.Exec(`UPDATE media_ingest_run SET preserve_visibility=?,terminal_reason=?,error_message='original error',finished_at='2026-07-01 01:02:03' WHERE id=?`, tc.preserve, reason, runID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media SET publication_state='published',published_at=CASE WHEN ?=1 THEN '2026-06-01 01:02:03' ELSE '2026-06-01 01:02:03' END WHERE id=?`, tc.preserve, mediaID); err != nil {
				t.Fatal(err)
			}

			aggregateCall(t, db, runID)

			var runState, terminalReason, runError, finished string
			if err := db.QueryRow(`SELECT status,terminal_reason,error_message,strftime('%Y-%m-%d %H:%M:%S',finished_at) FROM media_ingest_run WHERE id=?`, runID).Scan(&runState, &terminalReason, &runError, &finished); err != nil {
				t.Fatal(err)
			}
			state, publishedAt, publicationError := mediaState(t, db, mediaID)
			if runState != "cancelled" || terminalReason != reason || runError != "original error" || finished != "2026-07-01 01:02:03" {
				t.Fatalf("run=%s reason=%q error=%q finished=%q", runState, terminalReason, runError, finished)
			}
			if state != tc.wantMedia || publishedAt.Valid != tc.wantPublishedAt || publicationError != tc.wantError {
				t.Fatalf("media=%s published_at=%v error=%q", state, publishedAt, publicationError)
			}
		})
	}
}

func TestAggregateRepairCancellationUsesSamePrioritizedErrorForRunAndMedia(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		steps                    map[string]string
		cancelError, failedError string
		want                     string
	}{
		{name: "cancelled error", steps: map[string]string{"poster": "cancelled"}, cancelError: "poster cancelled", want: "poster cancelled"},
		{name: "failed has priority", steps: map[string]string{"poster": "cancelled", "encrypt": "failed"}, cancelError: "poster cancelled", failedError: "encrypt failed", want: "encrypt failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, runID, mediaID := aggregateFixture(t, "processing", 1, tc.steps)
			if _, err := db.Exec(`UPDATE media_ingest_run SET preserve_visibility=1 WHERE id=?`, runID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, mediaID); err != nil {
				t.Fatal(err)
			}
			if tc.cancelError != "" {
				if _, err := db.Exec(`UPDATE media_ingest_step SET last_error=? WHERE run_id=? AND status='cancelled'`, tc.cancelError, runID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failedError != "" {
				if _, err := db.Exec(`UPDATE media_ingest_step SET last_error=? WHERE run_id=? AND status='failed'`, tc.failedError, runID); err != nil {
					t.Fatal(err)
				}
			}
			aggregateCall(t, db, runID)
			var runStatus, runError string
			if err := db.QueryRow(`SELECT status,error_message FROM media_ingest_run WHERE id=?`, runID).Scan(&runStatus, &runError); err != nil {
				t.Fatal(err)
			}
			mediaStatus, _, mediaError := mediaState(t, db, mediaID)
			if runStatus != "degraded" || mediaStatus != "degraded" || runError != tc.want || mediaError != tc.want {
				t.Fatalf("run=%s/%q media=%s/%q", runStatus, runError, mediaStatus, mediaError)
			}
		})
	}
}

func TestAggregateRequiredCancellationWithoutRunIntentIsFailure(t *testing.T) {
	for _, tc := range []struct {
		name, wantRun, wantMedia string
		preserve                 int
	}{{name: "initial", wantRun: "failed", wantMedia: "failed"}, {name: "repair", preserve: 1, wantRun: "degraded", wantMedia: "degraded"}} {
		t.Run(tc.name, func(t *testing.T) {
			db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{"poster": "cancelled"})
			if _, err := db.Exec(`UPDATE media_ingest_run SET preserve_visibility=? WHERE id=?`, tc.preserve, runID); err != nil {
				t.Fatal(err)
			}
			if tc.preserve == 1 {
				if _, err := db.Exec(`UPDATE media SET publication_state='published',published_at='2026-06-01 01:02:03' WHERE id=?`, mediaID); err != nil {
					t.Fatal(err)
				}
			}
			aggregateCall(t, db, runID)
			var runState string
			if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runState); err != nil {
				t.Fatal(err)
			}
			mediaState, _, _ := mediaState(t, db, mediaID)
			if runState != tc.wantRun || mediaState != tc.wantMedia {
				t.Fatalf("run=%s media=%s", runState, mediaState)
			}
		})
	}
}

func TestAggregateOptionalFailureDoesNotBlock(t *testing.T) {
	db, r, m := aggregateFixture(t, "processing", 1, map[string]string{"poster": "done", "preview": "failed"})
	db.Exec("UPDATE media_ingest_step SET required=0 WHERE step_type='preview'")
	aggregateCall(t, db, r)
	s, _, _ := mediaState(t, db, m)
	if s != "published" {
		t.Fatal(s)
	}
}
func TestAggregateStaleGenerationCannotPublish(t *testing.T) {
	db, r, m := aggregateFixture(t, "processing", 1, map[string]string{"poster": "done"})
	db.Exec("UPDATE media SET ingest_generation=2,publication_state='processing' WHERE id=?", m)
	aggregateCall(t, db, r)
	s, _, _ := mediaState(t, db, m)
	if s != "processing" {
		t.Fatal(s)
	}
}
func TestRetryDegradedStepKeepsVisible(t *testing.T) {
	db, r, m := aggregateFixture(t, "degraded", 1, map[string]string{"poster": "failed"})
	db.Exec("UPDATE media SET publication_state='degraded',publication_error='poster exhausted' WHERE id=?", m)
	db.Exec("UPDATE media_ingest_step SET attempts=max_attempts,last_error='poster exhausted' WHERE run_id=?", r)
	db.Exec("INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts) SELECT media_id,run_id,id,generation,'poster','failed',max_attempts,max_attempts FROM media_ingest_step WHERE run_id=?", r)
	if _, err := RetryDegradedRuns(context.Background(), db, 1); err != nil {
		t.Fatal(err)
	}
	s, _, e := mediaState(t, db, m)
	if s != "degraded" || e != "poster exhausted" {
		t.Fatalf("got %s %q", s, e)
	}
	var rs, ss, qs string
	db.QueryRow("SELECT status FROM media_ingest_run WHERE id=?", r).Scan(&rs)
	db.QueryRow("SELECT status FROM media_ingest_step WHERE run_id=?", r).Scan(&ss)
	db.QueryRow("SELECT status FROM post_ingest_task WHERE ingest_run_id=?", r).Scan(&qs)
	if rs != "degraded" || ss != "waiting" || qs != "waiting" {
		t.Fatalf("states run=%s step=%s queue=%s", rs, ss, qs)
	}
}
func TestRetryDegradedRunsRequeuesExhaustedRequiredStep(t *testing.T) {
	db, r, _ := aggregateFixture(t, "degraded", 1, map[string]string{"poster": "failed"})
	db.Exec("UPDATE media_ingest_step SET attempts=max_attempts WHERE run_id=?", r)
	if _, err := db.Exec("INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts) SELECT media_id,run_id,id,generation,'poster','failed',max_attempts,max_attempts FROM media_ingest_step WHERE run_id=?", r); err != nil {
		t.Fatal(err)
	}
	var calledAt int64
	if err := db.QueryRow("SELECT unixepoch('now')").Scan(&calledAt); err != nil {
		t.Fatal(err)
	}
	n, err := RetryDegradedRuns(context.Background(), db, 1)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var s string
	db.QueryRow("SELECT status FROM post_ingest_task WHERE ingest_run_id=?", r).Scan(&s)
	if s != "waiting" {
		t.Fatal(s)
	}
	var stepAvailable, queueAvailable int64
	if err := db.QueryRow("SELECT unixepoch(available_at) FROM media_ingest_step WHERE run_id=?", r).Scan(&stepAvailable); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT unixepoch(available_at) FROM post_ingest_task WHERE ingest_run_id=?", r).Scan(&queueAvailable); err != nil {
		t.Fatal(err)
	}
	const maxDegradedRetryDelaySeconds = 5 * 60
	if stepAvailable <= calledAt || stepAvailable > calledAt+maxDegradedRetryDelaySeconds {
		t.Fatalf("step available_at=%d, want > %d and <= %d", stepAvailable, calledAt, calledAt+maxDegradedRetryDelaySeconds)
	}
	if queueAvailable != stepAvailable {
		t.Fatalf("step and queue available_at differ: %d vs %d", stepAvailable, queueAvailable)
	}
	if n, err := RetryDegradedRuns(context.Background(), db, 1); err != nil || n != 0 {
		t.Fatalf("second retry n=%d err=%v, want idempotent no-op", n, err)
	}
	var stepAvailableAgain, queueAvailableAgain int64
	db.QueryRow("SELECT unixepoch(available_at) FROM media_ingest_step WHERE run_id=?", r).Scan(&stepAvailableAgain)
	db.QueryRow("SELECT unixepoch(available_at) FROM post_ingest_task WHERE ingest_run_id=?", r).Scan(&queueAvailableAgain)
	if stepAvailableAgain != stepAvailable || queueAvailableAgain != queueAvailable {
		t.Fatalf("retry window changed availability: step %d/%d queue %d/%d", stepAvailableAgain, stepAvailable, queueAvailableAgain, queueAvailable)
	}
}

func TestRetryDegradedRunsRequeuesLinkedScrapeStepAtomically(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "degraded", 1, map[string]string{"scrape": "failed"})
	var stepID int64
	if err := db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=?`, runID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET attempts=max_attempts,last_error='scrape exhausted',lease_owner='old',lease_until='2020-01-01',started_at='2020-01-01',finished_at='2020-01-01',available_at='2020-01-01' WHERE id=?`, stepID); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO scrape_task(media_id,ingest_run_id,ingest_step_id,generation,status,fail_count,message,available_at,lease_owner,lease_until,finished_at) VALUES(?,?,?,1,'failed',3,'scrape failed','2020-01-01','old','2020-01-01','2020-01-01')`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := result.LastInsertId()
	var calledAt int64
	if err := db.QueryRow(`SELECT unixepoch('now')`).Scan(&calledAt); err != nil {
		t.Fatal(err)
	}
	if n, err := RetryDegradedRuns(context.Background(), db, 1); err != nil || n != 1 {
		t.Fatalf("retry=(%d,%v)", n, err)
	}
	var status, lastError string
	var attempts int
	var stepAvailable, taskAvailable int64
	if err := db.QueryRow(`SELECT status,attempts,last_error,unixepoch(available_at) FROM media_ingest_step WHERE id=?`, stepID).Scan(&status, &attempts, &lastError, &stepAvailable); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" || attempts != 0 || lastError != "" {
		t.Fatalf("step=%s/%d/%q", status, attempts, lastError)
	}
	if err := db.QueryRow(`SELECT status,unixepoch(available_at) FROM scrape_task WHERE id=?`, taskID).Scan(&status, &taskAvailable); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" || stepAvailable != taskAvailable || stepAvailable <= calledAt || stepAvailable > calledAt+300 {
		t.Fatalf("availability=%d/%d called=%d", stepAvailable, taskAvailable, calledAt)
	}
	if n, err := RetryDegradedRuns(context.Background(), db, 1); err != nil || n != 0 {
		t.Fatalf("second retry=(%d,%v)", n, err)
	}
	var again int64
	if err := db.QueryRow(`SELECT unixepoch(available_at) FROM media_ingest_step WHERE id=?`, stepID).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again != stepAvailable {
		t.Fatalf("availability changed: %d/%d", again, stepAvailable)
	}
}

func TestAggregateRequiredTerminalBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		required  []string
		optional  []string
		mutate    func(*testing.T, *sql.DB, int64)
		wantRun   string
		wantMedia string
	}{
		{name: "required cancelled", required: []string{"cancelled"}, wantRun: "failed", wantMedia: "failed"},
		{name: "required failed below max", required: []string{"failed"}, mutate: func(t *testing.T, db *sql.DB, runID int64) {
			if _, err := db.Exec(`UPDATE media_ingest_step SET attempts=1,max_attempts=3 WHERE run_id=?`, runID); err != nil {
				t.Fatal(err)
			}
		}, wantRun: "failed", wantMedia: "failed"},
		{name: "optional cancelled", required: []string{"done"}, optional: []string{"cancelled"}, wantRun: "published", wantMedia: "published"},
		{name: "zero required", optional: []string{"cancelled"}, wantRun: "published", wantMedia: "published"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{})
			for i, status := range tc.required {
				if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,?,1,?)`, runID, mediaID, []string{"poster", "preview"}[i], status); err != nil {
					t.Fatal(err)
				}
			}
			for i, status := range tc.optional {
				if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,?,0,?)`, runID, mediaID, []string{"subtitle", "atrack"}[i], status); err != nil {
					t.Fatal(err)
				}
			}
			if tc.mutate != nil {
				tc.mutate(t, db, runID)
			}
			aggregateCall(t, db, runID)
			var runState string
			if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runState); err != nil {
				t.Fatal(err)
			}
			mediaState, _, _ := mediaState(t, db, mediaID)
			if runState != tc.wantRun || mediaState != tc.wantMedia {
				t.Fatalf("run=%s media=%s want %s/%s", runState, mediaState, tc.wantRun, tc.wantMedia)
			}
		})
	}
}

func TestRetryDegradedRunsOnlyRequeuesLinkedExhaustedRequiredSteps(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "degraded", 1, map[string]string{})
	type item struct {
		typ, status             string
		required, attempts, max int
	}
	items := []item{{"poster", "failed", 1, 3, 3}, {"preview", "failed", 0, 3, 3}, {"subtitle", "failed", 1, 1, 3}, {"atrack", "failed", 1, 3, 3}}
	ids := map[string][2]int64{}
	for _, it := range items {
		res, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,max_attempts,available_at,last_error) VALUES(?,?,1,?,?,?,?,?,'2040-01-01 00:00:00','original')`, runID, mediaID, it.typ, it.required, it.status, it.attempts, it.max)
		if err != nil {
			t.Fatal(err)
		}
		stepID, _ := res.LastInsertId()
		res, err = db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,available_at,last_error) VALUES(?,?,?,1,?,'failed',?,?,'2040-01-01 00:00:00','original')`, mediaID, runID, stepID, it.typ, it.attempts, it.max)
		if err != nil {
			t.Fatal(err)
		}
		queueID, _ := res.LastInsertId()
		ids[it.typ] = [2]int64{stepID, queueID}
	}
	// This required exhausted step is deliberately unlinked and must remain untouched.
	if _, err := db.Exec(`UPDATE post_ingest_task SET ingest_step_id=NULL WHERE id=?`, ids["atrack"][1]); err != nil {
		t.Fatal(err)
	}
	if n, err := RetryDegradedRuns(context.Background(), db, 1); err != nil || n != 1 {
		t.Fatalf("retry=(%d,%v)", n, err)
	}
	for _, typ := range []string{"poster", "preview", "subtitle", "atrack"} {
		var ss, qs, se, qe string
		var sa, qa int
		pair := ids[typ]
		if err := db.QueryRow(`SELECT status,attempts,last_error FROM media_ingest_step WHERE id=?`, pair[0]).Scan(&ss, &sa, &se); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT status,attempts,last_error FROM post_ingest_task WHERE id=?`, pair[1]).Scan(&qs, &qa, &qe); err != nil {
			t.Fatal(err)
		}
		if typ == "poster" {
			if ss != "waiting" || qs != "waiting" || sa != 0 || qa != 0 || se != "" || qe != "" {
				t.Fatalf("poster not reset: %s/%d/%q %s/%d/%q", ss, sa, se, qs, qa, qe)
			}
			continue
		}
		if ss != "failed" || qs != "failed" || se != "original" || qe != "original" {
			t.Fatalf("%s changed: %s/%d/%q %s/%d/%q", typ, ss, sa, se, qs, qa, qe)
		}
	}
}

func TestRetryDegradedRunsRequeuesLinkedPrepareWithFutureAvailability(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "degraded", 1, map[string]string{})
	res, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error) VALUES(?,?,1,'prepare',1,'failed',3,3,'prepare failed')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO transcode_task(file_id,status,task_type,media_id,ingest_run_id,ingest_step_id,generation) VALUES('f','failed','pretranscode',?,?,?,1)`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	if _, err = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,error_message) VALUES(?,1,'720p','failed','prepare failed')`, taskID); err != nil {
		t.Fatal(err)
	}
	called := time.Now().Unix()
	n, err := RetryDegradedRuns(context.Background(), db, 1)
	if err != nil || n != 1 {
		t.Fatalf("retry=%d,%v", n, err)
	}
	var ss, ts, js string
	var sa, ja int64
	if err = db.QueryRow(`SELECT status,unixepoch(available_at) FROM media_ingest_step WHERE id=?`, stepID).Scan(&ss, &sa); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, taskID).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT status,unixepoch(available_at) FROM pretranscode_rendition_job WHERE task_id=?`, taskID).Scan(&js, &ja); err != nil {
		t.Fatal(err)
	}
	if ss != "waiting" || ts != "waiting" || js != "waiting" || sa != ja || ja <= called || ja > called+300 {
		t.Fatalf("step=%s/%d task=%s job=%s/%d called=%d", ss, sa, ts, js, ja, called)
	}
	if n, err = RetryDegradedRuns(context.Background(), db, 1); err != nil || n != 0 {
		t.Fatalf("second retry=%d,%v", n, err)
	}
}
