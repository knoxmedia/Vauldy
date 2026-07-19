package postingest

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"knox-media/internal/scancoord"
	"knox-media/internal/scanner"
)

type executorFunc func(context.Context, Task) error

func (f executorFunc) Execute(ctx context.Context, task Task) error { return f(ctx, task) }

func enqueueDispatcherTasks(t *testing.T, q *Queue, n int, types ...TaskType) []int64 {
	t.Helper()
	db := q.db
	seed, _, _ := seedQueueTest(t, db)
	libraryID := mediaLibraryID(t, db, seed)
	ids := make([]int64, 0, n*len(types))
	for i := 0; i < n; i++ {
		for _, typ := range types {
			mid := insertQueueMedia(t, db, libraryID, fmt.Sprintf("dispatcher-%s-%d", typ, i))
			res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type) VALUES (?,?)`, mid, typ)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			ids = append(ids, id)
		}
	}
	return ids
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestDefaultDispatcherOptions(t *testing.T) {
	o := DefaultDispatcherOptions()
	wantGlobal := runtime.NumCPU() / 2
	if wantGlobal < 2 {
		wantGlobal = 2
	}
	if wantGlobal > 4 {
		wantGlobal = 4
	}
	if o.Global != wantGlobal || o.Poster != min(2, wantGlobal) || o.Preview != 1 {
		t.Fatalf("budgets=%d/%d/%d", o.Global, o.Poster, o.Preview)
	}
	wants := map[TaskType]time.Duration{TaskPoster: 2 * time.Minute, TaskPreview: 30 * time.Minute, TaskKeyframe: 30 * time.Minute, TaskAtrack: 30 * time.Minute, TaskSubtitle: 60 * time.Minute}
	for typ, want := range wants {
		if o.Timeouts[typ] != want {
			t.Fatalf("timeout %s=%v", typ, o.Timeouts[typ])
		}
	}
}

func TestDispatcher_RejectsInvalidBudget(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "owner", nil)
	exec := executorFunc(func(context.Context, Task) error { return nil })
	valid := DefaultDispatcherOptions()
	valid.OwnerID = "owner"
	cases := []struct {
		name, field string
		mutate      func(*DispatcherOptions)
	}{
		{"global zero", "Global", func(o *DispatcherOptions) { o.Global = 0 }}, {"global high", "Global", func(o *DispatcherOptions) { o.Global = 33 }},
		{"poster zero", "Poster", func(o *DispatcherOptions) { o.Poster = 0 }}, {"poster high", "Poster", func(o *DispatcherOptions) { o.Poster = 3 }}, {"poster global", "Poster", func(o *DispatcherOptions) { o.Global = 1; o.Poster = 2 }},
		{"preview zero", "Preview", func(o *DispatcherOptions) { o.Preview = 0 }}, {"preview high", "Preview", func(o *DispatcherOptions) { o.Preview = 3 }}, {"preview global", "Preview", func(o *DispatcherOptions) { o.Global = 1; o.Poster = 1; o.Preview = 2 }},
		{"owner empty", "OwnerID", func(o *DispatcherOptions) { o.OwnerID = "" }}, {"owner slash", "OwnerID", func(o *DispatcherOptions) { o.OwnerID = "a/b" }},
		{"poll", "PollInterval", func(o *DispatcherOptions) { o.PollInterval = 0 }}, {"lease", "LeaseDuration", func(o *DispatcherOptions) { o.LeaseDuration = time.Second }},
		{"heartbeat", "HeartbeatInterval", func(o *DispatcherOptions) { o.HeartbeatInterval = o.LeaseDuration }}, {"timeouts missing", "Timeouts", func(o *DispatcherOptions) { delete(o.Timeouts, TaskPoster) }},
		{"timeout zero", "Timeouts", func(o *DispatcherOptions) { o.Timeouts[TaskPoster] = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := valid
			o.Timeouts = cloneTimeouts(valid.Timeouts)
			tc.mutate(&o)
			_, err := NewDispatcher(q, exec, o)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("err=%v want field %s", err, tc.field)
			}
		})
	}
	if _, err := NewDispatcher(nil, exec, valid); err == nil || !strings.Contains(err.Error(), "Queue") {
		t.Fatalf("nil queue err=%v", err)
	}
	if _, err := NewDispatcher(q, nil, valid); err == nil || !strings.Contains(err.Error(), "Executor") {
		t.Fatalf("nil executor err=%v", err)
	}
}

func cloneTimeouts(in map[TaskType]time.Duration) map[TaskType]time.Duration {
	out := map[TaskType]time.Duration{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestDispatcher_BudgetsBeforeClaim(t *testing.T) {
	for _, previewLimit := range []int{1, 2} {
		t.Run(fmt.Sprintf("preview-%d", previewLimit), func(t *testing.T) {
			db, _ := openQueueTestDB(t)
			q := NewQueue(db, "owner", nil)
			enqueueDispatcherTasks(t, q, 5, TaskPoster, TaskPreview, TaskKeyframe)
			var mu sync.Mutex
			current := map[TaskType]int{}
			peak := map[TaskType]int{}
			global, globalPeak := 0, 0
			release := make(chan struct{})
			exec := executorFunc(func(ctx context.Context, task Task) error {
				mu.Lock()
				current[task.Type]++
				global++
				if current[task.Type] > peak[task.Type] {
					peak[task.Type] = current[task.Type]
				}
				if global > globalPeak {
					globalPeak = global
				}
				mu.Unlock()
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			o := DefaultDispatcherOptions()
			o.OwnerID = "owner"
			o.Global = 4
			o.Poster = 2
			o.Preview = previewLimit
			o.PollInterval = 5 * time.Millisecond
			d, err := NewDispatcher(q, exec, o)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- d.Start(ctx) }()
			waitUntil(t, time.Second, func() bool { return d.Snapshot().GlobalUsed == 4 })
			time.Sleep(30 * time.Millisecond)
			s := d.Snapshot()
			mu.Lock()
			gp, pp, vp := globalPeak, peak[TaskPoster], peak[TaskPreview]
			mu.Unlock()
			if gp > o.Global || pp > 2 || vp > previewLimit || s.GlobalUsed > o.Global {
				t.Fatalf("peaks global=%d poster=%d preview=%d snapshot=%+v", gp, pp, vp, s)
			}
			var waiting int
			if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE status='waiting'`).Scan(&waiting); err != nil || waiting == 0 {
				t.Fatalf("waiting=%d err=%v", waiting, err)
			}
			close(release)
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDispatcher_TypeTimeouts(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "owner", nil)
	ids := enqueueDispatcherTasks(t, q, 1, TaskPoster, TaskPreview)
	observed := make(chan TaskType, 2)
	exec := executorFunc(func(ctx context.Context, task Task) error { <-ctx.Done(); observed <- task.Type; return ctx.Err() })
	o := DefaultDispatcherOptions()
	o.OwnerID = "owner"
	o.PollInterval = 5 * time.Millisecond
	o.Timeouts[TaskPoster] = 20 * time.Millisecond
	o.Timeouts[TaskPreview] = 35 * time.Millisecond
	d, err := NewDispatcher(q, exec, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	for range ids {
		select {
		case <-observed:
		case <-time.After(time.Second):
			t.Fatal("timeout not observed")
		}
	}
	waitUntil(t, time.Second, func() bool {
		var running int
		if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE status='running'`).Scan(&running); err != nil {
			return false
		}
		return running == 0
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		var status Status
		var attempts int
		var last string
		if err := db.QueryRow(`SELECT status,attempts,last_error FROM post_ingest_task WHERE id=?`, id).Scan(&status, &attempts, &last); err != nil {
			t.Fatal(err)
		}
		if status != StatusWaiting || attempts != 1 || !strings.Contains(strings.ToLower(last), "deadline") {
			t.Fatalf("task %d status=%s attempts=%d last=%q", id, status, attempts, last)
		}
	}
}

func TestDispatcher_ShutdownWaitsAndSnapshotSafe(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "owner", nil)
	enqueueDispatcherTasks(t, q, 2, TaskKeyframe)
	entered := make(chan struct{}, 2)
	exited := make(chan struct{}, 2)
	exec := executorFunc(func(ctx context.Context, _ Task) error {
		entered <- struct{}{}
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		exited <- struct{}{}
		return errors.New("stopped")
	})
	o := DefaultDispatcherOptions()
	o.OwnerID = "owner"
	o.Global = 2
	o.PollInterval = 5 * time.Millisecond
	d, err := NewDispatcher(q, exec, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	<-entered
	<-entered
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = d.Snapshot()
			}
		}()
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if len(exited) != 2 || d.Snapshot().GlobalUsed != 0 {
		t.Fatalf("workers not converged snapshot=%+v exited=%d", d.Snapshot(), len(exited))
	}
	if err := d.Start(context.Background()); err == nil {
		t.Fatal("second Start accepted")
	}
}

func dispatcherOptions(owner string) DispatcherOptions {
	o := DefaultDispatcherOptions()
	o.OwnerID = owner
	o.PollInterval = 5 * time.Millisecond
	return o
}

func TestDispatcher_RoundRobinPersistsAcrossSlots(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "owner", nil)
	enqueueDispatcherTasks(t, q, 12, taskTypes...)
	started := make(chan TaskType, 20)
	release := make(chan struct{}, 20)
	exec := executorFunc(func(ctx context.Context, task Task) error {
		started <- task.Type
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	o := dispatcherOptions("owner")
	o.Global = 1
	o.Poster = 1
	o.Preview = 1
	d, err := NewDispatcher(q, exec, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	seen := map[TaskType]bool{}
	for i := 0; i < 6; i++ {
		select {
		case typ := <-started:
			seen[typ] = true
			release <- struct{}{}
		case <-time.After(time.Second):
			t.Fatal("scheduler stalled")
		}
	}
	for _, typ := range taskTypes {
		if !seen[typ] {
			t.Fatalf("first six starts=%v missing %s", seen, typ)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_AllTypeTimeoutDeadlines(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "owner", nil)
	enqueueDispatcherTasks(t, q, 1, taskTypes...)
	type observation struct {
		typ       TaskType
		remaining time.Duration
	}
	observed := make(chan observation, len(taskTypes))
	exec := executorFunc(func(ctx context.Context, task Task) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("missing deadline")
		}
		observed <- observation{task.Type, time.Until(deadline)}
		<-ctx.Done()
		return ctx.Err()
	})
	o := dispatcherOptions("owner")
	o.Global = 6
	o.Poster = 1
	o.Preview = 1
	for i, typ := range taskTypes {
		o.Timeouts[typ] = time.Duration(300+i*20) * time.Millisecond
	}
	d, err := NewDispatcher(q, exec, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	seen := map[TaskType]bool{}
	for range taskTypes {
		select {
		case got := <-observed:
			want := o.Timeouts[got.typ]
			if got.remaining <= 0 || got.remaining > want+50*time.Millisecond {
				t.Fatalf("%s remaining=%v want near %v", got.typ, got.remaining, want)
			}
			seen[got.typ] = true
		case <-time.After(time.Second):
			t.Fatal("missing deadline")
		}
	}
	for _, typ := range taskTypes {
		if !seen[typ] {
			t.Fatalf("missing %s deadline", typ)
		}
	}
	waitUntil(t, 3*time.Second, func() bool {
		var running int
		if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE status='running'`).Scan(&running); err != nil {
			return false
		}
		return running == 0
	})
	cancel()
	<-done
}

func insertDispatcherTask(t *testing.T, q *Queue, typ TaskType, scanID *int64, suffix string) int64 {
	t.Helper()
	var libraryID int64
	if err := q.db.QueryRow(`SELECT id FROM library ORDER BY id LIMIT 1`).Scan(&libraryID); err != nil {
		seed, _, _ := seedQueueTest(t, q.db)
		libraryID = mediaLibraryID(t, q.db, seed)
	}
	mid := insertQueueMedia(t, q.db, libraryID, "dispatcher-extra-"+suffix)
	res, err := q.db.Exec(`INSERT INTO post_ingest_task(media_id,scan_task_id,task_type) VALUES (?,?,?)`, mid, nullableInt64(scanID), typ)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestClassifiedError_UnwrapsValue(t *testing.T) {
	cause := errors.New("cause")
	if !errors.Is(ClassifiedError{Kind: FailurePermanent, Err: cause}, cause) {
		t.Fatal("ClassifiedError value does not unwrap")
	}
}

func TestDispatcher_HeartbeatRenewsAndObservesScanCancellation(t *testing.T) {
	t.Run("renews", func(t *testing.T) {
		db, _ := openQueueTestDB(t)
		q := NewQueue(db, "owner", nil)
		id := insertDispatcherTask(t, q, TaskKeyframe, nil, "renew")
		entered := make(chan struct{})
		exec := executorFunc(func(ctx context.Context, _ Task) error { close(entered); <-ctx.Done(); return ctx.Err() })
		o := dispatcherOptions("owner")
		o.HeartbeatInterval = 50 * time.Millisecond
		d, _ := NewDispatcher(q, exec, o)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- d.Start(ctx) }()
		<-entered
		if _, err := db.Exec(`UPDATE post_ingest_task SET lease_until=datetime(CURRENT_TIMESTAMP,'+1 second') WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		var shortened time.Time
		_ = db.QueryRow(`SELECT lease_until FROM post_ingest_task WHERE id=?`, id).Scan(&shortened)
		waitUntil(t, time.Second, func() bool {
			var renewed time.Time
			if db.QueryRow(`SELECT lease_until FROM post_ingest_task WHERE id=?`, id).Scan(&renewed) != nil {
				return false
			}
			return renewed.After(shortened.Add(time.Second))
		})
		cancel()
		<-done
	})
	t.Run("cross process scan cancellation", func(t *testing.T) {
		db, _ := openQueueTestDB(t)
		_, scan, _ := seedQueueTest(t, db)
		q := NewQueue(db, "owner", nil)
		id := insertDispatcherTask(t, q, TaskSubtitle, &scan, "remote-cancel")
		cancelled := make(chan struct{})
		exec := executorFunc(func(ctx context.Context, _ Task) error { <-ctx.Done(); close(cancelled); return ctx.Err() })
		o := dispatcherOptions("owner")
		o.HeartbeatInterval = 50 * time.Millisecond
		d, _ := NewDispatcher(q, exec, o)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- d.Start(ctx) }()
		t.Cleanup(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("dispatcher shutdown: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("dispatcher did not stop during cleanup")
			}
		})
		waitUntil(t, 5*time.Second, func() bool { return d.Snapshot().GlobalUsed == 1 })
		if _, err := db.Exec(`UPDATE scan_task SET cancelled=1 WHERE id=?`, scan); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, 5*time.Second, func() bool {
			var s Status
			_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&s)
			return s == StatusCancelled
		})
		select {
		case <-cancelled:
		default:
			// Persistent cancellation can be observed by preflight before executor start.
		}
	})
}

func TestDispatcher_CancelScanOnlyLocalMatchingScan(t *testing.T) {
	db, _ := openQueueTestDB(t)
	_, scanOne, scanTwo := seedQueueTest(t, db)
	q := NewQueue(db, "owner", nil)
	idOne := insertDispatcherTask(t, q, TaskPoster, &scanOne, "local-one")
	idTwo := insertDispatcherTask(t, q, TaskPreview, &scanTwo, "local-two")
	cancelled := make(chan int64, 2)
	exec := executorFunc(func(ctx context.Context, task Task) error {
		<-ctx.Done()
		cancelled <- *task.ScanTaskID
		return ctx.Err()
	})
	o := dispatcherOptions("owner")
	o.Global = 2
	d, _ := NewDispatcher(q, exec, o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	waitUntil(t, time.Second, func() bool { return d.Snapshot().GlobalUsed == 2 })
	d.CancelScan(scanOne)
	waitUntil(t, time.Second, func() bool {
		var s Status
		_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, idOne).Scan(&s)
		return s == StatusCancelled
	})
	var other Status
	_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, idTwo).Scan(&other)
	if other != StatusRunning {
		t.Fatalf("other scan status=%s", other)
	}
	select {
	case got := <-cancelled:
		if got != scanOne {
			t.Fatalf("cancelled scan %d", got)
		}
	default:
		// Cancellation after registration but before executor start intentionally skips execution.
	}
	cancel()
	<-done
}

func TestDispatcher_ErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		want      Status
		keepLease bool
	}{{"ordinary", errors.New("temporary"), StatusWaiting, false}, {"permanent value", ClassifiedError{FailurePermanent, errors.New("bad")}, StatusFailed, false}, {"permanent pointer", &ClassifiedError{FailurePermanent, errors.New("bad")}, StatusFailed, false}, {"cancelled value", ClassifiedError{FailureCancelled, errors.New("stop")}, StatusCancelled, false}, {"cancelled pointer", &ClassifiedError{FailureCancelled, errors.New("stop")}, StatusCancelled, false}, {"shutdown value", ClassifiedError{FailureShutdown, errors.New("shutdown")}, StatusRunning, true}, {"shutdown pointer", &ClassifiedError{FailureShutdown, errors.New("shutdown")}, StatusRunning, true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := openQueueTestDB(t)
			q := NewQueue(db, "owner", nil)
			id := insertDispatcherTask(t, q, TaskAtrack, nil, "classify-"+tc.name)
			exec := executorFunc(func(context.Context, Task) error { return tc.err })
			o := dispatcherOptions("owner")
			d, _ := NewDispatcher(q, exec, o)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- d.Start(ctx) }()
			waitUntil(t, time.Second, func() bool {
				var s Status
				_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&s)
				return s == tc.want
			})
			waitUntil(t, 10*time.Second, func() bool {
				var owner *string
				if db.QueryRow(`SELECT lease_owner FROM post_ingest_task WHERE id=?`, id).Scan(&owner) != nil {
					return false
				}
				return (owner != nil) == tc.keepLease
			})
			var owner *string
			_ = db.QueryRow(`SELECT lease_owner FROM post_ingest_task WHERE id=?`, id).Scan(&owner)
			if (owner != nil) != tc.keepLease {
				t.Fatalf("lease owner=%v keep=%v", owner, tc.keepLease)
			}
			cancel()
			<-done
		})
	}
}

func TestDispatcher_StartRecoversBeforeExecutingAndReturnsRecoveryError(t *testing.T) {
	t.Run("recovers", func(t *testing.T) {
		db, _ := openQueueTestDB(t)
		q := NewQueue(db, "owner", nil)
		id := insertDispatcherTask(t, q, TaskAtrack, nil, "recover")
		if _, err := db.Exec(`UPDATE post_ingest_task SET status='running',attempts=1,lease_owner='old/token',lease_until=datetime(CURRENT_TIMESTAMP,'-1 second') WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		executed := make(chan struct{})
		exec := executorFunc(func(context.Context, Task) error { close(executed); return nil })
		o := dispatcherOptions("owner")
		d, _ := NewDispatcher(q, exec, o)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- d.Start(ctx) }()
		select {
		case <-executed:
		case <-time.After(time.Second):
			t.Fatal("recovered task not executed")
		}
		cancel()
		<-done
	})
	t.Run("recovery error", func(t *testing.T) {
		db, _ := openQueueTestDB(t)
		q := NewQueue(db, "owner", nil)
		_ = db.Close()
		var called bool
		exec := executorFunc(func(context.Context, Task) error { called = true; return nil })
		o := dispatcherOptions("owner")
		d, _ := NewDispatcher(q, exec, o)
		if err := d.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "recover") {
			t.Fatalf("Start error=%v", err)
		}
		if called {
			t.Fatal("executor called after recovery failure")
		}
	})
}

func TestDispatcher_RejectsInvalidExecutorStopGrace(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "owner", nil)
	o := dispatcherOptions("owner")
	o.ExecutorStopGrace = 0
	_, err := NewDispatcher(q, executorFunc(func(context.Context, Task) error { return nil }), o)
	if err == nil || !strings.Contains(err.Error(), "ExecutorStopGrace") {
		t.Fatalf("error=%v", err)
	}
}

func TestDispatcher_UnresponsiveExecutorHasBoundedShutdown(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "owner", nil)
	id := insertDispatcherTask(t, q, TaskKeyframe, nil, "unresponsive-shutdown")
	release := make(chan struct{})
	entered := make(chan struct{})
	exec := executorFunc(func(context.Context, Task) error { close(entered); <-release; return nil })
	o := dispatcherOptions("owner")
	o.ExecutorStopGrace = 30 * time.Millisecond
	d, err := NewDispatcher(q, exec, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	<-entered
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Start did not stop within grace")
	}
	if elapsed := time.Since(started); elapsed < o.ExecutorStopGrace || elapsed > 250*time.Millisecond {
		t.Fatalf("shutdown elapsed=%v", elapsed)
	}
	if d.Snapshot().GlobalUsed != 1 {
		t.Fatalf("unresponsive executor released budget: %+v", d.Snapshot())
	}
	var status Status
	var last string
	var owner *string
	if err := db.QueryRow(`SELECT status,last_error,lease_owner FROM post_ingest_task WHERE id=?`, id).Scan(&status, &last, &owner); err != nil {
		t.Fatal(err)
	}
	if status != StatusRunning || owner == nil || !strings.Contains(last, "executor did not stop") {
		t.Fatalf("status=%s owner=%v last=%q", status, owner, last)
	}
	close(release)
	waitUntil(t, time.Second, func() bool { return d.Snapshot().GlobalUsed == 0 })
}

func TestDispatcher_UnresponsiveExecutorTimeoutRetainsBudget(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "owner", nil)
	firstID := insertDispatcherTask(t, q, TaskPreview, nil, "unresponsive-timeout-first")
	insertDispatcherTask(t, q, TaskKeyframe, nil, "unresponsive-timeout-second")
	release := make(chan struct{})
	started := make(chan Task, 2)
	exec := executorFunc(func(_ context.Context, task Task) error {
		started <- task
		if task.ID == firstID {
			<-release
		}
		return nil
	})
	o := dispatcherOptions("owner")
	o.Global = 1
	o.Poster = 1
	o.Preview = 1
	o.PollInterval = 20 * time.Millisecond
	o.Timeouts[TaskPreview] = 30 * time.Millisecond
	o.ExecutorStopGrace = 30 * time.Millisecond
	d, err := NewDispatcher(q, exec, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	if first := <-started; first.ID != firstID {
		t.Fatalf("first task=%d want %d", first.ID, firstID)
	}
	time.Sleep(150 * time.Millisecond)
	if got := d.Snapshot().GlobalUsed; got != 1 {
		t.Fatalf("GlobalUsed=%d want 1 while executor runs", got)
	}
	select {
	case second := <-started:
		t.Fatalf("second task started while first executor runs: %d", second.ID)
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Start did not stop")
	}
	if d.Snapshot().GlobalUsed != 1 {
		t.Fatalf("shutdown released live executor budget: %+v", d.Snapshot())
	}
	close(release)
	waitUntil(t, time.Second, func() bool { return d.Snapshot().GlobalUsed == 0 })
}

func TestDispatcher_CancelScanFencesSuccessfulExecutorBeforeComplete(t *testing.T) {
	db, _ := openQueueTestDB(t)
	_, scan, _ := seedQueueTest(t, db)
	q := NewQueue(db, "fence-owner", nil)
	id := insertDispatcherTask(t, q, TaskKeyframe, &scan, "cancel-fence")
	entered := make(chan struct{})
	release := make(chan struct{})
	exec := executorFunc(func(context.Context, Task) error {
		close(entered)
		<-release
		return nil
	})
	o := dispatcherOptions("fence-owner")
	o.HeartbeatInterval = 30 * time.Second
	d, err := NewDispatcher(q, exec, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	<-entered
	if _, err := db.Exec(`UPDATE scan_task SET cancelled=1 WHERE id=?`, scan); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitUntil(t, time.Second, func() bool {
		var status Status
		_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status)
		return status != StatusRunning
	})
	var status Status
	if err := db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusCancelled {
		t.Fatalf("status=%s want cancelled", status)
	}
	cancel()
	<-done
}

func TestDispatcher_UnregisterIsGenerationSafe(t *testing.T) {
	scanID := int64(55)
	oldState := &workerState{}
	currentState := &workerState{}
	d := &Dispatcher{
		running: map[int64]*workerState{7: currentState},
		scans:   map[int64]map[int64]*workerState{scanID: {7: currentState}},
	}
	d.unregister(Task{ID: 7, ScanTaskID: &scanID}, oldState)
	if d.running[7] != currentState || d.scans[scanID][7] != currentState {
		t.Fatal("stale worker unregistered the current generation")
	}
}

type dispatcherNoopScanner struct{}

func (dispatcherNoopScanner) ScanLibraryFoldersWithContextAndCallbacks(context.Context, int64, []string, scanner.ScanCallbacks) (int, error) {
	return 0, nil
}

func TestDispatcher_CancelBetweenClaimAndRegisterSkipsExecutor(t *testing.T) {
	db, _ := openQueueTestDB(t)
	_, scanID, _ := seedQueueTest(t, db)
	q := NewQueue(db, "claim-register-owner", nil)
	taskID := insertDispatcherTask(t, q, TaskPoster, &scanID, "claim-register")
	var executorCalls int
	exec := executorFunc(func(context.Context, Task) error {
		executorCalls++
		return nil
	})
	o := dispatcherOptions("claim-register-owner")
	d, err := NewDispatcher(q, exec, o)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(chan struct{})
	releaseRegister := make(chan struct{})
	d.beforeRegister = func(task Task) {
		if task.ID == taskID {
			close(claimed)
			<-releaseRegister
		}
	}
	coordinator, err := scancoord.New(db, scancoord.Options{
		OwnerInstanceID: "claim-register-coordinator",
		Scanner:         dispatcherNoopScanner{},
		OnScanCancelled: func(_ context.Context, id int64) error { d.CancelScan(id); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	<-claimed
	if _, err := coordinator.Cancel(context.Background(), scanID); err != nil {
		t.Fatal(err)
	}
	close(releaseRegister)
	waitUntil(t, time.Second, func() bool {
		var status Status
		_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, taskID).Scan(&status)
		return status == StatusCancelled
	})
	if executorCalls != 0 {
		t.Fatalf("executor calls=%d want 0", executorCalls)
	}
	stop()
	<-done
}

func TestDispatcher_NilScanTaskSkipsInitialCancellationQuery(t *testing.T) {
	db, _ := openQueueTestDB(t)
	q := NewQueue(db, "nil-scan-owner", nil)
	id := insertDispatcherTask(t, q, TaskPoster, nil, "nil-scan")
	queries := 0
	q.isScanCancelled = func(context.Context, int64) (bool, error) { queries++; return false, nil }
	executed := make(chan struct{})
	exec := executorFunc(func(context.Context, Task) error { close(executed); return nil })
	d, err := NewDispatcher(q, exec, dispatcherOptions("nil-scan-owner"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	select {
	case <-executed:
	case <-time.After(time.Second):
		t.Fatal("nil scan task was not executed")
	}
	waitUntil(t, time.Second, func() bool {
		var status Status
		_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status)
		return status == StatusDone
	})
	if queries != 0 {
		t.Fatalf("cancellation queries=%d want 0", queries)
	}
	stop()
	<-done
}

func TestDispatcher_InitialCancellationReadErrorSkipsExecutor(t *testing.T) {
	db, _ := openQueueTestDB(t)
	_, scanID, _ := seedQueueTest(t, db)
	q := NewQueue(db, "read-error-owner", nil)
	id := insertDispatcherTask(t, q, TaskPoster, &scanID, "read-error")
	q.isScanCancelled = func(context.Context, int64) (bool, error) {
		return false, errors.New("injected cancellation read error")
	}
	calls := 0
	d, err := NewDispatcher(q, executorFunc(func(context.Context, Task) error { calls++; return nil }), dispatcherOptions("read-error-owner"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	waitUntil(t, time.Second, func() bool {
		var status Status
		_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, id).Scan(&status)
		return status == StatusWaiting
	})
	if calls != 0 {
		t.Fatalf("executor calls=%d want 0", calls)
	}
	stop()
	<-done
}
