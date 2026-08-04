package monitor

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"knox-media/internal/scancoord"
	"knox-media/internal/store"
)

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

func TestUnifiedScanSubmitterMonitorUsesMonitorSource(t *testing.T) {
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
	spy := &scanSubmitterSpy{called: make(chan struct{}, 1)}
	service := NewService(db, spy, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Start(ctx); close(done) }()
	select {
	case <-spy.called:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not submit scan")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop after context cancellation")
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.requests) == 0 {
		t.Fatal("monitor submitted no requests")
	}
	got := spy.requests[0]
	if got.LibraryID != libraryID || got.Source != scancoord.SourceMonitor {
		t.Fatalf("request=%+v", got)
	}
	if len(got.Roots) != 1 || got.Roots[0] != root {
		t.Fatalf("roots=%v", got.Roots)
	}
}

func TestScanSourcesDistinguishRealtimeAndAutoScan(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "sources.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	monitorRoot, scheduledRoot := t.TempDir(), t.TempDir()
	monResult, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('realtime',?,'movie',1,1,0)`, monitorRoot)
	if err != nil {
		t.Fatal(err)
	}
	monitorID, _ := monResult.LastInsertId()
	autoResult, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('auto',?,'movie',1,0,1)`, scheduledRoot)
	if err != nil {
		t.Fatal(err)
	}
	autoID, _ := autoResult.LastInsertId()
	spy := &scanSubmitterSpy{called: make(chan struct{}, 4)}
	service := NewService(db, spy, time.Hour)
	service.AutoScanInterval = time.Hour
	service.tick(context.Background())
	service.tick(context.Background())
	spy.mu.Lock()
	defer spy.mu.Unlock()
	var monitorCount, scheduledCount int
	for _, req := range spy.requests {
		switch req.LibraryID {
		case monitorID:
			if req.Source != scancoord.SourceMonitor {
				t.Fatalf("realtime source=%q", req.Source)
			}
			monitorCount++
		case autoID:
			if req.Source != scancoord.SourceScheduled {
				t.Fatalf("auto source=%q", req.Source)
			}
			scheduledCount++
		}
	}
	if monitorCount != 1 || scheduledCount != 1 {
		t.Fatalf("monitor=%d scheduled=%d requests=%+v", monitorCount, scheduledCount, spy.requests)
	}
}

func TestScanSourcesCoalesceRealtimeAndAutoScanPerTick(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "both.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fallback := filepath.Clean(filepath.Join(t.TempDir(), "fallback"))
	rootA := filepath.Clean(filepath.Join(t.TempDir(), "a"))
	rootB := filepath.Clean(filepath.Join(t.TempDir(), "b"))
	result, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('both',?,'movie',1,1,1)`, fallback)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	for i, folder := range []string{"  " + rootA + "  ", rootA, rootB} {
		if _, err := db.Exec(`INSERT INTO library_folder(library_id,path,sort_order) VALUES(?,?,?)`, id, folder, i); err != nil {
			t.Fatal(err)
		}
	}
	spy := &scanSubmitterSpy{called: make(chan struct{}, 4)}
	service := NewService(db, spy, time.Hour)
	service.AutoScanInterval = time.Hour
	service.MonitorScanInterval = time.Hour

	service.tick(context.Background())
	service.mu.Lock()
	service.lastAutoScan[id] = time.Now().Add(-2 * time.Hour)
	service.lastMonitorScan[id] = time.Now().Add(-2 * time.Hour)
	service.mu.Unlock()
	service.tick(context.Background())

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.requests) != 2 {
		t.Fatalf("requests=%+v want one per tick", spy.requests)
	}
	for i, req := range spy.requests {
		if req.LibraryID != id || req.Source != scancoord.SourceMonitor {
			t.Fatalf("request %d=%+v want realtime monitor precedence", i, req)
		}
		if len(req.Roots) != 2 || req.Roots[0] != rootA || req.Roots[1] != rootB {
			t.Fatalf("request %d roots=%q want normalized unique roots [%q %q]", i, req.Roots, rootA, rootB)
		}
	}
}

func TestAutoScanRetriesImmediatelyAfterSubmitFailureAndMarksAccepted(t *testing.T) {
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
	service.AutoScanInterval = time.Hour
	service.tick(context.Background())
	service.tick(context.Background())
	service.tick(context.Background())
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.requests) != 2 {
		t.Fatalf("requests=%d want failed retry then accepted suppression", len(spy.requests))
	}
	service.mu.Lock()
	_, marked := service.lastAutoScan[libraryID]
	service.mu.Unlock()
	if !marked {
		t.Fatal("accepted/coalesced nil submit did not advance auto-scan time")
	}
}

func TestAutoScanWithoutRootsDoesNotAdvance(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "auto-no-roots.sqlite"))
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
	service.tick(context.Background())
	service.mu.Lock()
	_, marked := service.lastAutoScan[libraryID]
	service.mu.Unlock()
	if marked {
		t.Fatal("no-roots scan advanced auto-scan time")
	}
}

func TestRealtimeMonitorThrottlesFullScansAndRetriesFailure(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "monitor-throttle.sqlite"))
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
	spy := &scanSubmitterSpy{errs: []error{errors.New("submit failed"), nil}}
	service := NewService(db, spy, time.Millisecond)
	service.MonitorScanInterval = time.Hour

	service.tick(context.Background()) // failure: must not advance the throttle
	service.tick(context.Background()) // success: records last submission
	service.tick(context.Background()) // suppressed within the interval

	spy.mu.Lock()
	requests := append([]scancoord.ScanRequest(nil), spy.requests...)
	spy.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests=%d want failed retry then suppression", len(requests))
	}
	for _, req := range requests {
		if req.LibraryID != libraryID || req.Source != scancoord.SourceMonitor {
			t.Fatalf("request=%+v", req)
		}
	}
	service.mu.Lock()
	_, marked := service.lastMonitorScan[libraryID]
	service.mu.Unlock()
	if !marked {
		t.Fatal("accepted monitor scan did not advance throttle")
	}
}
