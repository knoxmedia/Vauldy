package monitor

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sgtdi/fswatcher"
	"knox-media/internal/scancoord"
	"knox-media/internal/store"
)

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

type scanSubmitterSpy struct {
	mu       sync.Mutex
	requests []scancoord.ScanRequest
	called   chan struct{}
	errs     []error
}

func (s *scanSubmitterSpy) Submit(_ context.Context, req scancoord.ScanRequest) (scancoord.SubmitResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	var err error
	if len(s.errs) > 0 {
		err, s.errs = s.errs[0], s.errs[1:]
	}
	s.mu.Unlock()
	select {
	case s.called <- struct{}{}:
	default:
	}
	return scancoord.SubmitResult{TaskID: 41, Started: err == nil}, err
}

func (s *scanSubmitterSpy) requestsByLibrary() map[int64][]scancoord.ScanRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[int64][]scancoord.ScanRequest{}
	for _, r := range s.requests {
		m[r.LibraryID] = append(m[r.LibraryID], r)
	}
	return m
}

// fakeWatcher implements fswatcher.Watcher so tests can inject events without a
// real OS watcher.
type fakeWatcher struct {
	ch       chan fswatcher.WatchEvent
	dropped  chan fswatcher.WatchEvent
	paths    []string
	mu       sync.Mutex
	closed   bool
	started  chan struct{}
	watchErr error
}

func (fw *fakeWatcher) Watch(ctx context.Context) error {
	close(fw.started)
	<-ctx.Done()
	return fw.watchErr
}

func (fw *fakeWatcher) AddPath(path string, _ ...fswatcher.PathOption) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.closed {
		return errors.New("closed")
	}
	fw.paths = append(fw.paths, path)
	return nil
}

func (fw *fakeWatcher) DropPath(path string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	for i, p := range fw.paths {
		if p == path {
			fw.paths = append(fw.paths[:i], fw.paths[i+1:]...)
			return nil
		}
	}
	return nil
}

func (fw *fakeWatcher) Events() <-chan fswatcher.WatchEvent { return fw.ch }

func (fw *fakeWatcher) Dropped() <-chan fswatcher.WatchEvent { return fw.dropped }

func (fw *fakeWatcher) IsRunning() bool {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return !fw.closed
}

func (fw *fakeWatcher) Stats() fswatcher.WatcherStats { return fswatcher.WatcherStats{} }

func (fw *fakeWatcher) Paths() []string {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return append([]string(nil), fw.paths...)
}

func (fw *fakeWatcher) Log(_ fswatcher.Severity, _ string, _ ...any) {}

func (fw *fakeWatcher) Close() {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if !fw.closed {
		fw.closed = true
		close(fw.ch)
	}
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{
		ch:      make(chan fswatcher.WatchEvent, 64),
		dropped: make(chan fswatcher.WatchEvent, 10),
		started: make(chan struct{}),
	}
}

func makeEvent(path string, types ...fswatcher.EventType) fswatcher.WatchEvent {
	if len(types) == 0 {
		types = []fswatcher.EventType{fswatcher.EventCreate}
	}
	return fswatcher.WatchEvent{Path: path, Types: types, Time: time.Now()}
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestEventTriggersMonitorScan(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "monitor.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	result, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('mon',?,'movie',1,1,0)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := result.LastInsertId()
	spy := &scanSubmitterSpy{called: make(chan struct{}, 4)}
	fw := newFakeWatcher()
	orig := newWatcher
	newWatcher = func() (fswatcher.Watcher, error) { return fw, nil }
	defer func() { newWatcher = orig }()

	service := NewService(db, spy, time.Hour) // config ticker won't fire
	service.EventCooldown = 0                  // immediate
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.Start(ctx)

	<-fw.started // wait until Watch(ctx) begins

	// Send an event for a file inside the watched root.
	fw.ch <- makeEvent(filepath.Join(root, "movie.mkv"), fswatcher.EventCreate)

	select {
	case <-spy.called:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not submit scan after event")
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.requests) == 0 {
		t.Fatal("no requests submitted")
	}
	got := spy.requests[0]
	if got.LibraryID != libraryID || got.Source != scancoord.SourceMonitor {
		t.Fatalf("request=%+v", got)
	}
	if len(got.Roots) != 1 || got.Roots[0] != root {
		t.Fatalf("roots=%v", got.Roots)
	}
}

func TestEventDebouncedPerLibrary(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "debounce.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	result, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('mon',?,'movie',1,1,0)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := result.LastInsertId()
	spy := &scanSubmitterSpy{called: make(chan struct{}, 4)}
	fw := newFakeWatcher()
	orig := newWatcher
	newWatcher = func() (fswatcher.Watcher, error) { return fw, nil }
	defer func() { newWatcher = orig }()

	service := NewService(db, spy, time.Hour)
	// One-hour cooldown so only the first event triggers a scan.
	service.EventCooldown = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.Start(ctx)
	<-fw.started

	fw.ch <- makeEvent(filepath.Join(root, "a.mkv"), fswatcher.EventCreate)
	fw.ch <- makeEvent(filepath.Join(root, "b.mkv"), fswatcher.EventCreate)
	fw.ch <- makeEvent(filepath.Join(root, "c.mkv"), fswatcher.EventRemove)

	// The first event's submit should arrive; the rest are suppressed.
	select {
	case <-spy.called:
	case <-time.After(2 * time.Second):
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.requests) != 1 {
		t.Fatalf("requests=%d want 1 (debounced)", len(spy.requests))
	}
	if spy.requests[0].LibraryID != libraryID || spy.requests[0].Source != scancoord.SourceMonitor {
		t.Fatalf("request=%+v", spy.requests[0])
	}
}

func TestAutoScanSubmitsScheduledScans(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "autoscan.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	result, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('auto',?,'movie',1,0,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := result.LastInsertId()
	spy := &scanSubmitterSpy{called: make(chan struct{}, 4)}
	service := NewService(db, spy, time.Hour)
	service.AutoScanInterval = 0 // mark immediately due
	ctx := context.Background()
	service.submitAutoScans(ctx)

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.requests) != 1 {
		t.Fatalf("requests=%d want 1", len(spy.requests))
	}
	got := spy.requests[0]
	if got.LibraryID != libraryID || got.Source != scancoord.SourceScheduled {
		t.Fatalf("request=%+v", got)
	}
	if len(got.Roots) != 1 || got.Roots[0] != root {
		t.Fatalf("roots=%v", got.Roots)
	}
}

func TestAutoScanRetriesAfterFailure(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "auto-retry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	res, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('auto',?,'movie',1,0,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	spy := &scanSubmitterSpy{errs: []error{errors.New("submit failed"), nil}}
	service := NewService(db, spy, time.Hour)
	service.AutoScanInterval = 0
	ctx := context.Background()
	// First call fails – must not advance timestamp.
	service.submitAutoScans(ctx)
	// Second call succeeds and blocks further submissions.
	service.submitAutoScans(ctx)
	// Third call is suppressed.
	service.submitAutoScans(ctx)

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.requests) != 2 {
		t.Fatalf("requests=%d want 2 (fail + success)", len(spy.requests))
	}
	service.mu.Lock()
	_, marked := service.lastAutoScan[libraryID]
	service.mu.Unlock()
	if !marked {
		t.Fatal("accepted submit did not mark auto-scan time")
	}
}

func TestAutoScanWithoutRootsDoesNotAdvance(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "auto-noroots.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('auto','','movie',1,0,1)`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	service := NewService(db, &scanSubmitterSpy{}, time.Hour)
	service.AutoScanInterval = 0
	service.submitAutoScans(context.Background())
	service.mu.Lock()
	_, marked := service.lastAutoScan[libraryID]
	service.mu.Unlock()
	if marked {
		t.Fatal("no-roots scan advanced auto-scan time")
	}
}

func TestStartupAndShutdown(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "startup.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if _, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('mon',?,'movie',1,1,1)`, root); err != nil {
		t.Fatal(err)
	}
	fw := newFakeWatcher()
	orig := newWatcher
	newWatcher = func() (fswatcher.Watcher, error) { return fw, nil }
	defer func() { newWatcher = orig }()

	spy := &scanSubmitterSpy{called: make(chan struct{}, 4)}
	service := NewService(db, spy, time.Hour)
	service.AutoScanInterval = 0 // immediate auto-scan
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Start(ctx); close(done) }()

	// The initial submitAutoScans fires immediately; wait for it.
	select {
	case <-spy.called:
	case <-time.After(2 * time.Second):
		t.Fatal("auto-scan did not fire at startup")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("service did not shut down")
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.requests) < 1 {
		t.Fatal("expected at least one scheduled scan")
	}
}
