package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"knox-media/internal/store"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func openQueueTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "postingest-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	path := filepath.Join(dir, "postingest.db")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		dir := filepath.Dir(path)
		for i := 0; i < 20; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
	return db, path
}

func seedQueueTest(t *testing.T, db *sql.DB) (mediaID, scanOne, scanTwo int64) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO library (name, type, path) VALUES ('test', 'video', '/test')`)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libraryID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("library id: %v", err)
	}
	res, err = db.Exec(`INSERT INTO media (library_id, file_id) VALUES (?, 'queue-media')`, libraryID)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	mediaID, _ = res.LastInsertId()
	for i, target := range []*int64{&scanOne, &scanTwo} {
		res, err = db.Exec(`INSERT INTO scan_task (library_id, status, source) VALUES (?, 'running', ?)`, libraryID, "test"+string(rune('1'+i)))
		if err != nil {
			t.Fatalf("insert scan task: %v", err)
		}
		*target, _ = res.LastInsertId()
	}
	return mediaID, scanOne, scanTwo
}

func TestQueue_EnqueueIdempotent(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, scanOne, scanTwo := seedQueueTest(t, db)
	q := NewQueue(db, "owner", nil)

	inserted, err := q.Enqueue(context.Background(), mediaID, &scanOne, TaskPoster)
	if err != nil || !inserted {
		t.Fatalf("first enqueue = (%v, %v), want (true, nil)", inserted, err)
	}
	inserted, err = q.Enqueue(context.Background(), mediaID, &scanTwo, TaskPoster)
	if err != nil || inserted {
		t.Fatalf("second enqueue = (%v, %v), want (false, nil)", inserted, err)
	}

	var count, attempts int
	var gotScan int64
	var status string
	if err := db.QueryRow(`SELECT COUNT(*), scan_task_id, status, attempts FROM post_ingest_task WHERE media_id=? AND task_type=?`, mediaID, TaskPoster).Scan(&count, &gotScan, &status, &attempts); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if count != 1 || gotScan != scanOne || status != string(StatusWaiting) || attempts != 0 {
		t.Fatalf("task = count %d scan %d status %q attempts %d", count, gotScan, status, attempts)
	}
}

func TestQueue_RetryExplicitly(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, scanOne, scanTwo := seedQueueTest(t, db)
	q := NewQueue(db, "owner", nil)
	ctx := context.Background()

	for i, initial := range []Status{StatusFailed, StatusCancelled} {
		typ := []TaskType{TaskPreview, TaskKeyframe}[i]
		res, err := db.Exec(`INSERT INTO post_ingest_task
			(media_id, scan_task_id, task_type, status, attempts, available_at, lease_owner, lease_until, last_error, started_at, finished_at)
			VALUES (?, ?, ?, ?, 2, datetime(CURRENT_TIMESTAMP, '+1 day'), 'old-owner', datetime(CURRENT_TIMESTAMP, '+1 day'), 'boom', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			mediaID, scanOne, typ, initial)
		if err != nil {
			t.Fatalf("insert %s task: %v", initial, err)
		}
		id, _ := res.LastInsertId()
		if err := q.Retry(ctx, id, &scanTwo); err != nil {
			t.Fatalf("retry %s: %v", initial, err)
		}
		var scanID int64
		var status string
		var attempts int
		var available string
		var leaseOwner, leaseUntil, lastError, started, finished sql.NullString
		if err := db.QueryRow(`SELECT scan_task_id,status,attempts,available_at,lease_owner,lease_until,last_error,started_at,finished_at FROM post_ingest_task WHERE id=?`, id).
			Scan(&scanID, &status, &attempts, &available, &leaseOwner, &leaseUntil, &lastError, &started, &finished); err != nil {
			t.Fatalf("read retried task: %v", err)
		}
		if scanID != scanTwo || status != string(StatusWaiting) || attempts != 0 || leaseOwner.Valid || leaseUntil.Valid || lastError.String != "" || started.Valid || finished.Valid {
			t.Fatalf("retried task not reset: scan=%d status=%q attempts=%d owner=%v lease=%v error=%q started=%v finished=%v", scanID, status, attempts, leaseOwner, leaseUntil, lastError.String, started, finished)
		}
		var availableNow int
		if err := db.QueryRow(`SELECT available_at <= CURRENT_TIMESTAMP FROM post_ingest_task WHERE id=?`, id).Scan(&availableNow); err != nil || availableNow != 1 {
			t.Fatalf("available now = %d, err %v (stored %q)", availableNow, err, available)
		}
	}

	for i, status := range []Status{StatusWaiting, StatusRunning} {
		typ := []TaskType{TaskSubtitle, TaskAtrack}[i]
		res, err := db.Exec(`INSERT INTO post_ingest_task (media_id, task_type, status) VALUES (?, ?, ?)`, mediaID, typ, status)
		if err != nil {
			t.Fatalf("insert %s: %v", status, err)
		}
		id, _ := res.LastInsertId()
		err = q.Retry(ctx, id, nil)
		if err == nil || !strings.Contains(err.Error(), string(status)) {
			t.Fatalf("Retry status %s error = %v, want explicit status error", status, err)
		}
	}
	if err := q.Retry(ctx, 1<<60, nil); err == nil || !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Fatalf("Retry missing error = %v, want not found", err)
	}
}

func TestQueue_ClaimCompetes(t *testing.T) {
	db1, path := openQueueTestDB(t)
	mediaID, scanOne, _ := seedQueueTest(t, db1)
	if _, err := NewQueue(db1, "seed", nil).Enqueue(context.Background(), mediaID, &scanOne, TaskPoster); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	db2, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open second sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	queues := []*Queue{NewQueue(db1, "owner-a", nil), NewQueue(db2, "owner-b", nil)}
	start := make(chan struct{})
	results := make(chan *Task, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, q := range queues {
		wg.Add(1)
		go func(q *Queue) {
			defer wg.Done()
			<-start
			task, err := q.Claim(context.Background(), TaskPoster)
			results <- task
			errs <- err
		}(q)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
	}
	var winner *Task
	claimed := 0
	for task := range results {
		if task != nil {
			claimed++
			winner = task
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed count = %d, want 1", claimed)
	}
	if winner.Status != StatusRunning || winner.Attempts != 1 || (!strings.HasPrefix(winner.LeaseOwner, "owner-a/") && !strings.HasPrefix(winner.LeaseOwner, "owner-b/")) {
		t.Fatalf("winner = %+v", winner)
	}
	if !winner.LeaseUntil.After(time.Now().Add(70*time.Second)) || winner.LeaseUntil.After(time.Now().Add(100*time.Second)) {
		t.Fatalf("lease until = %v, want about 90s", winner.LeaseUntil)
	}
	var status, owner string
	var attempts int
	var leaseUntil time.Time
	if err := db1.QueryRow(`SELECT status, attempts, lease_owner, lease_until FROM post_ingest_task WHERE id=?`, winner.ID).Scan(&status, &attempts, &owner, &leaseUntil); err != nil {
		t.Fatalf("read claimed task: %v", err)
	}
	if status != string(StatusRunning) || attempts != 1 || owner != winner.LeaseOwner || leaseUntil.IsZero() {
		t.Fatalf("stored claim = status %q attempts %d owner %q lease %v", status, attempts, owner, leaseUntil)
	}
}

func TestQueue_RejectsNonPositiveScanTaskID(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "owner", nil)
	ctx := context.Background()

	for _, scanID := range []int64{0, -1} {
		inserted, err := q.Enqueue(ctx, mediaID, &scanID, TaskPoster)
		if err == nil || inserted || !strings.Contains(strings.ToLower(err.Error()), "scan") {
			t.Fatalf("Enqueue scan ID %d = (%v, %v), want explicit error", scanID, inserted, err)
		}
	}
	res, err := db.Exec(`INSERT INTO post_ingest_task (media_id, task_type, status) VALUES (?, ?, 'failed')`, mediaID, TaskPreview)
	if err != nil {
		t.Fatalf("insert failed task: %v", err)
	}
	id, _ := res.LastInsertId()
	for _, scanID := range []int64{0, -1} {
		if err := q.Retry(ctx, id, &scanID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "scan") {
			t.Fatalf("Retry scan ID %d error = %v, want explicit error", scanID, err)
		}
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil || status != string(StatusFailed) {
		t.Fatalf("invalid retry modified task: status=%q err=%v", status, err)
	}
}

func insertQueueMedia(t *testing.T, db *sql.DB, libraryID int64, fileID string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO media (library_id, file_id) VALUES (?, ?)`, libraryID, fileID)
	if err != nil {
		t.Fatalf("insert media %s: %v", fileID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("media id %s: %v", fileID, err)
	}
	return id
}

func mediaLibraryID(t *testing.T, db *sql.DB, mediaID int64) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT library_id FROM media WHERE id=?`, mediaID).Scan(&id); err != nil {
		t.Fatalf("media library: %v", err)
	}
	return id
}

func TestQueue_ClaimOldestEligible(t *testing.T) {
	db, _ := openQueueTestDB(t)
	seedMedia, _, _ := seedQueueTest(t, db)
	libraryID := mediaLibraryID(t, db, seedMedia)
	insert := func(fileID string, typ TaskType, available string, attempts, maxAttempts int, created string) int64 {
		t.Helper()
		mediaID := insertQueueMedia(t, db, libraryID, fileID)
		res, err := db.Exec(`INSERT INTO post_ingest_task
			(media_id, task_type, available_at, attempts, max_attempts, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, mediaID, typ, available, attempts, maxAttempts, created)
		if err != nil {
			t.Fatalf("insert candidate %s: %v", fileID, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	insert("future", TaskPoster, "2999-01-01 00:00:00", 0, 3, "2020-01-01 00:00:00")
	insert("exhausted", TaskPoster, "2020-01-01 00:00:00", 3, 3, "2020-01-01 00:00:00")
	insert("other-type", TaskPreview, "2020-01-01 00:00:00", 0, 3, "2019-01-01 00:00:00")
	oldestID := insert("oldest", TaskPoster, "2020-01-01 00:00:00", 0, 3, "2021-01-01 00:00:00")
	insert("same-time-later-id", TaskPoster, "2020-01-01 00:00:00", 0, 3, "2021-01-01 00:00:00")

	task, err := NewQueue(db, "owner", nil).Claim(context.Background(), TaskPoster)
	if err != nil || task == nil || task.ID != oldestID {
		t.Fatalf("Claim = (%+v, %v), want oldest eligible ID %d", task, err, oldestID)
	}
}

func TestQueue_ClaimHonorsScanCancellationAndAllowsNull(t *testing.T) {
	db, _ := openQueueTestDB(t)
	seedMedia, scanCancelledFlag, scanCancelledStatus := seedQueueTest(t, db)
	libraryID := mediaLibraryID(t, db, seedMedia)
	if _, err := db.Exec(`UPDATE scan_task SET cancelled=1 WHERE id=?`, scanCancelledFlag); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE scan_task SET status='cancelled' WHERE id=?`, scanCancelledStatus); err != nil {
		t.Fatal(err)
	}
	for i, scanID := range []any{scanCancelledFlag, scanCancelledStatus, nil} {
		mediaID := insertQueueMedia(t, db, libraryID, fmt.Sprintf("scan-filter-%d", i))
		if _, err := db.Exec(`INSERT INTO post_ingest_task (media_id, scan_task_id, task_type, created_at) VALUES (?, ?, ?, ?)`, mediaID, scanID, TaskPoster, fmt.Sprintf("202%d-01-01 00:00:00", i)); err != nil {
			t.Fatalf("insert scan candidate: %v", err)
		}
	}
	task, err := NewQueue(db, "owner", nil).Claim(context.Background(), TaskPoster)
	if err != nil || task == nil || task.ScanTaskID != nil {
		t.Fatalf("Claim = (%+v, %v), want NULL-scan task", task, err)
	}
	again, err := NewQueue(db, "owner", nil).Claim(context.Background(), TaskPoster)
	if err != nil || again != nil {
		t.Fatalf("second Claim = (%+v, %v), want no eligible task", again, err)
	}
}

func TestQueue_ClaimStartedAtCoalesces(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "owner", nil)
	inserted, err := q.Enqueue(context.Background(), mediaID, nil, TaskPoster)
	if err != nil || !inserted {
		t.Fatalf("enqueue = (%v, %v)", inserted, err)
	}
	first, err := q.Claim(context.Background(), TaskPoster)
	if err != nil || first == nil {
		t.Fatalf("first claim = (%+v, %v)", first, err)
	}
	var startedFirst time.Time
	if err := db.QueryRow(`SELECT started_at FROM post_ingest_task WHERE id=?`, first.ID).Scan(&startedFirst); err != nil || startedFirst.IsZero() {
		t.Fatalf("first started_at = %v, err %v", startedFirst, err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET status='waiting', lease_owner=NULL, lease_until=NULL WHERE id=?`, first.ID); err != nil {
		t.Fatalf("reset waiting: %v", err)
	}
	second, err := q.Claim(context.Background(), TaskPoster)
	if err != nil || second == nil {
		t.Fatalf("second claim = (%+v, %v)", second, err)
	}
	var startedSecond time.Time
	if err := db.QueryRow(`SELECT started_at FROM post_ingest_task WHERE id=?`, first.ID).Scan(&startedSecond); err != nil {
		t.Fatalf("second started_at: %v", err)
	}
	if !startedSecond.Equal(startedFirst) {
		t.Fatalf("started_at changed from %v to %v", startedFirst, startedSecond)
	}
}

func TestQueue_ValidationAndCancelledContext(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	validQueue := NewQueue(db, "owner", nil)
	ctx := context.Background()

	if _, err := NewQueue(nil, "owner", nil).Enqueue(ctx, mediaID, nil, TaskPoster); err == nil {
		t.Fatal("Enqueue nil DB: want error")
	}
	if err := NewQueue(nil, "owner", nil).Retry(ctx, 1, nil); err == nil {
		t.Fatal("Retry nil DB: want error")
	}
	if _, err := NewQueue(nil, "owner", nil).Claim(ctx, TaskPoster); err == nil {
		t.Fatal("Claim nil DB: want error")
	}
	if _, err := NewQueue(db, "", nil).Claim(ctx, TaskPoster); err == nil || !strings.Contains(strings.ToLower(err.Error()), "owner") {
		t.Fatalf("Claim empty owner error = %v", err)
	}
	if inserted, err := validQueue.Enqueue(ctx, 0, nil, TaskPoster); err == nil || inserted {
		t.Fatalf("Enqueue media 0 = (%v, %v), want error", inserted, err)
	}
	if inserted, err := validQueue.Enqueue(ctx, mediaID, nil, TaskType("bogus")); err == nil || inserted {
		t.Fatalf("Enqueue invalid type = (%v, %v), want error", inserted, err)
	}
	if _, err := validQueue.Claim(ctx, TaskType("bogus")); err == nil {
		t.Fatal("Claim invalid type: want error")
	}

	res, err := db.Exec(`INSERT INTO post_ingest_task (media_id, task_type, status) VALUES (?, ?, 'failed')`, mediaID, TaskPreview)
	if err != nil {
		t.Fatal(err)
	}
	retryID, _ := res.LastInsertId()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if inserted, err := validQueue.Enqueue(cancelled, mediaID, nil, TaskPoster); !errors.Is(err, context.Canceled) || inserted {
		t.Fatalf("cancelled Enqueue = (%v, %v)", inserted, err)
	}
	if err := validQueue.Retry(cancelled, retryID, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Retry error = %v", err)
	}
	if task, err := validQueue.Claim(cancelled, TaskPoster); !errors.Is(err, context.Canceled) || task != nil {
		t.Fatalf("cancelled Claim = (%+v, %v)", task, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND task_type=?`, mediaID, TaskPoster).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled Enqueue wrote %d rows, err %v", count, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, retryID).Scan(&status); err != nil || status != string(StatusFailed) {
		t.Fatalf("cancelled Retry changed status=%q err=%v", status, err)
	}
}

func TestQueue_ClaimNoTaskRepeatedly(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "owner", nil)
	for i := 0; i < 200; i++ {
		task, err := q.Claim(context.Background(), TaskPoster)
		if err != nil || task != nil {
			t.Fatalf("empty claim %d = (%+v, %v)", i, task, err)
		}
	}
}

func TestLeaseModifierUsesConfiguredDuration(t *testing.T) {
	want := fmt.Sprintf("+%d seconds", int64(leaseDuration/time.Second))
	if got := leaseModifier(); got != want {
		t.Fatalf("leaseModifier() = %q, want %q", got, want)
	}
	if leaseDuration <= 0 || leaseDuration%time.Second != 0 {
		t.Fatalf("leaseDuration = %v, want positive whole seconds", leaseDuration)
	}
}

func readTaskState(t *testing.T, db *sql.DB, id int64) (Status, int, int, sql.NullString, sql.NullString, string, sql.NullString, string) {
	t.Helper()
	var status Status
	var attempts, maxAttempts int
	var owner, lease, finished sql.NullString
	var lastError, available string
	if err := db.QueryRow(`SELECT status,attempts,max_attempts,lease_owner,lease_until,last_error,finished_at,available_at FROM post_ingest_task WHERE id=?`, id).Scan(&status, &attempts, &maxAttempts, &owner, &lease, &lastError, &finished, &available); err != nil {
		t.Fatalf("read task %d: %v", id, err)
	}
	return status, attempts, maxAttempts, owner, lease, lastError, finished, available
}

func TestQueue_Lifecycle(t *testing.T) {
	ctx := context.Background()
	t.Run("owner renew and complete", func(t *testing.T) {
		db, _ := openQueueTestDB(t)
		mediaID, _, _ := seedQueueTest(t, db)
		owner := NewQueue(db, "owner-a", nil)
		if _, err := owner.Enqueue(ctx, mediaID, nil, TaskPoster); err != nil {
			t.Fatal(err)
		}
		task, err := owner.Claim(ctx, TaskPoster)
		if err != nil || task == nil {
			t.Fatalf("claim: %+v, %v", task, err)
		}
		if _, err := db.Exec(`UPDATE post_ingest_task SET lease_until=datetime(CURRENT_TIMESTAMP,'+10 seconds') WHERE id=?`, task.ID); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT lease_until FROM post_ingest_task WHERE id=?`, task.ID).Scan(&task.LeaseUntil); err != nil {
			t.Fatal(err)
		}
		before := task.LeaseUntil
		other := NewQueue(db, "owner-b", nil)
		ok, err := other.Renew(ctx, *task)
		if err == nil || ok || !strings.Contains(strings.ToLower(err.Error()), "ownership") {
			t.Fatalf("non-owner renew=(%v,%v)", ok, err)
		}
		_, _, _, gotOwner, gotLease, _, _, _ := readTaskState(t, db, task.ID)
		if gotOwner.String != task.LeaseOwner || gotLease.String == "" {
			t.Fatalf("non-owner changed lease: owner=%v lease=%v", gotOwner, gotLease)
		}
		if err := other.Complete(ctx, *task); err == nil || !strings.Contains(strings.ToLower(err.Error()), "owner") {
			t.Fatalf("non-owner complete error=%v", err)
		}
		ok, err = owner.Renew(ctx, *task)
		if err != nil || !ok {
			t.Fatalf("owner renew=(%v,%v)", ok, err)
		}
		var renewed time.Time
		if err := db.QueryRow(`SELECT lease_until FROM post_ingest_task WHERE id=?`, task.ID).Scan(&renewed); err != nil {
			t.Fatal(err)
		}
		if !renewed.After(before) || renewed.Before(time.Now().Add(leaseDuration-5*time.Second)) {
			t.Fatalf("renewed lease=%v before=%v", renewed, before)
		}
		if err := owner.Complete(ctx, *task); err != nil {
			t.Fatal(err)
		}
		status, _, _, leaseOwner, leaseUntil, lastError, finished, _ := readTaskState(t, db, task.ID)
		if status != StatusDone || leaseOwner.Valid || leaseUntil.Valid || lastError != "" || !finished.Valid {
			t.Fatalf("completed state=%s owner=%v lease=%v error=%q finished=%v", status, leaseOwner, leaseUntil, lastError, finished)
		}
	})

	t.Run("failure transitions backoff and truncation", func(t *testing.T) {
		db, _ := openQueueTestDB(t)
		seedMedia, _, _ := seedQueueTest(t, db)
		libraryID := mediaLibraryID(t, db, seedMedia)
		q := NewQueue(db, "owner", nil)
		cases := []struct {
			name          string
			attempts, max int
			kind          FailureKind
			want          Status
			delay         int
		}{
			{"retry first", 1, 3, FailureRetryable, StatusWaiting, 5}, {"retry second", 2, 3, FailureRetryable, StatusWaiting, 30},
			{"exhaust third", 3, 3, FailureRetryable, StatusFailed, 0}, {"retry third with max four", 3, 4, FailureRetryable, StatusWaiting, 120},
			{"permanent", 1, 3, FailurePermanent, StatusFailed, 0}, {"cancelled", 1, 3, FailureCancelled, StatusCancelled, 0},
		}
		for i, tc := range cases {
			mediaID := insertQueueMedia(t, db, libraryID, fmt.Sprintf("lifecycle-%d", i))
			res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,attempts,max_attempts) VALUES (?,?,?,?)`, mediaID, TaskPoster, tc.attempts-1, tc.max)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			task, err := q.Claim(ctx, TaskPoster)
			if err != nil || task == nil || task.ID != id || task.Attempts != tc.attempts {
				t.Fatalf("%s claim=(%+v,%v)", tc.name, task, err)
			}
			cause := error(errors.New(strings.Repeat("x", 5000)))
			if err := q.Fail(ctx, task, tc.kind, cause); err != nil {
				t.Fatalf("%s fail: %v", tc.name, err)
			}
			status, _, _, owner, lease, lastError, finished, _ := readTaskState(t, db, id)
			if status != tc.want || owner.Valid || lease.Valid {
				t.Fatalf("%s state=%s owner=%v lease=%v", tc.name, status, owner, lease)
			}
			if len(lastError) > 4096 || !utf8.ValidString(lastError) {
				t.Fatalf("%s unsafe error bytes=%d valid=%v", tc.name, len(lastError), utf8.ValidString(lastError))
			}
			if tc.want == StatusWaiting && finished.Valid {
				t.Fatalf("%s waiting has finished", tc.name)
			}
			if tc.want != StatusWaiting && !finished.Valid {
				t.Fatalf("%s terminal lacks finished", tc.name)
			}
			var delta int
			if err := db.QueryRow(`SELECT CAST(strftime('%s',available_at) AS INTEGER)-CAST(strftime('%s','now') AS INTEGER) FROM post_ingest_task WHERE id=?`, id).Scan(&delta); err != nil {
				t.Fatalf("%s read delay: %v", tc.name, err)
			}
			if tc.delay > 0 {
				if delta < tc.delay-2 || delta > tc.delay+2 {
					t.Fatalf("%s delay=%d, want~%d", tc.name, delta, tc.delay)
				}
			} else if tc.kind == FailureRetryable && tc.want == StatusFailed && delta > 1 {
				t.Fatalf("%s terminal delay=%d, want not in future", tc.name, delta)
			}
		}
	})

	t.Run("nil cause stale attempts ownership and shutdown", func(t *testing.T) {
		db, _ := openQueueTestDB(t)
		mediaID, _, _ := seedQueueTest(t, db)
		q := NewQueue(db, "owner", nil)
		res, _ := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,attempts,max_attempts) VALUES (?,?,2,3)`, mediaID, TaskPoster)
		id, _ := res.LastInsertId()
		claimed, err := q.Claim(ctx, TaskPoster)
		if err != nil || claimed == nil || claimed.ID != id || claimed.Attempts != 3 {
			t.Fatalf("claim=(%+v,%v)", claimed, err)
		}
		staleAttempts := *claimed
		staleAttempts.Attempts = 1
		if err := q.Fail(ctx, &staleAttempts, FailureRetryable, nil); err != nil {
			t.Fatal(err)
		}
		status, attempts, _, _, _, last, _, _ := readTaskState(t, db, id)
		if status != StatusFailed || attempts != 3 || last == "" {
			t.Fatalf("DB attempts not used: status=%s attempts=%d error=%q", status, attempts, last)
		}
		media2 := insertQueueMedia(t, db, mediaLibraryID(t, db, mediaID), "shutdown")
		res, _ = db.Exec(`INSERT INTO post_ingest_task(media_id,task_type) VALUES (?,?)`, media2, TaskPoster)
		sid, _ := res.LastInsertId()
		shutdownTask, err := q.Claim(ctx, TaskPoster)
		if err != nil || shutdownTask == nil || shutdownTask.ID != sid {
			t.Fatalf("shutdown claim=(%+v,%v)", shutdownTask, err)
		}
		if err := q.Fail(ctx, shutdownTask, FailureShutdown, errors.New("stop")); err != nil {
			t.Fatal(err)
		}
		status, _, _, owner, lease, last, finished, _ := readTaskState(t, db, sid)
		if status != StatusRunning || owner.String != shutdownTask.LeaseOwner || !lease.Valid || last != "stop" || finished.Valid {
			t.Fatalf("shutdown changed lease state: %s %v %v %q %v", status, owner, lease, last, finished)
		}
		if err := NewQueue(db, "other", nil).Fail(ctx, &Task{ID: sid, Attempts: 1, LeaseOwner: "other/fake"}, FailurePermanent, errors.New("x")); err == nil || !strings.Contains(strings.ToLower(err.Error()), "owner") {
			t.Fatalf("non-owner fail=%v", err)
		}
		if err := q.Fail(ctx, shutdownTask, FailureKind(99), nil); err == nil {
			t.Fatal("invalid kind accepted")
		}
	})
}

func TestQueue_RecoverExpired(t *testing.T) {
	db, _ := openQueueTestDB(t)
	seedMedia, scanCancelled, scanStatus := seedQueueTest(t, db)
	libraryID := mediaLibraryID(t, db, seedMedia)
	if _, err := db.Exec(`UPDATE scan_task SET cancelled=1 WHERE id=?`, scanCancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE scan_task SET status='cancelled' WHERE id=?`, scanStatus); err != nil {
		t.Fatal(err)
	}
	type row struct {
		id      int64
		want    Status
		expired bool
	}
	var rows []row
	for i, tc := range []struct {
		scan          any
		attempts, max int
		expired       bool
		want          Status
	}{{scanCancelled, 1, 3, true, StatusCancelled}, {scanStatus, 1, 3, true, StatusCancelled}, {nil, 3, 3, true, StatusFailed}, {nil, 1, 3, true, StatusWaiting}, {nil, 1, 3, false, StatusRunning}} {
		mid := insertQueueMedia(t, db, libraryID, fmt.Sprintf("recover-%d", i))
		modifier := "-1 second"
		if !tc.expired {
			modifier = "+1 hour"
		}
		res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts,max_attempts,lease_owner,lease_until) VALUES (?,?,?,'running',?,?,'old',datetime(CURRENT_TIMESTAMP,?))`, mid, tc.scan, TaskPoster, tc.attempts, tc.max, modifier)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		rows = append(rows, row{id, tc.want, tc.expired})
	}
	n, err := NewQueue(db, "recovery", nil).RecoverExpired(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("RecoverExpired=(%d,%v), want (4,nil)", n, err)
	}
	for _, tc := range rows {
		status, _, _, owner, lease, last, finished, _ := readTaskState(t, db, tc.id)
		if status != tc.want {
			t.Fatalf("id %d status=%s want=%s", tc.id, status, tc.want)
		}
		if tc.expired {
			if owner.Valid || lease.Valid {
				t.Fatalf("id %d lease retained", tc.id)
			}
			if status == StatusFailed && !strings.Contains(strings.ToLower(last), "lease") {
				t.Fatalf("id %d exhausted error=%q", tc.id, last)
			}
			if status == StatusWaiting && finished.Valid {
				t.Fatalf("id %d waiting finished", tc.id)
			}
			if status != StatusWaiting && !finished.Valid {
				t.Fatalf("id %d terminal unfinished", tc.id)
			}
		} else if owner.String != "old" || !lease.Valid {
			t.Fatalf("unexpired changed")
		}
	}
}

func TestQueue_CancelScanRace(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		db1, path := openQueueTestDB(t)
		seedMedia, scanOne, scanTwo := seedQueueTest(t, db1)
		libraryID := mediaLibraryID(t, db1, seedMedia)
		waitingMedia := insertQueueMedia(t, db1, libraryID, fmt.Sprintf("race-w-%d", iteration))
		res, err := db1.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type) VALUES (?,?,?)`, waitingMedia, scanOne, TaskPoster)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		otherMedia := insertQueueMedia(t, db1, libraryID, fmt.Sprintf("race-o-%d", iteration))
		db1.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status) VALUES (?,?,?,'done')`, otherMedia, scanOne, TaskPoster)
		batchMedia := insertQueueMedia(t, db1, libraryID, fmt.Sprintf("race-b-%d", iteration))
		db1.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type) VALUES (?,?,?)`, batchMedia, scanTwo, TaskPoster)
		db2, e := store.OpenSQLite(path)
		if e != nil {
			t.Fatal(e)
		}
		qClaim := NewQueue(db1, "claimer", nil)
		qCancel := NewQueue(db2, "canceller", nil)
		start := make(chan struct{})
		var claimed *Task
		var claimErr, cancelErr error
		var cancelled int64
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; claimed, claimErr = qClaim.Claim(context.Background(), TaskPoster) }()
		go func() {
			defer wg.Done()
			<-start
			cancelled, cancelErr = qCancel.CancelScan(context.Background(), scanOne)
		}()
		close(start)
		wg.Wait()
		_ = db2.Close()
		if claimErr != nil || cancelErr != nil {
			t.Fatalf("iteration %d errors claim=%v cancel=%v", iteration, claimErr, cancelErr)
		}
		status, _, _, _, _, _, finished, _ := readTaskState(t, db1, id)
		if claimed == nil || claimed.ID != id {
			if cancelled != 1 || status != StatusCancelled || !finished.Valid {
				t.Fatalf("iteration %d cancel won: count=%d status=%s finished=%v", iteration, cancelled, status, finished)
			}
			again, e := qClaim.Claim(context.Background(), TaskPoster)
			if e != nil || (again != nil && again.ID == id) {
				t.Fatalf("cancelled task reclaimed: %+v %v", again, e)
			}
		} else {
			if claimed.ID != id || status != StatusRunning || cancelled != 0 {
				t.Fatalf("iteration %d claim won: claimed=%+v count=%d status=%s", iteration, claimed, cancelled, status)
			}
		}
		var doneStatus, batchStatus Status
		if err := db1.QueryRow(`SELECT status FROM post_ingest_task WHERE media_id=?`, otherMedia).Scan(&doneStatus); err != nil || doneStatus != StatusDone {
			t.Fatalf("done changed: %s %v", doneStatus, err)
		}
		if err := db1.QueryRow(`SELECT status FROM post_ingest_task WHERE media_id=?`, batchMedia).Scan(&batchStatus); err != nil || (batchStatus != StatusWaiting && batchStatus != StatusRunning) {
			t.Fatalf("other batch changed: %s %v", batchStatus, err)
		}
		if _, err := qClaim.CancelScan(context.Background(), 0); err == nil {
			t.Fatal("CancelScan accepted zero")
		}
		if _, err := qClaim.IsScanCancelled(context.Background(), 1<<60); err == nil {
			t.Fatal("missing scan accepted")
		}
		if _, err := db1.Exec(`UPDATE scan_task SET cancelled=1 WHERE id=?`, scanOne); err != nil {
			t.Fatal(err)
		}
		isCancelled, err := qClaim.IsScanCancelled(context.Background(), scanOne)
		if err != nil || !isCancelled {
			t.Fatalf("IsScanCancelled=(%v,%v)", isCancelled, err)
		}
	}
}

func TestQueue_GenerationFencing(t *testing.T) {
	ctx := context.Background()
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "same-owner", nil)
	inserted, err := q.Enqueue(ctx, mediaID, nil, TaskPoster)
	if err != nil || !inserted {
		t.Fatalf("enqueue=(%v,%v)", inserted, err)
	}
	oldTask, err := q.Claim(ctx, TaskPoster)
	if err != nil || oldTask == nil || oldTask.Attempts != 1 {
		t.Fatalf("first claim=(%+v,%v)", oldTask, err)
	}
	if _, err := db.Exec(`UPDATE post_ingest_task SET lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?`, oldTask.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := q.RecoverExpired(ctx); err != nil || n != 1 {
		t.Fatalf("recover=(%d,%v)", n, err)
	}
	newTask, err := q.Claim(ctx, TaskPoster)
	if err != nil || newTask == nil || newTask.ID != oldTask.ID || newTask.Attempts != 2 {
		t.Fatalf("second claim=(%+v,%v), old=%+v", newTask, err, oldTask)
	}
	if oldTask.LeaseOwner == newTask.LeaseOwner || !strings.HasPrefix(oldTask.LeaseOwner, "same-owner/") || !strings.HasPrefix(newTask.LeaseOwner, "same-owner/") {
		t.Fatalf("claim tokens old=%q new=%q, want distinct same-owner tokens", oldTask.LeaseOwner, newTask.LeaseOwner)
	}
	var leaseBefore time.Time
	if err := db.QueryRow(`SELECT lease_until FROM post_ingest_task WHERE id=?`, newTask.ID).Scan(&leaseBefore); err != nil {
		t.Fatal(err)
	}
	if ok, err := q.Renew(ctx, *oldTask); err != nil || ok {
		t.Fatalf("stale Renew=(%v,%v), want false,nil", ok, err)
	}
	var leaseAfter time.Time
	if err := db.QueryRow(`SELECT lease_until FROM post_ingest_task WHERE id=?`, newTask.ID).Scan(&leaseAfter); err != nil {
		t.Fatal(err)
	}
	if !leaseAfter.Equal(leaseBefore) {
		t.Fatalf("stale Renew changed lease from %v to %v", leaseBefore, leaseAfter)
	}
	for name, operation := range map[string]func() error{
		"complete":       func() error { return q.Complete(ctx, *oldTask) },
		"fail retryable": func() error { return q.Fail(ctx, oldTask, FailureRetryable, errors.New("stale")) },
		"fail permanent": func() error { return q.Fail(ctx, oldTask, FailurePermanent, errors.New("stale")) },
		"fail cancelled": func() error { return q.Fail(ctx, oldTask, FailureCancelled, errors.New("stale")) },
		"fail shutdown":  func() error { return q.Fail(ctx, oldTask, FailureShutdown, errors.New("stale")) },
	} {
		if err := operation(); err == nil || (!strings.Contains(strings.ToLower(err.Error()), "generation") && !strings.Contains(strings.ToLower(err.Error()), "ownership")) {
			t.Fatalf("stale %s error=%v, want generation/ownership error", name, err)
		}
		status, attempts, _, owner, lease, _, finished, _ := readTaskState(t, db, newTask.ID)
		if status != StatusRunning || attempts != 2 || owner.String != newTask.LeaseOwner || !lease.Valid || finished.Valid {
			t.Fatalf("stale %s changed new generation: status=%s attempts=%d owner=%v lease=%v finished=%v", name, status, attempts, owner, lease, finished)
		}
	}
	if ok, err := q.Renew(ctx, *newTask); err != nil || !ok {
		t.Fatalf("current Renew=(%v,%v)", ok, err)
	}
	if err := q.Complete(ctx, *newTask); err != nil {
		t.Fatalf("current Complete=%v", err)
	}
}

func TestQueue_RejectsInvalidClaimToken(t *testing.T) {
	ctx := context.Background()
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "owner", nil)
	if _, err := q.Enqueue(ctx, mediaID, nil, TaskPoster); err != nil {
		t.Fatal(err)
	}
	claimed, err := q.Claim(ctx, TaskPoster)
	if err != nil || claimed == nil {
		t.Fatalf("claim=(%+v,%v)", claimed, err)
	}
	for name, task := range map[string]Task{
		"empty token":      {ID: claimed.ID, Attempts: claimed.Attempts},
		"empty suffix":     {ID: claimed.ID, Attempts: claimed.Attempts, LeaseOwner: "owner/"},
		"malformed UUID":   {ID: claimed.ID, Attempts: claimed.Attempts, LeaseOwner: "owner/not-a-uuid"},
		"foreign token":    {ID: claimed.ID, Attempts: claimed.Attempts, LeaseOwner: "other/" + strings.TrimPrefix(claimed.LeaseOwner, "owner/")},
		"prefix collision": {ID: claimed.ID, Attempts: claimed.Attempts, LeaseOwner: "owner/child/" + strings.TrimPrefix(claimed.LeaseOwner, "owner/")},
		"zero attempts":    {ID: claimed.ID, LeaseOwner: claimed.LeaseOwner},
	} {
		if ok, err := q.Renew(ctx, task); err == nil || ok {
			t.Fatalf("%s Renew=(%v,%v)", name, ok, err)
		}
		if err := q.Complete(ctx, task); err == nil {
			t.Fatalf("%s Complete=%v", name, err)
		}
		if err := q.Fail(ctx, &task, FailurePermanent, errors.New("x")); err == nil {
			t.Fatalf("%s Fail=%v", name, err)
		}
	}
	status, attempts, _, owner, lease, _, _, _ := readTaskState(t, db, claimed.ID)
	if status != StatusRunning || attempts != 1 || owner.String != claimed.LeaseOwner || !lease.Valid {
		t.Fatalf("invalid token changed task: status=%s attempts=%d owner=%v lease=%v", status, attempts, owner, lease)
	}
}

func TestQueue_LeaseTokenFencingABA(t *testing.T) {
	ctx := context.Background()
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "same-owner", nil)
	if _, err := q.Enqueue(ctx, mediaID, nil, TaskPoster); err != nil {
		t.Fatal(err)
	}
	oldTask, err := q.Claim(ctx, TaskPoster)
	if err != nil || oldTask == nil || oldTask.Attempts != 1 {
		t.Fatalf("old claim=(%+v,%v)", oldTask, err)
	}
	if err := q.Fail(ctx, oldTask, FailurePermanent, errors.New("first generation failed")); err != nil {
		t.Fatal(err)
	}
	if err := q.Retry(ctx, oldTask.ID, nil); err != nil {
		t.Fatal(err)
	}
	newTask, err := q.Claim(ctx, TaskPoster)
	if err != nil || newTask == nil || newTask.ID != oldTask.ID || newTask.Attempts != 1 {
		t.Fatalf("new claim=(%+v,%v)", newTask, err)
	}
	if oldTask.LeaseOwner == newTask.LeaseOwner {
		t.Fatalf("ABA tokens reused: %q", oldTask.LeaseOwner)
	}
	if ok, err := q.Renew(ctx, *oldTask); err != nil || ok {
		t.Fatalf("stale Renew=(%v,%v)", ok, err)
	}
	for name, operation := range map[string]func() error{
		"complete":  func() error { return q.Complete(ctx, *oldTask) },
		"retryable": func() error { return q.Fail(ctx, oldTask, FailureRetryable, errors.New("stale")) },
		"permanent": func() error { return q.Fail(ctx, oldTask, FailurePermanent, errors.New("stale")) },
		"cancelled": func() error { return q.Fail(ctx, oldTask, FailureCancelled, errors.New("stale")) },
		"shutdown":  func() error { return q.Fail(ctx, oldTask, FailureShutdown, errors.New("stale")) },
	} {
		if err := operation(); err == nil || !strings.Contains(strings.ToLower(err.Error()), "ownership") {
			t.Fatalf("stale %s error=%v", name, err)
		}
		status, attempts, _, owner, lease, _, finished, _ := readTaskState(t, db, newTask.ID)
		if status != StatusRunning || attempts != 1 || owner.String != newTask.LeaseOwner || !lease.Valid || finished.Valid {
			t.Fatalf("stale %s changed new claim: status=%s attempts=%d owner=%v lease=%v finished=%v", name, status, attempts, owner, lease, finished)
		}
	}
	if ok, err := q.Renew(ctx, *newTask); err != nil || !ok {
		t.Fatalf("new Renew=(%v,%v)", ok, err)
	}
	if err := q.Complete(ctx, *newTask); err != nil {
		t.Fatalf("new Complete=%v", err)
	}
}

func TestQueue_RejectsInvalidOwnerConfiguration(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "a/b", nil)
	if _, err := q.Claim(context.Background(), TaskPoster); err == nil || !strings.Contains(strings.ToLower(err.Error()), "owner") || !strings.Contains(err.Error(), "/") {
		t.Fatalf("Claim invalid owner error=%v", err)
	}
	fake := Task{ID: 1, Attempts: 1, LeaseOwner: "a/b/" + strings.Repeat("0", 36)}
	if ok, err := q.Renew(context.Background(), fake); err == nil || ok {
		t.Fatalf("Renew invalid owner=(%v,%v)", ok, err)
	}
}

func TestQueue_CancelScanFencesRetryableFailure(t *testing.T) {
	db, _ := openQueueTestDB(t)
	seedMedia, scanID, _ := seedQueueTest(t, db)
	libraryID := mediaLibraryID(t, db, seedMedia)
	mediaID := insertQueueMedia(t, db, libraryID, "cancel-fail-fence")
	q := NewQueue(db, "fail-fence-owner", nil)
	if _, err := q.Enqueue(context.Background(), mediaID, &scanID, TaskPoster); err != nil {
		t.Fatal(err)
	}
	task, err := q.Claim(context.Background(), TaskPoster)
	if err != nil || task == nil {
		t.Fatalf("Claim=%+v,%v", task, err)
	}
	if _, err := db.Exec(`UPDATE scan_task SET cancelled=1 WHERE id=?`, scanID); err != nil {
		t.Fatal(err)
	}
	if err := q.Fail(context.Background(), task, FailureRetryable, errors.New("executor returned before heartbeat")); err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, task.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusCancelled {
		t.Fatalf("status=%s want cancelled", status)
	}
}

func TestQueue_FailSerializesWithScanCancellation(t *testing.T) {
	db1, path := openQueueTestDB(t)
	seedMedia, scanID, _ := seedQueueTest(t, db1)
	libraryID := mediaLibraryID(t, db1, seedMedia)
	mediaID := insertQueueMedia(t, db1, libraryID, "fail-serial-cancel")
	q := NewQueue(db1, "fail-serial-owner", nil)
	if _, err := q.Enqueue(context.Background(), mediaID, &scanID, TaskPoster); err != nil {
		t.Fatal(err)
	}
	task, err := q.Claim(context.Background(), TaskPoster)
	if err != nil || task == nil {
		t.Fatalf("Claim=%+v,%v", task, err)
	}
	db2, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	transitionOpen := make(chan struct{})
	releaseTransition := make(chan struct{})
	var hookOnce sync.Once
	q.beforeFailTransition = func() { hookOnce.Do(func() { close(transitionOpen); <-releaseTransition }) }
	failDone := make(chan error, 1)
	go func() { failDone <- q.Fail(context.Background(), task, FailureRetryable, errors.New("retry")) }()
	<-transitionOpen
	cancelStarted := make(chan struct{})
	cancelDone := make(chan error, 1)
	go func() {
		close(cancelStarted)
		_, err := db2.Exec(`BEGIN IMMEDIATE`)
		if err == nil {
			_, err = db2.Exec(`UPDATE scan_task SET cancelled=1 WHERE id=?`, scanID)
			if err == nil {
				_, err = db2.Exec(`UPDATE post_ingest_task SET status='cancelled',finished_at=CURRENT_TIMESTAMP WHERE scan_task_id=? AND status='waiting'`, scanID)
			}
			if err == nil {
				_, err = db2.Exec(`COMMIT`)
			} else {
				_, _ = db2.Exec(`ROLLBACK`)
			}
		}
		cancelDone <- err
	}()
	<-cancelStarted
	close(releaseTransition)
	if err := <-failDone; err != nil {
		t.Fatal(err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := db1.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, task.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusCancelled {
		t.Fatalf("status=%s want cancelled", status)
	}
}

func TestRestartRecoveryPreservesWaitingAndRecoversExpiredRunning(t *testing.T) {
	db, path := openQueueTestDB(t)
	seedMedia, cancelledScanID, _ := seedQueueTest(t, db)
	libraryID := mediaLibraryID(t, db, seedMedia)
	waitingMedia := insertQueueMedia(t, db, libraryID, "restart-waiting")
	expiredMedia := insertQueueMedia(t, db, libraryID, "restart-expired")
	cancelledMedia := insertQueueMedia(t, db, libraryID, "restart-cancelled")
	waitingResult, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,status) VALUES(?,?,'waiting')`, waitingMedia, TaskPoster)
	if err != nil {
		t.Fatal(err)
	}
	waitingID, _ := waitingResult.LastInsertId()
	expiredResult, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,status,attempts,max_attempts,lease_owner,lease_until) VALUES(?,?,'running',1,3,'old-owner/expired',datetime(CURRENT_TIMESTAMP,'-1 second'))`, expiredMedia, TaskPoster)
	if err != nil {
		t.Fatal(err)
	}
	expiredID, _ := expiredResult.LastInsertId()
	cancelledResult, err := db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type,status,attempts,max_attempts,lease_owner,lease_until) VALUES(?,?,?,'running',1,3,'old-owner/cancelled',datetime(CURRENT_TIMESTAMP,'-1 second'))`, cancelledMedia, cancelledScanID, TaskPoster)
	if err != nil {
		t.Fatal(err)
	}
	cancelledID, _ := cancelledResult.LastInsertId()
	if _, err := db.Exec(`UPDATE scan_task SET cancelled=1,status='cancelled' WHERE id=?`, cancelledScanID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	q := NewQueue(reopened, "new-process", nil)
	if n, err := q.RecoverExpired(context.Background()); err != nil || n != 2 {
		t.Fatalf("RecoverExpired=(%d,%v) want (2,nil)", n, err)
	}
	for id, want := range map[int64]Status{waitingID: StatusWaiting, expiredID: StatusWaiting, cancelledID: StatusCancelled} {
		var status Status
		var attempts int
		var owner sql.NullString
		if err := reopened.QueryRow(`SELECT status,attempts,lease_owner FROM post_ingest_task WHERE id=?`, id).Scan(&status, &attempts, &owner); err != nil {
			t.Fatal(err)
		}
		if status != want || owner.Valid {
			t.Fatalf("id=%d status=%s owner=%v want=%s no owner", id, status, owner, want)
		}
		if id == expiredID && attempts != 1 {
			t.Fatalf("expired attempts=%d want 1", attempts)
		}
	}
	claimed, err := q.Claim(context.Background(), TaskPoster)
	if err != nil || claimed == nil || claimed.ID != waitingID || claimed.Attempts != 1 || !strings.HasPrefix(claimed.LeaseOwner, "new-process/") {
		t.Fatalf("new owner claim=%+v err=%v", claimed, err)
	}
}

func TestLeaseExpiryAttemptLimitFailsOnThirdExpiredLeaseAndFencesOldOwners(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, db)
	q := NewQueue(db, "restart-owner", nil)
	if inserted, err := q.Enqueue(context.Background(), mediaID, nil, TaskPreview); err != nil || !inserted {
		t.Fatalf("Enqueue=(%v,%v)", inserted, err)
	}
	var generations []*Task
	for attempt := 1; attempt <= 3; attempt++ {
		task, err := q.Claim(context.Background(), TaskPreview)
		if err != nil || task == nil || task.Attempts != attempt {
			t.Fatalf("claim %d=%+v,%v", attempt, task, err)
		}
		generations = append(generations, task)
		if _, err := db.Exec(`UPDATE post_ingest_task SET lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?`, task.ID); err != nil {
			t.Fatal(err)
		}
		if n, err := q.RecoverExpired(context.Background()); err != nil || n != 1 {
			t.Fatalf("recover %d=(%d,%v)", attempt, n, err)
		}
		var status Status
		var attempts int
		if err := db.QueryRow(`SELECT status,attempts FROM post_ingest_task WHERE id=?`, task.ID).Scan(&status, &attempts); err != nil {
			t.Fatal(err)
		}
		want := StatusWaiting
		if attempt == 3 {
			want = StatusFailed
		}
		if status != want || attempts != attempt {
			t.Fatalf("after expiry %d status=%s attempts=%d want=%s,%d", attempt, status, attempts, want, attempt)
		}
	}
	if next, err := q.Claim(context.Background(), TaskPreview); err != nil || next != nil {
		t.Fatalf("claim exhausted=%+v,%v", next, err)
	}
	for _, stale := range generations {
		if err := q.Complete(context.Background(), *stale); err == nil {
			t.Fatalf("stale owner %q completed terminal task", stale.LeaseOwner)
		}
	}
}

func TestSQLiteBusyIntegrationRetryExplicitRetriesRealWriterLock(t *testing.T) {
	bootstrap, path := openQueueTestDB(t)
	mediaID, _, _ := seedQueueTest(t, bootstrap)
	result, err := bootstrap.Exec(`INSERT INTO post_ingest_task(media_id,task_type,status) VALUES(?,?,'failed')`, mediaID, TaskAtrack)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := result.LastInsertId()
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}
	dsn := path + "?_pragma=busy_timeout(1)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	locker, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	conn, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(60 * time.Millisecond)
		_, err := conn.ExecContext(context.Background(), `COMMIT`)
		released <- err
	}()
	metrics := &store.SQLiteMetrics{}
	q := NewQueue(writer, "restart-writer", metrics)
	if err := q.RetryExplicit(context.Background(), taskID, nil); err != nil {
		t.Fatalf("RetryExplicit under real lock: %v", err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if metrics.BusyRetries.Load() == 0 || metrics.BusyExhausted.Load() != 0 {
		t.Fatalf("metrics retries=%d exhausted=%d", metrics.BusyRetries.Load(), metrics.BusyExhausted.Load())
	}
}
