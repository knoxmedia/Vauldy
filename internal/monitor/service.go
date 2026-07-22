package monitor

import (
	"context"
	"database/sql"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"knox-media/internal/scancoord"
)

const defaultAutoScanInterval = 5 * time.Minute

type ScanSubmitter interface {
	Submit(context.Context, scancoord.ScanRequest) (scancoord.SubmitResult, error)
}

type Service struct {
	DB        *sql.DB
	Submitter ScanSubmitter
	Interval  time.Duration
	// AutoScanInterval controls periodic scheduled submissions when auto_scan=1.
	AutoScanInterval time.Duration

	mu           sync.Mutex
	running      map[int64]bool
	lastAutoScan map[int64]time.Time
}

func NewService(db *sql.DB, submitter ScanSubmitter, interval time.Duration) *Service {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Service{
		DB:               db,
		Submitter:        submitter,
		Interval:         interval,
		AutoScanInterval: defaultAutoScanInterval,
		running:          make(map[int64]bool),
		lastAutoScan:     make(map[int64]time.Time),
	}
}

func (s *Service) Start(ctx context.Context) {
	tk := time.NewTicker(s.Interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			s.tick(ctx)
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, path, COALESCE(realtime_monitor, 0), COALESCE(auto_scan, 0)
		FROM library
		WHERE enabled = 1 AND (realtime_monitor = 1 OR auto_scan = 1)
	`)
	if err != nil {
		return
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var id int64
		var path sql.NullString
		var realtime, autoScan int
		if rows.Scan(&id, &path, &realtime, &autoScan) != nil || id <= 0 {
			continue
		}
		var source scancoord.Source
		if realtime == 1 {
			source = scancoord.SourceMonitor
		} else if autoScan == 1 && s.autoScanDue(id, now) {
			source = scancoord.SourceScheduled
		}
		if source == "" || s.Submitter == nil {
			continue
		}
		folders := listFoldersContext(ctx, s.DB, id, path.String)
		if len(folders) == 0 {
			continue
		}
		if _, err := s.Submitter.Submit(ctx, scancoord.ScanRequest{LibraryID: id, Source: source, Roots: folders}); err != nil {
			log.Printf("library scan submit failed library=%d source=%s err=%v", id, source, err)
		} else if source == scancoord.SourceScheduled {
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

func (s *Service) tryLock(libraryID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[libraryID] {
		return false
	}
	s.running[libraryID] = true
	return true
}

func (s *Service) unlock(libraryID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, libraryID)
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
