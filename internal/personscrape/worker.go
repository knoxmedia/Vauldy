package personscrape

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Worker executes a claimed person scrape task.
type Worker struct {
	db      *sql.DB
	store   *Store
	scraper PersonScraper
}

// NewWorker creates a new person scrape Worker.
func NewWorker(db *sql.DB, scraper PersonScraper) *Worker {
	return &Worker{
		db:      db,
		store:   NewStore(db),
		scraper: scraper,
	}
}

// ExecuteClaimed runs one claimed person scrape task. It does not query batches
// or own canonical lifecycle - the scheduler owns concurrency.
func (w *Worker) ExecuteClaimed(ctx context.Context, task *Task) error {
	if task == nil {
		return fmt.Errorf("person scrape worker: nil task")
	}
	if task.Status != StatusRunning {
		return FenceError{Reason: "task not in running state"}
	}

	// Validate lease before effects.
	if err := w.validateLease(ctx, task); err != nil {
		return err
	}

	if w.scraper == nil {
		return fmt.Errorf("person scrape worker: no scraper configured")
	}

	// For query-based scrape: search and validate.
	if task.Method == MethodQuery {
		if stringsAreEmpty(task.QueryName) {
			return fmt.Errorf("person scrape: query name required for query method")
		}
		candidates, err := w.scraper.SearchCandidates(ctx, task.QueryName, task.Language, task.APIKey)
		if err != nil {
			w.store.MarkFailed(ctx, task.ID, task.LeaseOwner, err.Error(), AmbiguityNone)
			return err
		}
		if len(candidates) == 0 {
			ae := AmbiguityError{
				Level:         AmbiguityNoResults,
				CandidateCount: 0,
				Message:       fmt.Sprintf("no results for query '%s'", task.QueryName),
			}
			w.store.MarkFailed(ctx, task.ID, task.LeaseOwner, ae.Error(), AmbiguityNoResults)
			return ae
		}
		if len(candidates) > 1 {
			ae := AmbiguityError{
				Level:         AmbiguityMultipleHits,
				CandidateCount: len(candidates),
				Message:       fmt.Sprintf("multiple candidates for '%s': %d matches", task.QueryName, len(candidates)),
			}
			w.store.MarkFailed(ctx, task.ID, task.LeaseOwner, ae.Error(), AmbiguityMultipleHits)
			return ae
		}
		// Single match: proceed with TMDB fetch.
		task.ExternalID = candidates[0].ExternalID
		task.Method = MethodTMDB
	}

	if task.Method == MethodExternalID || task.Method == MethodTMDB {
		if stringsAreEmpty(task.ExternalID) {
			return fmt.Errorf("person scrape: external_id required")
		}
		// Re-validate lease before provider I/O.
		if err := w.validateLease(ctx, task); err != nil {
			return err
		}

		profile, err := w.scraper.FetchProfile(ctx, task.ExternalID, task.Language, task.APIKey)
		if err != nil {
			w.store.MarkFailed(ctx, task.ID, task.LeaseOwner, err.Error(), AmbiguityNone)
			return err
		}

		// Re-validate lease before commit.
		if err := w.validateLease(ctx, task); err != nil {
			return err
		}

		profileJSON, err := json.Marshal(profile)
		if err != nil {
			w.store.MarkFailed(ctx, task.ID, task.LeaseOwner, err.Error(), AmbiguityNone)
			return err
		}

		result := PersonResult{
			ProfileJSON: string(profileJSON),
			AvatarPath:  profile.AvatarURL,
		}

		return w.store.CommitDone(ctx, task.ID, task.LeaseOwner, task.Generation, result)
	}

	return fmt.Errorf("person scrape: unsupported method %s", task.Method)
}

func (w *Worker) validateLease(ctx context.Context, task *Task) error {
	current, err := w.store.Get(ctx, task.ID)
	if err != nil {
		return err
	}
	if current.Status != StatusRunning {
		return FenceError{Reason: "task no longer running"}
	}
	if current.LeaseOwner != task.LeaseOwner {
		return FenceError{Reason: "lease owner changed"}
	}
	if current.Generation != task.Generation {
		return FenceError{Reason: "generation changed"}
	}
	if time.Now().After(current.LeaseUntil) {
		return FenceError{Reason: "lease expired"}
	}
	return nil
}

func stringsAreEmpty(ss ...string) bool {
	for _, s := range ss {
		if s == "" {
			return true
		}
	}
	return false
}
