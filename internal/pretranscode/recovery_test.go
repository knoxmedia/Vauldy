package pretranscode

import (
	"context"
	"testing"
)

func TestRecoverExpiredPrepareParentsResetsCurrentAndCancelsSuperseded(t *testing.T) {
	db := newTestDB(t)
	task, run, step, media := seedLinkedPrepareTerminal(t, db, 1)
	_, _ = db.Exec(`UPDATE transcode_task SET lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?`, task)
	_, _ = db.Exec(`UPDATE media_ingest_step SET lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?`, step)
	_, _ = db.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_name,status,lease_owner,lease_until)VALUES(?,'x','running','job',datetime(CURRENT_TIMESTAMP,'-1 second'))`, task)
	n, err := RecoverExpiredPrepareParents(context.Background(), db, 10)
	if err != nil || n != 1 {
		t.Fatalf("recover=%d/%v", n, err)
	}
	var ts, ss, js string
	_ = db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, task).Scan(&ts)
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, step).Scan(&ss)
	_ = db.QueryRow(`SELECT status FROM pretranscode_rendition_job WHERE task_id=?`, task).Scan(&js)
	if ts != "waiting" || ss != "waiting" || js != "waiting" {
		t.Fatalf("states=%s/%s/%s", ts, ss, js)
	}
	_, _ = db.Exec(`UPDATE transcode_task SET status='running',lease_owner='old',lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?;UPDATE media_ingest_step SET status='running',lease_owner='old',lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?;UPDATE pretranscode_rendition_job SET status='running' WHERE task_id=?;UPDATE media_ingest_run SET superseded_at=CURRENT_TIMESTAMP WHERE id=?`, task, step, task, run)
	n, err = RecoverExpiredPrepareParents(context.Background(), db, 10)
	if err != nil || n != 1 {
		t.Fatalf("stale recover=%d/%v", n, err)
	}
	_ = db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, task).Scan(&ts)
	_ = db.QueryRow(`SELECT status FROM media_ingest_step WHERE id=?`, step).Scan(&ss)
	if ts != "cancelled" || ss != "cancelled" {
		t.Fatalf("stale=%s/%s media=%d", ts, ss, media)
	}
}
func TestRecoverExpiredPrepareParentsLeavesUnexpired(t *testing.T) {
	db := newTestDB(t)
	task, _, _, _ := seedLinkedPrepareTerminal(t, db, 1)
	n, err := RecoverExpiredPrepareParents(context.Background(), db, 10)
	if err != nil || n != 0 {
		t.Fatalf("recover=%d/%v", n, err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, task).Scan(&status)
	if status != "running" {
		t.Fatal(status)
	}
}

func TestRecoverExpiredPrepareParentsRejectsGenerationMismatchWithoutSupersession(t *testing.T) {
	db := newTestDB(t)
	task, _, step, media := seedLinkedPrepareTerminal(t, db, 1)
	_, _ = db.Exec(`UPDATE transcode_task SET lease_until=datetime('now','-1 second') WHERE id=?;UPDATE media SET ingest_generation=2 WHERE id=?`, task, media)
	if _, err := RecoverExpiredPrepareParents(context.Background(), db, 10); err == nil {
		t.Fatal("expected diagnostic")
	}
	var ts, ss string
	_ = db.QueryRow(`SELECT t.status,s.status FROM transcode_task t JOIN media_ingest_step s ON s.id=? WHERE t.id=?`, step, task).Scan(&ts, &ss)
	if ts != "running" || ss != "running" {
		t.Fatalf("%s/%s", ts, ss)
	}
}
func TestRecoverExpiredSupersededPrepareAggregatesNoOp(t *testing.T) {
	db := newTestDB(t)
	task, run, step, _ := seedLinkedPrepareTerminal(t, db, 1)
	_, _ = db.Exec(`UPDATE transcode_task SET lease_until=datetime('now','-1 second') WHERE id=?;UPDATE media_ingest_run SET status='cancelled',superseded_at=CURRENT_TIMESTAMP WHERE id=?`, task, run)
	if n, err := RecoverExpiredPrepareParents(context.Background(), db, 10); err != nil || n != 1 {
		t.Fatalf("%d/%v", n, err)
	}
	var ts, ss, rs string
	_ = db.QueryRow(`SELECT t.status,s.status,r.status FROM transcode_task t JOIN media_ingest_step s ON s.id=? JOIN media_ingest_run r ON r.id=? WHERE t.id=?`, step, run, task).Scan(&ts, &ss, &rs)
	if ts != "cancelled" || ss != "cancelled" || rs != "cancelled" {
		t.Fatalf("%s/%s/%s", ts, ss, rs)
	}
}
func TestRecoverExpiredPrepareParentsRejectsIdentityDrift(t *testing.T) {
	for _, name := range []string{"step_relink", "owner_mismatch", "run_mismatch"} {
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			task, run, step, media := seedLinkedPrepareTerminal(t, db, 1)
			_, _ = db.Exec(`UPDATE transcode_task SET lease_until=datetime('now','-1 second') WHERE id=?`, task)
			switch name {
			case "owner_mismatch":
				_, _ = db.Exec(`UPDATE media_ingest_step SET lease_owner='other' WHERE id=?`, step)
			case "step_relink":
				r, e := db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status,lease_owner)VALUES(?,?,1,'scrape',0,'running','test-parent')`, run, media)
				if e != nil {
					t.Fatal(e)
				}
				other, _ := r.LastInsertId()
				_, e = db.Exec(`UPDATE transcode_task SET ingest_step_id=? WHERE id=?`, other, task)
				if e != nil {
					t.Fatal(e)
				}
			case "run_mismatch":
				_, e := db.Exec(`UPDATE media_ingest_step SET step_type='scrape' WHERE id=?`, step)
				if e != nil {
					t.Fatal(e)
				}
			}
			if _, err := RecoverExpiredPrepareParents(context.Background(), db, 10); err == nil {
				t.Fatal("expected identity diagnostic")
			}
			var status string
			_ = db.QueryRow(`SELECT status FROM transcode_task WHERE id=?`, task).Scan(&status)
			if status != "running" {
				t.Fatal(status)
			}
		})
	}
}
func TestCancellationCaptureRejectsReusedJobID(t *testing.T) {
	db := newTestDB(t)
	w := NewWorker(db, nil, "ffmpeg", t.TempDir(), 1, 1)
	oldCtx, oldCancel := context.WithCancel(context.Background())
	newCtx, newCancel := context.WithCancel(context.Background())
	defer oldCancel()
	defer newCancel()
	old := &activeRendition{taskID: 1, cancel: oldCancel, token: &struct{}{}}
	w.running[7] = old
	capture := w.CaptureRenditionCancellation(7)
	w.running[7] = &activeRendition{taskID: 2, cancel: newCancel, token: &struct{}{}}
	capture()
	select {
	case <-newCtx.Done():
		t.Fatal("reused job cancelled")
	default:
	}
	select {
	case <-oldCtx.Done():
		t.Fatal("stale handle invoked")
	default:
	}
}
