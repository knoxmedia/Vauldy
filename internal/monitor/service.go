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

type Service struct {
	DB       *sql.DB
	Scanner  *scanner.Scanner
	Interval time.Duration
	mu       sync.Mutex
	running  map[int64]bool
}

func NewService(db *sql.DB, sc *scanner.Scanner, interval time.Duration) *Service {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Service{DB: db, Scanner: sc, Interval: interval, running: make(map[int64]bool)}
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
	rows, err := s.DB.Query(`SELECT id, path FROM library WHERE enabled = 1 AND realtime_monitor = 1`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var path sql.NullString
		if rows.Scan(&id, &path) != nil || id <= 0 {
			continue
		}
		folders := listFolders(s.DB, id, path.String)
		if len(folders) == 0 {
			continue
		}
		if !s.tryLock(id) {
			continue
		}
		go func(libraryID int64, roots []string) {
			defer s.unlock(libraryID)
			added, err := s.Scanner.ScanLibraryFolders(libraryID, roots)
			if err != nil {
				log.Printf("realtime monitor scan failed library=%d err=%v", libraryID, err)
				return
			}
			if added > 0 {
				log.Printf("realtime monitor scan library=%d added=%d", libraryID, added)
			}
		}(id, folders)
	}
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
