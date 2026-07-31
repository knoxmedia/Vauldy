package handler

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

func explicitPostIngestDB(t *testing.T) (*sql.DB, int64, int64) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lr, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('l','video','/tmp')`)
	if err != nil {
		t.Fatal(err)
	}
	lid, _ := lr.LastInsertId()
	mr, err := db.Exec(`INSERT INTO media(library_id,file_id) VALUES(?,'m')`, lid)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := mr.LastInsertId()
	sr, err := db.Exec(`INSERT INTO scan_task(library_id,status) VALUES(?,'running')`, lid)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := sr.LastInsertId()
	if _, err := db.Exec(`INSERT INTO preview_task(media_id,status,interval_sec,thumb_count,error_message) VALUES(?,'ready',9,7,NULL)`, mid); err != nil {
		t.Fatal(err)
	}
	return db, mid, sid
}

func TestEnqueueExplicitPostIngestStateMatrix(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial postingest.Status
		want    postingest.Status
		reset   bool
	}{
		{"waiting", postingest.StatusWaiting, postingest.StatusWaiting, false},
		{"running", postingest.StatusRunning, postingest.StatusRunning, false},
		{"done", postingest.StatusDone, postingest.StatusDone, false},
		{"failed", postingest.StatusFailed, postingest.StatusWaiting, true},
		{"cancelled", postingest.StatusCancelled, postingest.StatusWaiting, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mid, sid := explicitPostIngestDB(t)
			_, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts,lease_owner,lease_until,last_error) VALUES(?,?,'preview',?,3,'owner/token',datetime(CURRENT_TIMESTAMP,'+1 hour'),'old')`, mid, sid, tc.initial)
			if err != nil {
				t.Fatal(err)
			}
			resetCalls := 0
			reset := func(ctx context.Context, tx *sql.Tx) error {
				resetCalls++
				_, err := tx.ExecContext(ctx, `UPDATE preview_task SET status='waiting',error_message=NULL WHERE media_id=?`, mid)
				return err
			}
			if _, err := enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskPreview, false, reset, nil); err != nil {
				t.Fatal(err)
			}
			if (resetCalls == 1) != tc.reset {
				t.Fatalf("reset calls=%d want reset=%v", resetCalls, tc.reset)
			}
			var domain string
			if err := db.QueryRow(`SELECT status FROM preview_task WHERE media_id=?`, mid).Scan(&domain); err != nil {
				t.Fatal(err)
			}
			wantDomain := "ready"
			if tc.reset {
				wantDomain = "waiting"
			}
			if domain != wantDomain {
				t.Fatalf("domain=%s want %s", domain, wantDomain)
			}
			var status postingest.Status
			var scan, owner, lease sql.NullString
			var attempts int
			if err := db.QueryRow(`SELECT status,CAST(scan_task_id AS TEXT),attempts,lease_owner,lease_until FROM post_ingest_task WHERE media_id=? AND task_type='preview'`, mid).Scan(&status, &scan, &attempts, &owner, &lease); err != nil {
				t.Fatal(err)
			}
			if status != tc.want {
				t.Fatalf("status=%s want %s", status, tc.want)
			}
			if tc.reset {
				if scan.Valid || owner.Valid || lease.Valid || attempts != 0 {
					t.Fatalf("retry did not reset: scan=%v attempts=%d owner=%v lease=%v", scan, attempts, owner, lease)
				}
			} else if scan.String == "" || owner.String != "owner/token" || !lease.Valid || attempts != 3 {
				t.Fatalf("idempotent state changed: scan=%v attempts=%d owner=%v lease=%v", scan, attempts, owner, lease)
			}
		})
	}
}

func TestEnqueueExplicitPostIngestCreatesMissingRow(t *testing.T) {
	db, mid, _ := explicitPostIngestDB(t)
	calls := 0
	if _, err := enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskPreview, false, func(context.Context, *sql.Tx) error { calls++; return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("reset calls=%d", calls)
	}
	var status postingest.Status
	var scan sql.NullInt64
	if err := db.QueryRow(`SELECT status,scan_task_id FROM post_ingest_task WHERE media_id=? AND task_type='preview'`, mid).Scan(&status, &scan); err != nil {
		t.Fatal(err)
	}
	if status != postingest.StatusWaiting || scan.Valid {
		t.Fatalf("status=%s scan=%v", status, scan)
	}
}

func TestEnqueueExplicitPostIngestCreatesMissingAlignedDomainRow(t *testing.T) {
	for _, tc := range []struct {
		typ   postingest.TaskType
		table string
		want  string
	}{
		{postingest.TaskPreview, "preview_task", "waiting"},
		{postingest.TaskSubtitle, "subtitle_task", "pending"},
		{postingest.TaskAtrack, "atrack_task", "waiting"},
		{postingest.TaskKeyframe, "keyframe_task", "waiting"},
	} {
		t.Run(string(tc.typ), func(t *testing.T) {
			db, mid, _ := explicitPostIngestDB(t)
			if _, err := db.Exec(`DELETE FROM `+tc.table+` WHERE media_id=?`, mid); err != nil {
				t.Fatal(err)
			}

			if _, err := enqueueExplicitPostIngest(context.Background(), db, mid, tc.typ, false, nil, nil); err != nil {
				t.Fatal(err)
			}

			var got string
			if err := db.QueryRow(`SELECT status FROM `+tc.table+` WHERE media_id=?`, mid).Scan(&got); err != nil {
				t.Fatalf("domain row: %v", err)
			}
			if got != tc.want {
				t.Fatalf("status=%q want %q", got, tc.want)
			}
		})
	}
}

func TestEnqueueExplicitPostIngestRepairsMissingDomainRowWhenAlreadyWaiting(t *testing.T) {
	db, mid, sid := explicitPostIngestDB(t)
	if _, err := db.Exec(`DELETE FROM preview_task WHERE media_id=?`, mid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status) VALUES(?,?,'preview','waiting')`, mid, sid); err != nil {
		t.Fatal(err)
	}

	got, err := enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskPreview, false, nil, nil)
	if err != nil || got != explicitPostIngestAlreadyQueued {
		t.Fatalf("result=%q err=%v", got, err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM preview_task WHERE media_id=?`, mid).Scan(&status); err != nil {
		t.Fatalf("domain row: %v", err)
	}
	if status != "waiting" {
		t.Fatalf("status=%q want waiting", status)
	}
}

func TestEnqueueExplicitPostIngestDoesNotQueueWhenDomainResetFails(t *testing.T) {
	db, mid, sid := explicitPostIngestDB(t)
	_, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts) VALUES(?,?,'preview','failed',2)`, mid, sid)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("domain reset failed")
	_, err = enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskPreview, false, func(context.Context, *sql.Tx) error { return want }, nil)
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE media_id=? AND task_type='preview'`, mid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("post status=%s", status)
	}
}

func TestEnqueueExplicitPostIngestSerializesConcurrentRetry(t *testing.T) {
	db, mid, sid := explicitPostIngestDB(t)
	_, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts) VALUES(?,?,'preview','failed',2)`, mid, sid)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	reset := func(context.Context, *sql.Tx) error {
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-release
		}
		return nil
	}
	errs := make(chan error, 2)
	go func() {
		_, err := enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskPreview, false, reset, nil)
		errs <- err
	}()
	<-firstEntered
	go func() {
		_, err := enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskPreview, false, reset, nil)
		errs <- err
	}()
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		close(release)
		t.Fatalf("concurrent reset calls before release=%d want 1", got)
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("concurrent retry: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("reset calls=%d want 1", calls.Load())
	}
}

func TestKeyframeRetryErrorLeavesPostTaskFailed(t *testing.T) {
	db, mid, sid := explicitPostIngestDB(t)
	if _, err := db.Exec(`INSERT INTO keyframe_task(media_id,status,error_message) VALUES(?,'failed','old')`, mid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts) VALUES(?,?,'keyframe','failed',2)`, mid, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER block_keyframe_retry BEFORE UPDATE ON keyframe_task BEGIN SELECT RAISE(ABORT,'retry blocked'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskKeyframe, false, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE keyframe_task SET status='waiting',error_message=NULL WHERE media_id=?`, mid)
		return err
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "retry blocked") {
		t.Fatalf("err=%v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE media_id=? AND task_type='keyframe'`, mid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("post status=%s", status)
	}
}

func TestEnqueueExplicitPostIngestReportsExistingState(t *testing.T) {
	for _, tc := range []struct {
		status postingest.Status
		want   explicitPostIngestResult
	}{{postingest.StatusWaiting, explicitPostIngestAlreadyQueued}, {postingest.StatusRunning, explicitPostIngestAlreadyRunning}, {postingest.StatusDone, explicitPostIngestAlreadyDone}} {
		t.Run(string(tc.status), func(t *testing.T) {
			db, mid, sid := explicitPostIngestDB(t)
			_, _ = db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status) VALUES(?,?,'preview',?)`, mid, sid, tc.status)
			got, err := enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskPreview, false, nil, nil)
			if err != nil || got != tc.want {
				t.Fatalf("got=%v err=%v want=%v", got, err, tc.want)
			}
		})
	}
}
func TestEnqueueExplicitPostIngestAllowsDoneOnlyForRetry(t *testing.T) {
	db, mid, sid := explicitPostIngestDB(t)
	res, _ := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status) VALUES(?,?,'preview','done')`, mid, sid)
	id, _ := res.LastInsertId()
	calls := 0
	got, err := enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskPreview, true, func(context.Context, *sql.Tx) error { calls++; return nil }, nil)
	if err != nil || got != explicitPostIngestQueued || calls != 1 {
		t.Fatalf("got=%v err=%v calls=%d", got, err, calls)
	}
	var status postingest.Status
	_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status)
	if status != postingest.StatusWaiting {
		t.Fatalf("status=%s", status)
	}
}

func TestScheduledSubtitleAndAtrackOnlyEnqueue(t *testing.T) {
	src, err := os.ReadFile("schedule_task.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, bad := range []string{"h.Subtitle.RunBatch(context.Background()", "h.AtrackWorker.RunBatch("} {
		if strings.Contains(text, bad) {
			t.Fatalf("scheduled bypass remains: %s", bad)
		}
	}
}

func TestEnqueueExplicitPostIngestRollsBackDomainWhenPostUpdateFails(t *testing.T) {
	db, mid, sid := explicitPostIngestDB(t)
	_, _ = db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status) VALUES(?,?,'preview','failed'); CREATE TRIGGER reject_post_retry BEFORE UPDATE ON post_ingest_task BEGIN SELECT RAISE(FAIL,'reject post'); END`, mid, sid)
	reset := func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE preview_task SET status='waiting' WHERE media_id=?`, mid)
		return err
	}
	_, err := enqueueExplicitPostIngest(context.Background(), db, mid, postingest.TaskPreview, false, reset, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var domain, post string
	_ = db.QueryRow(`SELECT status FROM preview_task WHERE media_id=?`, mid).Scan(&domain)
	_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE media_id=? AND task_type='preview'`, mid).Scan(&post)
	if domain != "ready" || post != "failed" {
		t.Fatalf("domain=%s post=%s", domain, post)
	}
}
func TestEnqueueExplicitPostIngestCancellationChangesNothing(t *testing.T) {
	db, mid, sid := explicitPostIngestDB(t)
	_, _ = db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status) VALUES(?,?,'preview','failed')`, mid, sid)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reset := func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE preview_task SET status='waiting' WHERE media_id=?`, mid)
		return err
	}
	_, err := enqueueExplicitPostIngest(ctx, db, mid, postingest.TaskPreview, false, reset, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	var domain string
	_ = db.QueryRow(`SELECT status FROM preview_task WHERE media_id=?`, mid).Scan(&domain)
	if domain != "ready" {
		t.Fatalf("domain=%s", domain)
	}
}
