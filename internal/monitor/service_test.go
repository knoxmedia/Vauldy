package monitor

import (
	"context"
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
}

func (s *scanSubmitterSpy) Submit(_ context.Context, req scancoord.ScanRequest) (scancoord.SubmitResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	select {
	case s.called <- struct{}{}:
	default:
	}
	return scancoord.SubmitResult{TaskID: 41, Started: true}, nil
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
	if monitorCount != 2 || scheduledCount != 1 {
		t.Fatalf("monitor=%d scheduled=%d requests=%+v", monitorCount, scheduledCount, spy.requests)
	}
}

func TestScanSourcesRealtimeWinsThenScheduledWhenDue(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "both.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	result, err := db.Exec(`INSERT INTO library(name,path,type,enabled,realtime_monitor,auto_scan) VALUES('both',?,'movie',1,1,1)`, root)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	spy := &scanSubmitterSpy{called: make(chan struct{}, 4)}
	service := NewService(db, spy, time.Hour)
	service.AutoScanInterval = time.Hour
	service.tick(context.Background())
	service.mu.Lock()
	service.lastAutoScan[id] = time.Now().Add(-2 * time.Hour)
	service.mu.Unlock()
	service.tick(context.Background())
	service.tick(context.Background())
	spy.mu.Lock()
	defer spy.mu.Unlock()
	want := []scancoord.Source{scancoord.SourceMonitor, scancoord.SourceScheduled, scancoord.SourceMonitor, scancoord.SourceScheduled, scancoord.SourceMonitor}
	if len(spy.requests) != len(want) {
		t.Fatalf("requests=%+v", spy.requests)
	}
	for i, source := range want {
		if spy.requests[i].Source != source {
			t.Fatalf("request %d source=%q want=%q", i, spy.requests[i].Source, source)
		}
	}
}
