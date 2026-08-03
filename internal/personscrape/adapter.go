package personscrape

import (
	"context"
	"database/sql"
	"fmt"
)

// PersonScraper is the primitive scraper interface for person profile data.
type PersonScraper interface {
	SearchCandidates(ctx context.Context, query, language, apiKey string) ([]CandidateMatch, error)
	FetchProfile(ctx context.Context, externalID, language, apiKey string) (*ProfileData, error)
}

// CandidateMatch is a search result from a person provider.
type CandidateMatch struct {
	ExternalID  string
	Name        string
	EnglishName string
	Profile     string
	Birthday    string
	KnownFor    string
	Gender      int
}

// ProfileData is full person profile from a provider.
type ProfileData struct {
	Name        string
	EnglishName string
	Gender      int
	BirthDate   string
	BirthPlace  string
	Biography   string
	AvatarURL   string
	Aliases     string
	Occupations []string
	TMDBID      string
	IMDBID      string
}

// Adapter wraps person scrape in a postingest-compatible adapter.
type Adapter struct {
	db      *sql.DB
	store   *Store
	worker  *Worker
	scraper PersonScraper
}

// NewAdapter creates a postingest adapter for person scrape.
func NewAdapter(db *sql.DB, scraper PersonScraper) *Adapter {
	return &Adapter{
		db:      db,
		store:   NewStore(db),
		worker:  NewWorker(db, scraper),
		scraper: scraper,
	}
}

// QueueTask is the task shape passed by the postingest dispatcher.
type QueueTask struct {
	ID              int64
	PersonSubjectID int64
	TaskType        string
	Status          string
	LeaseOwner      string
	Generation      int64
	RetryRound      int
	Attempts        int
	MaxAttempts     int
}

// Execute runs a claimed person scrape task.
func (a *Adapter) Execute(ctx context.Context, qt QueueTask) error {
	if a.worker == nil {
		return fmt.Errorf("person scrape adapter: no worker configured")
	}

	task, err := a.store.Get(ctx, qt.ID)
	if err != nil {
		return fmt.Errorf("person scrape adapter: %w", err)
	}

	if task.Status != StatusRunning {
		return FenceError{Reason: "task not running"}
	}
	if task.LeaseOwner != qt.LeaseOwner {
		return FenceError{Reason: "lease owner mismatch"}
	}

	return a.worker.ExecuteClaimed(ctx, task)
}
