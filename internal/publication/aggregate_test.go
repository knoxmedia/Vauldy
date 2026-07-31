package publication

import (
	"context"
	"database/sql"
	"knox-media/internal/store"
	"strings"
	"testing"
	"unicode/utf8"
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
	if runState != "degraded" || state != "degraded" || !publishedAt.Valid || publishedAt.Time.Format("2006-01-02 15:04:05") != priorPublishedAt || publicationError != "poster: poster exhausted" {
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

func TestAggregateRequiredFailureCancelsBlockedWaitingRequired(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{"poster": "failed", "encrypt": "waiting"})
	if _, err := db.Exec(`UPDATE media_ingest_run SET preserve_visibility=1,reason='repair' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET publication_state='published',published_at='2026-07-01 02:03:04' WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET attempts=max_attempts,last_error='poster exhausted' WHERE run_id=? AND step_type='poster'`, runID); err != nil {
		t.Fatal(err)
	}
	var encryptStep int64
	if err := db.QueryRow(`SELECT id FROM media_ingest_step WHERE run_id=? AND step_type='encrypt'`, runID).Scan(&encryptStep); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,max_attempts) VALUES(?,?,?,1,'encrypt','waiting',3)`, mediaID, runID, encryptStep); err != nil {
		t.Fatal(err)
	}

	aggregateCall(t, db, runID)

	var runState, encryptStepStatus, encryptQueueStatus string
	if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, encryptStep).Scan(&encryptStepStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE ingest_step_id=?`, encryptStep).Scan(&encryptQueueStatus); err != nil {
		t.Fatal(err)
	}
	state, publishedAt, _ := mediaState(t, db, mediaID)
	if runState != "degraded" || state != "degraded" || !publishedAt.Valid || encryptStepStatus != "cancelled" || encryptQueueStatus != "cancelled" {
		t.Fatalf("run=%s media=%s encrypt step/queue=%s/%s", runState, state, encryptStepStatus, encryptQueueStatus)
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
		{name: "cancelled error", steps: map[string]string{"poster": "cancelled"}, cancelError: "poster cancelled", want: "poster: poster cancelled"},
		{name: "failed has priority", steps: map[string]string{"poster": "cancelled", "encrypt": "failed"}, cancelError: "poster cancelled", failedError: "encrypt failed", want: "poster: poster cancelled; encrypt: encrypt failed"},
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

func TestAggregateDiagnosticSelectionSkipsEmptyHigherPriorityErrors(t *testing.T) {
	for _, tc := range []struct {
		name, failedError, cancelledError, want string
	}{
		{name: "cancelled detail after empty failed", failedError: "", cancelledError: "cancel detail", want: "poster: cancel detail"},
		{name: "fallback when all empty", failedError: "", cancelledError: "", want: "required step exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{"encrypt": "failed", "poster": "cancelled"})
			if _, err := db.Exec(`UPDATE media_ingest_run SET preserve_visibility=1 WHERE id=?`, runID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=?`, mediaID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE media_ingest_step SET last_error=CASE status WHEN 'failed' THEN ? ELSE ? END WHERE run_id=?`, tc.failedError, tc.cancelledError, runID); err != nil {
				t.Fatal(err)
			}
			aggregateCall(t, db, runID)
			var runError string
			if err := db.QueryRow(`SELECT error_message FROM media_ingest_run WHERE id=?`, runID).Scan(&runError); err != nil {
				t.Fatal(err)
			}
			_, _, mediaError := mediaState(t, db, mediaID)
			if runError != tc.want || mediaError != tc.want {
				t.Fatalf("run=%q media=%q", runError, mediaError)
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
func TestTerminalDegradedRunRemainsImmutableWithoutAdminReplacement(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "degraded", 1, map[string]string{"poster": "failed"})
	if _, err := db.Exec(`UPDATE media SET publication_state='degraded',publication_error='poster exhausted' WHERE id=?; UPDATE media_ingest_run SET preserve_visibility=1 WHERE id=?; UPDATE media_ingest_step SET max_attempts=3,attempts=3,last_error='poster exhausted',finished_at='2026-07-01 01:02:03' WHERE run_id=?; INSERT INTO post_ingest_task(media_id,ingest_run_id,ingest_step_id,generation,task_type,status,attempts,max_attempts,last_error,finished_at) SELECT media_id,run_id,id,generation,'poster','failed',3,3,'poster exhausted','2026-07-01 01:02:03' FROM media_ingest_step WHERE run_id=?`, mediaID, runID, runID, runID); err != nil {
		t.Fatal(err)
	}
	aggregateCall(t, db, runID)
	var runState, stepState, taskState, mediaState, mediaError string
	var stepAttempts, taskAttempts int
	if err := db.QueryRow(`SELECT status FROM media_ingest_run WHERE id=?`, runID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,attempts FROM media_ingest_step WHERE run_id=?`, runID).Scan(&stepState, &stepAttempts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,attempts FROM post_ingest_task WHERE ingest_run_id=?`, runID).Scan(&taskState, &taskAttempts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_state,publication_error FROM media WHERE id=?`, mediaID).Scan(&mediaState, &mediaError); err != nil {
		t.Fatal(err)
	}
	if runState != "degraded" || stepState != "failed" || taskState != "failed" || stepAttempts != 3 || taskAttempts != 3 || mediaState != "degraded" || mediaError != "poster: poster exhausted" {
		t.Fatalf("run=%s step=%s/%d task=%s/%d media=%s/%q", runState, stepState, stepAttempts, taskState, taskAttempts, mediaState, mediaError)
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

func TestAggregateSecureRepairFailureRetainsTimestampButFailsHidden(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{"encrypt": "failed"})
	const original = "2026-07-01 02:03:04"
	if _, err := db.Exec(`UPDATE media SET publication_state='processing',published_at=? WHERE id=?`, original, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_run SET reason='repair',preserve_visibility=0 WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET attempts=max_attempts,last_error='encrypt exhausted' WHERE run_id=?`, runID); err != nil {
		t.Fatal(err)
	}
	aggregateCall(t, db, runID)
	state, publishedAt, _ := mediaState(t, db, mediaID)
	if state != "failed" || !publishedAt.Valid || publishedAt.Time.Format("2006-01-02 15:04:05") != original {
		t.Fatalf("state=%s published=%v", state, publishedAt)
	}
}

func aggregateErrors(t *testing.T, db *sql.DB, runID, mediaID int64) (string, string) {
	t.Helper()
	var runError, mediaError string
	if err := db.QueryRow(`SELECT error_message FROM media_ingest_run WHERE id=?`, runID).Scan(&runError); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_error FROM media WHERE id=?`, mediaID).Scan(&mediaError); err != nil {
		t.Fatal(err)
	}
	return runError, mediaError
}

func TestAggregateRequiredDiagnosticsDeterministicAndComplete(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{})
	steps := []struct{ typ, status, detail string }{
		{"prepare", "failed", "prepare bad"},
		{"encrypt", "cancelled", "encrypt bad"},
		{"preview", "failed", "preview bad"},
		{"thumbnail", "failed", "thumb bad"},
		{"poster", "cancelled", "poster bad"},
	}
	for _, step := range steps {
		if _, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,last_error) VALUES(?,?,1,?,1,?,?)`, runID, mediaID, step.typ, step.status, step.detail); err != nil {
			t.Fatal(err)
		}
	}

	aggregateCall(t, db, runID)

	want := "poster: poster bad; thumbnail: thumb bad; encrypt: encrypt bad; prepare: prepare bad; preview: preview bad"
	runError, mediaError := aggregateErrors(t, db, runID, mediaID)
	if runError != want || mediaError != want {
		t.Fatalf("run=%q media=%q want=%q", runError, mediaError, want)
	}
}

func TestAggregateRequiredDiagnosticsSkipEmptyAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, posterError, encryptError, want string
	}{
		{name: "skip empty", encryptError: "seal failed", want: "encrypt: seal failed"},
		{name: "fallback", want: "required step exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{"poster": "failed", "encrypt": "cancelled"})
			if _, err := db.Exec(`UPDATE media_ingest_step SET last_error=CASE step_type WHEN 'poster' THEN ? ELSE ? END WHERE run_id=?`, tc.posterError, tc.encryptError, runID); err != nil {
				t.Fatal(err)
			}
			aggregateCall(t, db, runID)
			runError, mediaError := aggregateErrors(t, db, runID, mediaID)
			if runError != tc.want || mediaError != tc.want {
				t.Fatalf("run=%q media=%q want=%q", runError, mediaError, tc.want)
			}
		})
	}
}

func TestAggregateRequiredDiagnosticsUTF8Bounded(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "processing", 1, map[string]string{"poster": "failed", "encrypt": "failed"})
	long := strings.Repeat("\u754c", 700)
	if _, err := db.Exec(`UPDATE media_ingest_step SET last_error=? WHERE run_id=?`, long, runID); err != nil {
		t.Fatal(err)
	}

	aggregateCall(t, db, runID)

	runError, mediaError := aggregateErrors(t, db, runID, mediaID)
	if runError != mediaError {
		t.Fatalf("run/media diagnostics differ")
	}
	if len(runError) > 1500 || !utf8.ValidString(runError) || !strings.HasSuffix(runError, "...") {
		t.Fatalf("bytes=%d valid=%v suffix=%q", len(runError), utf8.ValidString(runError), runError[len(runError)-3:])
	}
}

func TestAggregateExplicitCancellationPreservesReasonAndRunError(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "cancelled", 1, map[string]string{"poster": "cancelled", "encrypt": "failed"})
	if _, err := db.Exec(`UPDATE media_ingest_run SET terminal_reason='operator requested',error_message='explicit cancellation detail' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_step SET last_error='worker detail' WHERE run_id=?`, runID); err != nil {
		t.Fatal(err)
	}

	aggregateCall(t, db, runID)

	var terminalReason string
	if err := db.QueryRow(`SELECT terminal_reason FROM media_ingest_run WHERE id=?`, runID).Scan(&terminalReason); err != nil {
		t.Fatal(err)
	}
	runError, mediaError := aggregateErrors(t, db, runID, mediaID)
	if terminalReason != "operator requested" || runError != "explicit cancellation detail" || mediaError != "operator requested" {
		t.Fatalf("reason=%q run=%q media=%q", terminalReason, runError, mediaError)
	}
}

func TestAggregateSupersededRunIsNoOp(t *testing.T) {
	db, runID, mediaID := aggregateFixture(t, "cancelled", 1, map[string]string{"poster": "failed"})
	if _, err := db.Exec(`UPDATE media SET publication_state='published',publication_error='current generation',ingest_generation=2 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media_ingest_run SET terminal_reason='superseded_by_policy_v2',error_message='preserve me',superseded_by_generation=2,superseded_at=CURRENT_TIMESTAMP WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}

	aggregateCall(t, db, runID)

	var runError string
	if err := db.QueryRow(`SELECT error_message FROM media_ingest_run WHERE id=?`, runID).Scan(&runError); err != nil {
		t.Fatal(err)
	}
	state, _, mediaError := mediaState(t, db, mediaID)
	if runError != "preserve me" || state != "published" || mediaError != "current generation" {
		t.Fatalf("run=%q media=%s/%q", runError, state, mediaError)
	}
}
