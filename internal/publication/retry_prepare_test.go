package publication

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"knox-media/internal/store"
)

func seedOptionalPrepareRetry(t *testing.T, db *sql.DB) (mediaID, runID, stepID, taskID int64, snapshot string) {
	t.Helper()
	snapshot = `[{"rendition_id":0,"rendition_name":"360p","config_snapshot":{"preset":{"id":7},"rendition":{"id":11,"name":"360p"},"output_path":"immutable","priority":"normal"}},{"rendition_id":0,"rendition_name":"720p","config_snapshot":{"preset":{"id":7},"rendition":{"id":12,"name":"720p"},"output_path":"immutable","priority":"normal"}}]`
	lr, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('prepare-retry','video','/prepare')`)
	if err != nil {
		t.Fatal(err)
	}
	lid, _ := lr.LastInsertId()
	mr, err := db.Exec(`INSERT INTO media(library_id,file_id,file_type,publication_state,publication_error,ingest_generation,published_at) VALUES(?,'prepare-media','video','published','exact media error',1,'2026-07-01 01:02:03')`, lid)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ = mr.LastInsertId()
	rr, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,config_snapshot_json,error_message,finished_at) VALUES(?,1,'scan','published','{}','exact run error','2026-07-01 04:05:06')`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ = rr.LastInsertId()
	sr, err := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,attempts,max_attempts,last_error,lease_owner,lease_until,started_at,finished_at) VALUES(?,?,1,'prepare',0,'failed',3,3,'old step error','old-owner','2026-01-01','2026-01-01','2026-01-01')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ = sr.LastInsertId()
	tr, err := db.Exec(`INSERT INTO transcode_task(file_id,status,progress,error_message,task_type,media_id,ingest_run_id,ingest_step_id,generation,lease_owner,lease_until,started_at,completed_at) VALUES('prepare-media','failed',67,'old parent error','pretranscode',?,?,?,1,'old-owner','2026-01-01','2026-01-01','2026-01-01')`, mediaID, runID, stepID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ = tr.LastInsertId()
	if _, err = db.Exec(`INSERT INTO pretranscode_task_meta(task_id,preset_id,output_format,encryption_mode,priority,output_path,ingest_jobs_snapshot_json) VALUES(?,1,'hls','none','normal','immutable-root',?)`, taskID, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,progress,error_message,output_path,encoder_used,started_at,completed_at,config_snapshot_json) VALUES(?,NULL,'mutable-old','failed',88,'old rendition error','old-output','old-encoder','2026-01-01','2026-01-01','{}')`, taskID); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRetryOptionalPreparePreservesOutcomeAndReexpandsImmutableSnapshot(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "prepare-retry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mediaID, runID, stepID, taskID, snapshot := seedOptionalPrepareRetry(t, db)
	var cancelledTask int64
	var cancelledRound int
	err = RetryOptionalPrepare(context.Background(), db, OptionalPrepareRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: 42, Reason: "operator retry", CaptureActive: func(id int64, round int) func() { return func() { cancelledTask, cancelledRound = id, round } }}, NewCapabilityMatrix([]string{"prepare"}))
	if err != nil {
		t.Fatal(err)
	}
	var ms, me, mp, rs, re, rf, ss, se, ts, te, gotSnapshot string
	var gen, sa, tr int
	if err = db.QueryRow(`SELECT m.ingest_generation,m.publication_state,m.publication_error,m.published_at,r.status,r.error_message,r.finished_at,s.status,s.attempts,s.last_error,t.status,t.retry_round,COALESCE(t.error_message,''),pm.ingest_jobs_snapshot_json FROM media m JOIN media_ingest_run r ON r.media_id=m.id AND r.generation=m.ingest_generation JOIN media_ingest_step s ON s.run_id=r.id JOIN transcode_task t ON t.ingest_step_id=s.id JOIN pretranscode_task_meta pm ON pm.task_id=t.id WHERE m.id=?`, mediaID).Scan(&gen, &ms, &me, &mp, &rs, &re, &rf, &ss, &sa, &se, &ts, &tr, &te, &gotSnapshot); err != nil {
		t.Fatal(err)
	}
	if gen != 1 || ms != "published" || me != "exact media error" || mp != "2026-07-01T01:02:03Z" || rs != "published" || re != "exact run error" || rf != "2026-07-01T04:05:06Z" {
		t.Fatalf("outcome changed: %d %s %q %s / %s %q %s", gen, ms, me, mp, rs, re, rf)
	}
	if ss != "waiting" || sa != 0 || se != "" || ts != "waiting" || tr != 1 || te != "" || gotSnapshot != snapshot {
		t.Fatalf("retry state=%s/%d/%q parent=%s/%d/%q snapshotEqual=%v", ss, sa, se, ts, tr, te, gotSnapshot == snapshot)
	}
	rows, err := db.Query(`SELECT COALESCE(rendition_id,0),rendition_name,status,progress,COALESCE(error_message,''),COALESCE(output_path,''),retry_round FROM pretranscode_rendition_job WHERE task_id=? ORDER BY rendition_name`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, progress, round int
		var name, status, e, out string
		if err = rows.Scan(&id, &name, &status, &progress, &e, &out, &round); err != nil {
			t.Fatal(err)
		}
		got = append(got, name+":"+status)
		if progress != 0 || e != "" || out != "" || round != 1 {
			t.Fatalf("rendition not reset: %d %s %s %d %q %q round=%d", id, name, status, progress, e, out, round)
		}
	}
	if len(got) != 2 || got[0] != "360p:waiting" || got[1] != "720p:waiting" {
		t.Fatalf("jobs=%v", got)
	}
	var family, pqe, pse string
	var auditRound, previous int
	if err = db.QueryRow(`SELECT task_family,previous_attempts,previous_queue_error,previous_step_error,retry_round FROM media_ingest_optional_retry_audit WHERE step_id=?`, stepID).Scan(&family, &previous, &pqe, &pse, &auditRound); err != nil {
		t.Fatal(err)
	}
	if family != "prepare" || previous != 3 || pqe != "old parent error" || pse != "old step error" || auditRound != 1 {
		t.Fatalf("audit=%s/%d/%q/%q/%d", family, previous, pqe, pse, auditRound)
	}
	if cancelledTask != taskID || cancelledRound != 0 {
		t.Fatalf("cancel=%d/%d", cancelledTask, cancelledRound)
	}
	_ = runID
}

func TestRetryOptionalPrepareRejectsInvalidStatesAndConcurrency(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sql.DB, int64, int64, int64)
	}{
		{"required", func(db *sql.DB, _ int64, step int64, _ int64) {
			_, _ = db.Exec(`UPDATE media_ingest_step SET required=1 WHERE id=?`, step)
		}},
		{"nonterminal", func(db *sql.DB, _ int64, step int64, task int64) {
			_, _ = db.Exec(`UPDATE media_ingest_step SET status='running' WHERE id=?; UPDATE transcode_task SET status='running' WHERE id=?`, step, task)
		}},
		{"unexhausted", func(db *sql.DB, _ int64, step int64, _ int64) {
			_, _ = db.Exec(`UPDATE media_ingest_step SET attempts=2,max_attempts=3 WHERE id=?`, step)
		}},
		{"stale", func(db *sql.DB, media int64, _ int64, _ int64) {
			_, _ = db.Exec(`UPDATE media SET ingest_generation=2 WHERE id=?`, media)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "x.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			m, _, s, q, _ := seedOptionalPrepareRetry(t, db)
			tc.mutate(db, m, s, q)
			err = RetryOptionalPrepare(context.Background(), db, OptionalPrepareRetryRequest{MediaID: m, StepID: s, ActorID: 1, Reason: "test"}, NewCapabilityMatrix([]string{"prepare"}))
			if !errors.Is(err, ErrNoRetryableWork) {
				t.Fatalf("err=%v", err)
			}
		})
	}

	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "concurrent.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, _, s, _, _ := seedOptionalPrepareRetry(t, db)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- RetryOptionalPrepare(context.Background(), db, OptionalPrepareRetryRequest{MediaID: m, StepID: s, ActorID: 1, Reason: "race"}, NewCapabilityMatrix([]string{"prepare"}))
		}()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for e := range errs {
		if e == nil {
			ok++
		} else if !errors.Is(e, ErrNoRetryableWork) {
			t.Fatal(e)
		}
	}
	var audits int
	_ = db.QueryRow(`SELECT COUNT(*) FROM media_ingest_optional_retry_audit WHERE step_id=?`, s).Scan(&audits)
	if ok != 1 || audits != 1 {
		t.Fatalf("success=%d audits=%d", ok, audits)
	}
}

func TestRetryOptionalPrepareRejectsEmptyReasonAndUnavailableCapability(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "prepare-reject.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, _, s, _, _ := seedOptionalPrepareRetry(t, db)
	if err = RetryOptionalPrepare(context.Background(), db, OptionalPrepareRetryRequest{MediaID: m, StepID: s, ActorID: 1, Reason: "  "}, NewCapabilityMatrix([]string{"prepare"})); !errors.Is(err, ErrInvalidRetryReason) {
		t.Fatalf("empty reason err=%v", err)
	}
	if err = RetryOptionalPrepare(context.Background(), db, OptionalPrepareRetryRequest{MediaID: m, StepID: s, ActorID: 1, Reason: "retry"}, NewCapabilityMatrix(nil)); !errors.Is(err, ErrPrepareCapabilityUnavailable) {
		t.Fatalf("capability err=%v", err)
	}
	if err = RetryOptionalPrepare(context.Background(), db, OptionalPrepareRetryRequest{MediaID: m, StepID: s, ActorID: 1, Reason: "retry"}, nil); !errors.Is(err, ErrPrepareCapabilityUnavailable) {
		t.Fatalf("nil registry err=%v", err)
	}
}

func TestRetryOptionalPrepareCancelsOnlyAfterCommit(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "prepare-cancel-order.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, _, s, taskID, _ := seedOptionalPrepareRetry(t, db)
	var cancelOrder []string
	var cancelledRound int
	err = RetryOptionalPrepare(context.Background(), db, OptionalPrepareRetryRequest{
		MediaID: m, StepID: s, ActorID: 7, Reason: "ordered cancel",
		CaptureActive: func(id int64, round int) func() {
			if id != taskID || round != 0 {
				t.Fatalf("capture identity=%d/%d", id, round)
			}
			cancelOrder = append(cancelOrder, "capture")
			return func() {
				cancelOrder = append(cancelOrder, "cancel")
				cancelledRound = round
			}
		},
	}, NewCapabilityMatrix([]string{"prepare"}))
	if err != nil {
		t.Fatal(err)
	}
	var roundNow int
	if err = db.QueryRow(`SELECT retry_round FROM transcode_task WHERE id=?`, taskID).Scan(&roundNow); err != nil {
		t.Fatal(err)
	}
	if roundNow != 1 || cancelledRound != 0 || len(cancelOrder) != 2 || cancelOrder[0] != "capture" || cancelOrder[1] != "cancel" {
		t.Fatalf("round=%d cancelled=%d order=%v", roundNow, cancelledRound, cancelOrder)
	}
}
