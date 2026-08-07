package monitor

import (
	"context"
	"database/sql"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sgtdi/fswatcher"
	"knox-media/internal/scancoord"
)

const defaultAutoScanInterval = 5 * time.Minute
const defaultEventCooldown = 30 * time.Second

type ScanSubmitter interface {
	Submit(context.Context, scancoord.ScanRequest) (scancoord.SubmitResult, error)
}

type Service struct {
	DB        *sql.DB
	Submitter ScanSubmitter
	Interval  time.Duration
	// AutoScanInterval controls periodic scheduled submissions when auto_scan=1.
	AutoScanInterval time.Duration
	// EventCooldown prevents the same library from being scanned more than
	// once within this duration by filesystem events.
	EventCooldown time.Duration

	mu            sync.Mutex
	lastAutoScan  map[int64]time.Time
	lastEventScan map[int64]time.Time
	// libFolders maps libraryID → watched root folders.
	libFolders map[int64][]string
}

// newWatcher is a factory that can be overridden in tests.
var newWatcher = func() (fswatcher.Watcher, error) {
	return fswatcher.New(
		fswatcher.WithCooldown(200*time.Millisecond),
		fswatcher.WithBufferSize(512),
	)
}

func NewService(db *sql.DB, submitter ScanSubmitter, interval time.Duration) *Service {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Service{
		DB:               db,
		Submitter:        submitter,
		Interval:         interval,
		AutoScanInterval: defaultAutoScanInterval,
		EventCooldown:    defaultEventCooldown,
		lastAutoScan:     make(map[int64]time.Time),
		lastEventScan:    make(map[int64]time.Time),
		libFolders:       make(map[int64][]string),
	}
}

// Start initialises a filesystem watcher on libraries with realtime_monitor,
// then merges event-driven submissions (with per-library cooldown) and periodic
// auto-scan submissions into one loop that also periodically reconciles library
// configuration against the currently watched paths. When the file watcher
// cannot be created the service falls back to auto-scan only.
func (s *Service) Start(ctx context.Context) {
	configTicker := time.NewTicker(s.Interval)
	defer configTicker.Stop()

	autoTicker := time.NewTicker(time.Minute)
	defer autoTicker.Stop()

	w, err := newWatcher()
	if err != nil {
		log.Printf("monitor: file watcher unavailable: %v; falling back to auto-scan only", err)
		// Fallback: periodic auto-scans without file watching.
		s.submitAutoScans(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-autoTicker.C:
				s.submitAutoScans(ctx)
			}
		}
	}
	go func() {
		if watchErr := w.Watch(ctx); watchErr != nil && ctx.Err() == nil {
			log.Printf("monitor: watcher exited: %v", watchErr)
		}
	}()

	// Seed initial watch set before entering the loop so the first
	// auto-scan tick does not duplicate a monitor submission.
	s.reconcileWatchPaths(ctx, w)
	s.submitAutoScans(ctx)

	for {
		select {
		case <-ctx.Done():
			w.Close()
			return
		case evt := <-w.Events():
			s.handleEvent(ctx, evt)
		case <-configTicker.C:
			s.reconcileWatchPaths(ctx, w)
		case <-autoTicker.C:
			s.submitAutoScans(ctx)
		}
	}
}

// handleEvent maps a filesystem event path to zero or more library IDs and
// submits a monitor scan for each, subject to EventCooldown.
func (s *Service) handleEvent(ctx context.Context, evt fswatcher.WatchEvent) {
	now := time.Now()
	s.mu.Lock()
	libs := s.lookupLibrariesForPath(evt.Path)
	type pair struct{ id int64; folders []string }
	var due []pair
	for _, libID := range libs {
		last, ok := s.lastEventScan[libID]
		if ok && now.Sub(last) < s.EventCooldown {
			continue
		}
		s.lastEventScan[libID] = now
		due = append(due, pair{id: libID, folders: s.libFolders[libID]})
	}
	s.mu.Unlock()
	for _, p := range due {
		if err := s.submitScan(ctx, p.id, scancoord.SourceMonitor, p.folders); err != nil {
			log.Printf("monitor: event scan submit library=%d err=%v", p.id, err)
		}
	}
}

// lookupLibrariesForPath returns every library whose watched folders contain path.
// Caller must hold s.mu.
func (s *Service) lookupLibrariesForPath(path string) []int64 {
	clean := filepath.Clean(path)
	var ids []int64
	for libID, folders := range s.libFolders {
		for _, folder := range folders {
			if clean == folder || strings.HasPrefix(clean, folder+string(filepath.Separator)) {
				ids = append(ids, libID)
				break
			}
		}
	}
	return ids
}

// reconcileWatchPaths queries enabled libraries with realtime_monitor and adds
// or removes watched paths so the watcher matches current configuration.
func (s *Service) reconcileWatchPaths(ctx context.Context, w fswatcher.Watcher) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, path FROM library
		WHERE enabled = 1 AND realtime_monitor = 1
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	desired := make(map[int64][]string)
	for rows.Next() {
		var id int64
		var fallback sql.NullString
		if rows.Scan(&id, &fallback) != nil || id <= 0 {
			continue
		}
		folders := listFoldersContext(ctx, s.DB, id, fallback.String)
		if len(folders) == 0 {
			continue
		}
		desired[id] = folders
	}

	s.mu.Lock()
	prev := s.libFolders
	// Compute paths that need to be added or dropped.
	add := make(map[string]struct{})
	keep := make(map[string]struct{})
	for _, folders := range desired {
		for _, f := range folders {
			keep[f] = struct{}{}
		}
	}
	for _, folders := range prev {
		for _, f := range folders {
			if _, ok := keep[f]; !ok {
				_ = w.DropPath(f)
			}
		}
	}
	for _, folders := range desired {
		for _, f := range folders {
			if !prevWatched(prev, f) {
				add[f] = struct{}{}
			}
		}
	}
	s.libFolders = desired
	s.mu.Unlock()

	for f := range add {
		if err := w.AddPath(f, fswatcher.WithDepth(fswatcher.WatchNested)); err != nil {
			log.Printf("monitor: add watch path %s: %v", f, err)
		}
	}
}

func prevWatched(prev map[int64][]string, path string) bool {
	if prev == nil {
		return false
	}
	for _, folders := range prev {
		for _, f := range folders {
			if f == path {
				return true
			}
		}
	}
	return false
}

func (s *Service) submitScan(ctx context.Context, libraryID int64, source scancoord.Source, folders []string) error {
	if s.Submitter == nil {
		return nil
	}
	if _, err := s.Submitter.Submit(ctx, scancoord.ScanRequest{
		LibraryID: libraryID,
		Source:    source,
		Roots:     folders,
	}); err != nil {
		return err
	}
	return nil
}

// submitAutoScans submits scheduled scans for any library with auto_scan=1 that
// is due according to AutoScanInterval.
func (s *Service) submitAutoScans(ctx context.Context) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, path FROM library
		WHERE enabled = 1 AND auto_scan = 1
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	now := time.Now()
	for rows.Next() {
		var id int64
		var fallback sql.NullString
		if rows.Scan(&id, &fallback) != nil || id <= 0 {
			continue
		}
		if !s.autoScanDue(id, now) {
			continue
		}
		folders := listFoldersContext(ctx, s.DB, id, fallback.String)
		if len(folders) == 0 {
			continue
		}
		if err := s.submitScan(ctx, id, scancoord.SourceScheduled, folders); err != nil {
			log.Printf("monitor: auto scan submit library=%d err=%v", id, err)
		} else {
			s.markAutoScan(id, now)
		}
	}
}

func (s *Service) autoScanDue(libraryID int64, now time.Time) bool {
	interval := s.AutoScanInterval
	if interval <= 0 {
		interval = defaultAutoScanInterval
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.lastAutoScan[libraryID]
	if ok && now.Sub(last) < interval {
		return false
	}
	return true
}

func (s *Service) markAutoScan(libraryID int64, now time.Time) {
	s.mu.Lock()
	s.lastAutoScan[libraryID] = now
	s.mu.Unlock()
}

func listFolders(db *sql.DB, libraryID int64, fallback string) []string {
	return listFoldersContext(context.Background(), db, libraryID, fallback)
}

func listFoldersContext(ctx context.Context, db *sql.DB, libraryID int64, fallback string) []string {
	rows, err := db.QueryContext(ctx, `SELECT path FROM library_folder WHERE library_id = ? ORDER BY sort_order, id`, libraryID)
	if err != nil {
		if strings.TrimSpace(fallback) == "" {
			return nil
		}
		return []string{strings.TrimSpace(fallback)}
	}
	defer rows.Close()
	var out []string
	seen := make(map[string]struct{})
	for rows.Next() {
		var p sql.NullString
		if rows.Scan(&p) != nil || !p.Valid || strings.TrimSpace(p.String) == "" {
			continue
		}
		clean := filepath.Clean(strings.TrimSpace(p.String))
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	if len(out) == 0 && strings.TrimSpace(fallback) != "" {
		return []string{filepath.Clean(strings.TrimSpace(fallback))}
	}
	return out
}
