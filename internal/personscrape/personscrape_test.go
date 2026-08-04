package personscrape

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=2000")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db := newTestDB(t)
	store := NewStore(db)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return store
}

// mockScraper is a mock PersonScraper for testing.
type mockScraper struct {
	searchResult []CandidateMatch
	searchErr    error
	profileData  *ProfileData
	profileErr   error
}

func (m *mockScraper) SearchCandidates(ctx context.Context, query, language, apiKey string) ([]CandidateMatch, error) {
	return m.searchResult, m.searchErr
}

func (m *mockScraper) FetchProfile(ctx context.Context, externalID, language, apiKey string) (*ProfileData, error) {
	return m.profileData, m.profileErr
}

// --- Task 9: Person Scrape tests ---

func TestPersonScrape_EnqueueQuery(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := TaskInput{
		PersonSubjectID: 1,
		QueryName:       "Tom Hanks",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Language:        "zh-CN",
		Generation:      1,
	}

	task, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if task.Status != StatusWaiting {
		t.Errorf("expected waiting, got %s", task.Status)
	}
	if task.QueryName != "Tom Hanks" {
		t.Errorf("expected query 'Tom Hanks', got %s", task.QueryName)
	}
	if task.Method != MethodQuery {
		t.Errorf("expected query method, got %s", task.Method)
	}
	if task.PersonSubjectID != 1 {
		t.Errorf("expected person_subject_id 1, got %d", task.PersonSubjectID)
	}
}

func TestPersonScrape_EnqueueExternalID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := TaskInput{
		PersonSubjectID: 2,
		ExternalID:      "31",
		Method:          MethodExternalID,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Language:        "en-US",
		Generation:      1,
	}

	task, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("enqueue external_id failed: %v", err)
	}
	if task.ExternalID != "31" {
		t.Errorf("expected external_id 31, got %s", task.ExternalID)
	}
	if task.Method != MethodExternalID {
		t.Errorf("expected external_id method, got %s", task.Method)
	}
}

func TestPersonScrape_EnqueueDuplicateIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := TaskInput{
		PersonSubjectID: 42,
		QueryName:       "Scarlett Johansson",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	}

	_, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}

	// Claim and complete.
	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)
	store.CommitDone(ctx, task.ID, "worker-1", 1, PersonResult{
		ProfileJSON: `{"name":"Scarlett Johansson"}`, AvatarPath: "/avatars/42.jpg",
	})

	// Re-enqueue after done returns DuplicateError.
	_, err = store.Enqueue(ctx, input)
	if err == nil {
		t.Error("expected DuplicateError on re-enqueue of completed task")
	}
	if _, ok := err.(DuplicateError); !ok {
		t.Errorf("expected DuplicateError, got %T: %v", err, err)
	}
}

func TestPersonScrape_ClaimAndHeartbeat(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 10,
		QueryName:       "Brad Pitt",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.Status != StatusRunning {
		t.Errorf("expected running, got %s", task.Status)
	}
	if task.LeaseOwner != "worker-1" {
		t.Errorf("expected owner worker-1, got %s", task.LeaseOwner)
	}
	if task.Attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", task.Attempts)
	}

	err = store.Heartbeat(ctx, task.ID, "worker-1", 60*time.Second)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
}

func TestPersonScrape_Cancellation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 20,
		QueryName:       "Meryl Streep",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)

	err := store.Cancel(ctx, task.ID, "worker-1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	got, _ := store.Get(ctx, task.ID)
	if got.Status != StatusCancelled {
		t.Errorf("expected cancelled, got %s", got.Status)
	}
}

func TestPersonScrape_AvatarStaging(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 30,
		QueryName:       "Leonardo DiCaprio",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)

	err := store.SetAvatarStaged(ctx, task.ID, "worker-1", "/tmp/avatars/staged_30.jpg")
	if err != nil {
		t.Fatalf("avatar staged: %v", err)
	}

	got, _ := store.Get(ctx, task.ID)
	if got.AvatarStaged != "/tmp/avatars/staged_30.jpg" {
		t.Errorf("expected staged avatar, got %s", got.AvatarStaged)
	}
}

func TestPersonScrape_AtomicProfileCommit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 40,
		QueryName:       "Morgan Freeman",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)

	result := PersonResult{
		ProfileJSON: `{"name":"Morgan Freeman","birth_date":"1937-06-01"}`,
		AvatarPath:  "https://image.tmdb.org/t/p/original/abc.jpg",
	}

	err := store.CommitDone(ctx, task.ID, "worker-1", 1, result)
	if err != nil {
		t.Fatalf("commit done: %v", err)
	}

	got, _ := store.Get(ctx, task.ID)
	if got.Status != StatusDone {
		t.Errorf("expected done, got %s", got.Status)
	}
	if got.ProfileJSON != result.ProfileJSON {
		t.Errorf("expected profile %s, got %s", result.ProfileJSON, got.ProfileJSON)
	}
	if got.AvatarPath != result.AvatarPath {
		t.Errorf("expected avatar %s, got %s", result.AvatarPath, got.AvatarPath)
	}
}

func TestPersonScrape_PersonDeletionFence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 50,
		QueryName:       "Natalie Portman",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	// Delete by person subject.
	err := store.DeleteByPersonSubject(ctx, 50)
	if err != nil {
		t.Fatalf("delete by subject: %v", err)
	}

	// Verify task is gone.
	_, err = store.GetByPersonSubject(ctx, 50)
	if err == nil {
		t.Error("expected NotFoundError after deletion")
	}
	if _, ok := err.(NotFoundError); !ok {
		t.Errorf("expected NotFoundError, got %T: %v", err, err)
	}
}

func TestPersonScrape_AmbiguityMultipleHits(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	store.EnsureSchema(context.Background())
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 60,
		QueryName:       "Li Na",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)

	mock := &mockScraper{
		searchResult: []CandidateMatch{
			{ExternalID: "1001", Name: "Li Na", KnownFor: "Tennis"},
			{ExternalID: "1002", Name: "Li Na", KnownFor: "Acting"},
		},
	}
	worker := NewWorker(db, mock)
	err := worker.ExecuteClaimed(ctx, task)
	if err == nil {
		t.Fatal("expected ambiguity error for multiple hits")
	}
	if _, ok := err.(AmbiguityError); !ok {
		t.Errorf("expected AmbiguityError, got %T: %v", err, err)
	}

	// Verify task is failed with ambiguity.
	got, _ := store.Get(ctx, task.ID)
	if got.Status != StatusFailed {
		t.Errorf("expected failed, got %s", got.Status)
	}
	if got.Ambiguity != AmbiguityMultipleHits {
		t.Errorf("expected multiple_hits ambiguity, got %s", got.Ambiguity)
	}
}

func TestPersonScrape_AmbiguityNoResults(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	store.EnsureSchema(context.Background())
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 61,
		QueryName:       "NobodyExists",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)

	mock := &mockScraper{searchResult: []CandidateMatch{}}
	worker := NewWorker(db, mock)
	err := worker.ExecuteClaimed(ctx, task)
	if err == nil {
		t.Fatal("expected ambiguity error for no results")
	}

	got, _ := store.Get(ctx, task.ID)
	if got.Status != StatusFailed {
		t.Errorf("expected failed, got %s", got.Status)
	}
	if got.Ambiguity != AmbiguityNoResults {
		t.Errorf("expected no_results ambiguity, got %s", got.Ambiguity)
	}
}

func TestPersonScrape_TMDBProfileFetchSuccess(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	store.EnsureSchema(context.Background())
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 62,
		ExternalID:      "500",
		Method:          MethodExternalID,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Language:        "zh-CN",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)

	profile := &ProfileData{
		Name:        "Jackie Chan",
		EnglishName: "Chan Kong-sang",
		Gender:      2,
		BirthDate:   "1954-04-07",
		BirthPlace:  "Hong Kong",
		Biography:   "Famous martial artist and actor.",
		AvatarURL:   "https://image.tmdb.org/t/p/original/jackie.jpg",
		Occupations: []string{"actor", "director", "producer"},
		TMDBID:      "500",
	}

	mock := &mockScraper{profileData: profile}
	worker := NewWorker(db, mock)
	err := worker.ExecuteClaimed(ctx, task)
	if err != nil {
		t.Fatalf("worker execute: %v", err)
	}

	got, _ := store.Get(ctx, task.ID)
	if got.Status != StatusDone {
		t.Errorf("expected done, got %s", got.Status)
	}

	var gotProfile ProfileData
	json.Unmarshal([]byte(got.ProfileJSON), &gotProfile)
	if gotProfile.Name != "Jackie Chan" {
		t.Errorf("expected name 'Jackie Chan', got %s", gotProfile.Name)
	}
	if got.AvatarPath != "https://image.tmdb.org/t/p/original/jackie.jpg" {
		t.Errorf("wrong avatar path: %s", got.AvatarPath)
	}
}

func TestPersonScrape_RetryableNetworkError(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	store.EnsureSchema(context.Background())
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 63,
		ExternalID:      "501",
		Method:          MethodExternalID,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)

	mock := &mockScraper{profileErr: errors.New("network timeout")}
	worker := NewWorker(db, mock)
	err := worker.ExecuteClaimed(ctx, task)
	if err != nil {
		t.Logf("expected failure: %v", err)
	}

	got, _ := store.Get(ctx, task.ID)
	if got.Status != StatusFailed {
		t.Errorf("expected failed, got %s", got.Status)
	}
}

func TestPersonScrape_PermanentAuthError(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	store.EnsureSchema(context.Background())
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 64,
		ExternalID:      "502",
		Method:          MethodExternalID,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)

	mock := &mockScraper{profileErr: fmt.Errorf("http 401")}
	worker := NewWorker(db, mock)
	worker.ExecuteClaimed(ctx, task)

	got, _ := store.Get(ctx, task.ID)
	if got.Status != StatusFailed {
		t.Errorf("expected failed for auth error, got %s", got.Status)
	}
}

func TestPersonScrape_NotFoundSubject(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	store.EnsureSchema(context.Background())
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 65,
		ExternalID:      "99999",
		Method:          MethodExternalID,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)

	mock := &mockScraper{profileErr: fmt.Errorf("http 404")}
	worker := NewWorker(db, mock)
	worker.ExecuteClaimed(ctx, task)

	got, _ := store.Get(ctx, task.ID)
	if got.Status != StatusFailed {
		t.Errorf("expected failed for not found, got %s", got.Status)
	}
}

func TestPersonScrape_ResetStuckRunning(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 66,
		QueryName:       "Test Person",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})

	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)
	// Manually expire the lease.
	store.db.ExecContext(ctx,
		`UPDATE person_scrape_task SET lease_until='2000-01-01T00:00:00Z' WHERE id=?`, task.ID)

	n, err := store.ResetStuckRunning(ctx)
	if err != nil {
		t.Fatalf("reset stuck: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 reset, got %d", n)
	}

	got, _ := store.Get(ctx, task.ID)
	if got.Status != StatusWaiting {
		t.Errorf("expected waiting after reset, got %s", got.Status)
	}
	if got.LeaseOwner != "" {
		t.Errorf("expected empty lease owner, got %s", got.LeaseOwner)
	}
}

func TestPersonScrape_WorkerNilTask(t *testing.T) {
	db := newTestDB(t)
	worker := NewWorker(db, &mockScraper{})
	err := worker.ExecuteClaimed(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil task")
	}
}

func TestPersonScrape_WorkerNotRunning(t *testing.T) {
	db := newTestDB(t)
	worker := NewWorker(db, &mockScraper{})
	task := &Task{ID: 1, Status: StatusWaiting}
	err := worker.ExecuteClaimed(context.Background(), task)
	if err == nil {
		t.Error("expected FenceError for non-running task")
	}
	if _, ok := err.(FenceError); !ok {
		t.Errorf("expected FenceError, got %T", err)
	}
}

func TestPersonScrape_EnqueueRetryRoundIncrements(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := TaskInput{
		PersonSubjectID: 67,
		QueryName:       "Retry Person",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	}

	// First enqueue.
	t1, _ := store.Enqueue(ctx, input)
	// Claim and fail.
	task, _ := store.Claim(ctx, "worker-1", 30*time.Second)
	store.MarkFailed(ctx, task.ID, "worker-1", "test error", AmbiguityNone)

	// Re-enqueue (should increment retry round).
	t2, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	if t2.RetryRound <= t1.RetryRound {
		t.Errorf("expected retry round > %d, got %d", t1.RetryRound, t2.RetryRound)
	}
}

func TestPersonScrape_QueryVsExternalID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Enqueue with query method.
	qTask, _ := store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 68,
		QueryName:       "Query Method",
		Method:          MethodQuery,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})
	if qTask.Method != MethodQuery {
		t.Errorf("expected query method, got %s", qTask.Method)
	}

	// Enqueue with external_id method.
	eTask, _ := store.Enqueue(ctx, TaskInput{
		PersonSubjectID: 69,
		ExternalID:      "tmdb-123",
		Method:          MethodExternalID,
		ProviderSource:  "tmdb",
		APIKey:          "test-key",
		Generation:      1,
	})
	if eTask.Method != MethodExternalID {
		t.Errorf("expected external_id method, got %s", eTask.Method)
	}
}
