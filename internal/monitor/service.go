package monitor

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"sync"
	"time"

	"knox-media/internal/scanner"
)

const defaultAutoScanInterval = 5 * time.Minute

type Service struct {
	DB       *sql.DB
	Scanner  *scanner.Scanner
	Interval time.Duration
	// AutoScanInterval applies when library.auto_scan=1 and realtime_monitor=0.
	AutoScanInterval time.Duration

	mu           sync.Mutex
	running      map[int64]bool
	lastAutoScan map[int64]time.Time
}

func NewService(db *sql.DB, sc *scanner.Scanner, interval time.Duration) *Service {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Service{
		DB:               db,
		Scanner:          sc,
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
			s.tick()
		}
	}
}

func (s *Service) tick() {
	rows, err := s.DB.Query(`
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
		shouldScan := false
		if realtime == 1 {
			shouldScan = true
		} else if autoScan == 1 {
			shouldScan = s.autoScanDue(id, now)
		}
		if !shouldScan {
			continue
		}
		folders := listFolders(s.DB, id, path.String)
		if len(folders) == 0 {
			continue
		}
		if !s.tryLock(id) {
			continue
		}
		go func(libraryID int64, roots []string, periodic bool) {
			defer s.unlock(libraryID)
			added, err := s.Scanner.ScanLibraryFolders(libraryID, roots)
			if err != nil {
				log.Printf("library monitor scan failed library=%d err=%v", libraryID, err)
				return
			}
			if added > 0 {
				if periodic {
					log.Printf("auto scan library=%d added=%d", libraryID, added)
				} else {
					log.Printf("realtime monitor scan library=%d added=%d", libraryID, added)
				}
			}
		}(id, folders, realtime != 1)
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
	s.lastAutoScan[libraryID] = now
	return true
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
	rows, err := db.Query(`SELECT path FROM library_folder WHERE library_id = ? ORDER BY sort_order, id`, libraryID)
	if err != nil {
		if strings.TrimSpace(fallback) == "" {
			return nil
		}
		return []string{strings.TrimSpace(fallback)}
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p sql.NullString
		if rows.Scan(&p) == nil && p.Valid && strings.TrimSpace(p.String) != "" {
			out = append(out, strings.TrimSpace(p.String))
		}
	}
	if len(out) == 0 && strings.TrimSpace(fallback) != "" {
		return []string{strings.TrimSpace(fallback)}
	}
	return out
}
