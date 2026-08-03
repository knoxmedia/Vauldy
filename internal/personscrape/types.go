package personscrape

import "time"

// Status is the unified lifecycle status for person scrape tasks.
type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// ScrapeMethod identifies how the person is identified.
type ScrapeMethod string

const (
	MethodQuery     ScrapeMethod = "query"
	MethodExternalID ScrapeMethod = "external_id"
	MethodTMDB      ScrapeMethod = "tmdb"
)

// AmbiguityLevel indicates the level of search ambiguity.
type AmbiguityLevel string

const (
	AmbiguityNone         AmbiguityLevel = "none"
	AmbiguityMultipleHits AmbiguityLevel = "multiple_hits"
	AmbiguityNoResults    AmbiguityLevel = "no_results"
	AmbiguityOperatorRequired AmbiguityLevel = "operator_required"
)

// Task represents a durable person scrape task.
type Task struct {
	ID              int64
	PersonSubjectID int64
	Status          Status
	LeaseOwner      string
	LeaseUntil      time.Time
	Method          ScrapeMethod
	QueryName       string
	ExternalID      string
	ProviderSource  string
	APIKey          string
	Language        string
	Generation      int64
	RetryRound      int
	Attempts        int
	MaxAttempts     int
	Ambiguity       AmbiguityLevel
	ProfileJSON     string
	AvatarPath      string
	AvatarStaged    string
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// FenceError is returned when a generation or lease fence is breached.
type FenceError struct {
	Reason string
}

func (e FenceError) Error() string {
	return "person scrape fence: " + e.Reason
}

// NotFoundError indicates a task was not found.
type NotFoundError struct {
	ID int64
}

func (e NotFoundError) Error() string {
	return "person scrape task not found"
}

// DuplicateError indicates an idempotent duplicate was detected.
type DuplicateError struct {
	ExistingTaskID int64
}

func (e DuplicateError) Error() string {
	return "duplicate person scrape task"
}

// AmbiguityError indicates the scrape produced ambiguous results requiring
// operator intervention. This is a structured failure, not a transient error.
type AmbiguityError struct {
	Level        AmbiguityLevel
	CandidateCount int
	Message      string
}

func (e AmbiguityError) Error() string {
	return "person scrape ambiguity: " + string(e.Level) + " - " + e.Message
}

// TaskInput is the input for enqueuing a person scrape task.
type TaskInput struct {
	PersonSubjectID int64
	QueryName       string
	ExternalID      string
	Method          ScrapeMethod
	ProviderSource  string
	APIKey          string
	Language        string
	Generation      int64
}

// PersonResult is the output of a successful person scrape.
type PersonResult struct {
	ProfileJSON string
	AvatarPath  string
}

// ClaimResult is the result of claiming a person scrape task.
type ClaimResult struct {
	Task       Task
	LeaseUntil time.Time
}
