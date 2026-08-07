package aitask

import (
	"context"
	"database/sql"
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

// --- Task 8: AI SubTask tests ---

func TestAI_Subtask_EnqueueSummary(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := SubTaskInput{
		MediaID:      1,
		ParentTaskID: 100,
		Capability:   CapSummary,
		Provider:     "openai",
		ProviderID:   "org-1",
		Model:        "gpt-4",
		ModelVersion: "0613",
		InputDigest:  "sha256:abc123",
		Generation:   1,
	}

	task, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if task.Status != StatusWaiting {
		t.Errorf("expected waiting, got %s", task.Status)
	}
	if task.Capability != CapSummary {
		t.Errorf("expected summary capability, got %s", task.Capability)
	}
	if task.MediaID != 1 {
		t.Errorf("expected media_id 1, got %d", task.MediaID)
	}
	if task.Provider != "openai" {
		t.Errorf("expected provider openai, got %s", task.Provider)
	}
	if task.Model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", task.Model)
	}
	if task.InputDigest != "sha256:abc123" {
		t.Errorf("expected input digest, got %s", task.InputDigest)
	}
}

func TestAI_Subtask_EnqueueClassification(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := SubTaskInput{
		MediaID:      2,
		ParentTaskID: 101,
		Capability:   CapClassification,
		Provider:     "openai",
		Model:        "gpt-4",
		InputDigest:  "sha256:def456",
		Generation:   1,
	}

	task, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("enqueue classification failed: %v", err)
	}
	if task.Capability != CapClassification {
		t.Errorf("expected classification, got %s", task.Capability)
	}
}

func TestAI_Subtask_EnqueueTags(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := SubTaskInput{
		MediaID:      3,
		ParentTaskID: 102,
		Capability:   CapTags,
		Provider:     "anthropic",
		Model:        "claude-3",
		InputDigest:  "sha256:ghi789",
		Generation:   1,
	}

	task, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("enqueue tags failed: %v", err)
	}
	if task.Capability != CapTags {
		t.Errorf("expected tags, got %s", task.Capability)
	}
}

func TestAI_Subtask_EnqueueDuplicateIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := SubTaskInput{
		MediaID: 42, ParentTaskID: 200, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:xxx",
		Generation: 1,
	}

	task1, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}

	// Claim and complete.
	claimed, err := store.Claim(ctx, CapSummary, "worker-1", 30*time.Second)
	if err != nil || claimed == nil {
		t.Fatalf("claim failed: %v", err)
	}
	store.CommitDone(ctx, claimed.ID, "worker-1", 1, SubTaskResult{
		ResultHash: "done-hash", ResultPreview: "done-preview", ResultRows: 5,
	})

	// Re-enqueue of same capability after done returns DuplicateError.
	_, err = store.Enqueue(ctx, input)
	if err == nil {
		t.Error("expected DuplicateError on re-enqueue of completed subtask")
	}
	if _, ok := err.(DuplicateError); !ok {
		t.Errorf("expected DuplicateError, got %T: %v", err, err)
	}
	_ = task1
}

func TestAI_Subtask_ClaimAndHeartbeat(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, SubTaskInput{
		MediaID: 10, ParentTaskID: 300, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:hhh",
		Generation: 1,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	task, err := store.Claim(ctx, CapSummary, "worker-1", 30*time.Second)
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

func TestAI_Subtask_FailureDoesNotOverwriteSibling(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Enqueue summary and classification as siblings.
	s1, _ := store.Enqueue(ctx, SubTaskInput{
		MediaID: 50, ParentTaskID: 400, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:sum",
		Generation: 1,
	})
	s2, _ := store.Enqueue(ctx, SubTaskInput{
		MediaID: 50, ParentTaskID: 400, Capability: CapClassification,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:cls",
		Generation: 1,
	})

	// Fail the summary.
	claimed, _ := store.Claim(ctx, CapSummary, "worker-1", 30*time.Second)
	store.MarkFailed(ctx, claimed.ID, "worker-1", "summary error")

	// Classification must remain waiting (not overwritten).
	cls, err := store.Get(ctx, s2.ID)
	if err != nil {
		t.Fatalf("get classification: %v", err)
	}
	if cls.Status != StatusWaiting {
		t.Errorf("sibling classification should remain waiting, got %s", cls.Status)
	}
	_ = s1
}

func TestAI_Subtask_MissingCapabilityWaits(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Enqueue only summary and classification, not tags.
	store.Enqueue(ctx, SubTaskInput{
		MediaID: 60, ParentTaskID: 500, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:aa",
		Generation: 1,
	})
	store.Enqueue(ctx, SubTaskInput{
		MediaID: 60, ParentTaskID: 500, Capability: CapClassification,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:bb",
		Generation: 1,
	})

	// Attempting to get tags should return NotFoundError.
	_, err := store.GetByMediaAndCapability(ctx, 60, CapTags)
	if err == nil {
		t.Error("expected NotFoundError for missing tags capability")
	}
	if _, ok := err.(NotFoundError); !ok {
		t.Errorf("expected NotFoundError, got %T: %v", err, err)
	}

	// List by media should only return 2 subtasks.
	list, err := store.ListByMedia(ctx, 60)
	if err != nil {
		t.Fatalf("list by media: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 subtasks, got %d", len(list))
	}
}

func TestAI_Subtask_DuplicateCompletionDoesNotDoubleApply(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := SubTaskInput{
		MediaID: 70, ParentTaskID: 600, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:ccc",
		Generation: 1,
	}
	store.Enqueue(ctx, input)

	claimed, _ := store.Claim(ctx, CapSummary, "worker-1", 30*time.Second)
	err := store.CommitDone(ctx, claimed.ID, "worker-1", 1, SubTaskResult{
		ResultHash: "hash1", ResultPreview: "preview1", ResultRows: 3,
	})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// Second commit should fail with FenceError (no longer running).
	err = store.CommitDone(ctx, claimed.ID, "worker-1", 1, SubTaskResult{
		ResultHash: "hash2", ResultPreview: "preview2", ResultRows: 10,
	})
	if err == nil {
		t.Error("expected FenceError on duplicate completion")
	}
	if _, ok := err.(FenceError); !ok {
		t.Errorf("expected FenceError, got %T: %v", err, err)
	}

	// Verify first result remains.
	task, _ := store.Get(ctx, claimed.ID)
	if task.ResultHash != "hash1" {
		t.Errorf("result should be hash1, got %s", task.ResultHash)
	}
	if task.ResultRows != 3 {
		t.Errorf("result rows should be 3, got %d", task.ResultRows)
	}
}

func TestAI_Subtask_ProviderTokensAcquired(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	input := SubTaskInput{
		MediaID: 80, ParentTaskID: 700, Capability: CapSummary,
		Provider: "openai", ProviderID: "org-xyz", Model: "gpt-4o",
		ModelVersion: "2024-08", InputDigest: "sha256:ddd",
		Generation: 1,
	}
	task, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if task.Provider != "openai" {
		t.Errorf("expected provider openai, got %s", task.Provider)
	}
	if task.ProviderID != "org-xyz" {
		t.Errorf("expected provider_id org-xyz, got %s", task.ProviderID)
	}
	if task.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", task.Model)
	}
	if task.ModelVersion != "2024-08" {
		t.Errorf("expected model_version 2024-08, got %s", task.ModelVersion)
	}
}

func TestAI_Subtask_Cancellation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, SubTaskInput{
		MediaID: 90, ParentTaskID: 800, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:eee",
		Generation: 1,
	})

	claimed, _ := store.Claim(ctx, CapSummary, "worker-1", 30*time.Second)

	// Cancel the running task.
	err := store.Cancel(ctx, claimed.ID, "worker-1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	task, _ := store.Get(ctx, claimed.ID)
	if task.Status != StatusCancelled {
		t.Errorf("expected cancelled, got %s", task.Status)
	}
}

func TestAI_Subtask_Progress(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, SubTaskInput{
		MediaID: 91, ParentTaskID: 801, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:fff",
		Generation: 1,
	})
	claimed, _ := store.Claim(ctx, CapSummary, "worker-1", 30*time.Second)

	err := store.UpdateProgress(ctx, claimed.ID, "worker-1", 50.0)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}

	task, _ := store.Get(ctx, claimed.ID)
	if task.Progress != 50.0 {
		t.Errorf("expected progress 50, got %f", task.Progress)
	}
}

func TestAI_Subtask_ResetStuckRunning(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, SubTaskInput{
		MediaID: 92, ParentTaskID: 802, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:ggg",
		Generation: 1,
	})

	// Claim with very short lease, then manually set lease far in the past.
	claimed, _ := store.Claim(ctx, CapSummary, "worker-1", 30*time.Second)
	// Manually expire the lease so ResetStuckRunning finds it.
	_, err := store.db.ExecContext(ctx,
		`UPDATE ai_analysis_result SET lease_until='2000-01-01T00:00:00Z' WHERE id=?`, claimed.ID)
	if err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	n, err := store.ResetStuckRunning(ctx)
	if err != nil {
		t.Fatalf("reset stuck: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 reset, got %d", n)
	}

	task, _ := store.Get(ctx, claimed.ID)
	if task.Status != StatusWaiting {
		t.Errorf("expected waiting after reset, got %s", task.Status)
	}
	if task.LeaseOwner != "" {
		t.Errorf("expected empty lease owner, got %s", task.LeaseOwner)
	}
}

func TestAI_Subtask_Prerequisites(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Enqueue with prerequisites (classification depends on summary).
	input := SubTaskInput{
		MediaID: 93, ParentTaskID: 803, Capability: CapClassification,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:hhh",
		Generation: 1, Prerequisites: "[1]",
	}
	task, err := store.Enqueue(ctx, input)
	if err != nil {
		t.Fatalf("enqueue with prereqs: %v", err)
	}

	preIDs, err := store.GetPrerequisites(ctx, task.ID)
	if err != nil {
		t.Fatalf("get prereqs: %v", err)
	}
	if len(preIDs) != 1 || preIDs[0] != 1 {
		t.Errorf("expected prereq [1], got %v", preIDs)
	}
}

func TestAI_Subtask_ListByParent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, SubTaskInput{
		MediaID: 94, ParentTaskID: 804, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:iii",
		Generation: 1,
	})
	store.Enqueue(ctx, SubTaskInput{
		MediaID: 94, ParentTaskID: 804, Capability: CapClassification,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:jjj",
		Generation: 1,
	})

	tasks, err := store.ListByParent(ctx, 804)
	if err != nil {
		t.Fatalf("list by parent: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 subtasks, got %d", len(tasks))
	}
}

func TestAI_Subtask_ThreeCapsAsIndependentKeys(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Enqueue all three capabilities for same media.
	store.Enqueue(ctx, SubTaskInput{
		MediaID: 95, ParentTaskID: 805, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:kkk",
		Generation: 1,
	})
	store.Enqueue(ctx, SubTaskInput{
		MediaID: 95, ParentTaskID: 805, Capability: CapClassification,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:lll",
		Generation: 1,
	})
	store.Enqueue(ctx, SubTaskInput{
		MediaID: 95, ParentTaskID: 805, Capability: CapTags,
		Provider: "anthropic", Model: "claude-3", InputDigest: "sha256:mmm",
		Generation: 1,
	})

	list, _ := store.ListByMedia(ctx, 95)
	if len(list) != 3 {
		t.Fatalf("expected 3 subtasks, got %d", len(list))
	}

	// Verify each has a distinct capability.
	capsFound := make(map[Capability]bool)
	for _, task := range list {
		capsFound[task.Capability] = true
	}
	if len(capsFound) != 3 {
		t.Errorf("expected 3 distinct capabilities, got %d", len(capsFound))
	}
	if !capsFound[CapSummary] || !capsFound[CapClassification] || !capsFound[CapTags] {
		t.Errorf("missing expected capabilities: %v", capsFound)
	}
}

func TestAI_Subtask_ClaimByCapability(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Enqueue(ctx, SubTaskInput{
		MediaID: 96, ParentTaskID: 806, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:nnn",
		Generation: 1,
	})
	store.Enqueue(ctx, SubTaskInput{
		MediaID: 96, ParentTaskID: 806, Capability: CapClassification,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:ooo",
		Generation: 1,
	})

	// Claim specifically the classification.
	task, err := store.Claim(ctx, CapClassification, "worker-cls", 30*time.Second)
	if err != nil {
		t.Fatalf("claim by capability: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.Capability != CapClassification {
		t.Errorf("expected classification, got %s", task.Capability)
	}
}

func TestAI_Subtask_WorkerExecuteClaimed(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	store.EnsureSchema(context.Background())
	ctx := context.Background()

	store.Enqueue(ctx, SubTaskInput{
		MediaID: 97, ParentTaskID: 807, Capability: CapSummary,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:ppp",
		Generation: 1,
	})

	claimed, _ := store.Claim(ctx, CapSummary, "worker-exec", 30*time.Second)

	worker := NewWorker(db, NoOpProvider{})
	err := worker.ExecuteClaimed(ctx, claimed)
	if err != nil {
		t.Fatalf("worker execute: %v", err)
	}

	task, _ := store.Get(ctx, claimed.ID)
	if task.Status != StatusDone {
		t.Errorf("expected done, got %s", task.Status)
	}
}

func TestAI_Subtask_AdapterEnqueueAll(t *testing.T) {
	db := newTestDB(t)
	store := NewStore(db)
	store.EnsureSchema(context.Background())
	ctx := context.Background()

	worker := NewWorker(db, NoOpProvider{})
	adapter := NewAdapter(db, worker)

	input := SubTaskInput{
		MediaID: 98, ParentTaskID: 808,
		Provider: "openai", Model: "gpt-4", InputDigest: "sha256:qqq",
		Generation: 1,
	}

	tasks, err := adapter.EnqueueAllCapabilities(ctx, input)
	if err != nil {
		t.Fatalf("enqueue all: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}

	caps := map[Capability]bool{}
	for _, task := range tasks {
		caps[task.Capability] = true
	}
	if !caps[CapSummary] || !caps[CapClassification] || !caps[CapTags] {
		t.Errorf("missing capabilities: %v", caps)
	}
}

func TestAI_Subtask_WorkerNilTask(t *testing.T) {
	db := newTestDB(t)
	worker := NewWorker(db, NoOpProvider{})
	err := worker.ExecuteClaimed(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil task")
	}
}

func TestAI_Subtask_WorkerNotRunning(t *testing.T) {
	db := newTestDB(t)
	worker := NewWorker(db, NoOpProvider{})
	task := &SubTask{ID: 1, Status: StatusWaiting}
	err := worker.ExecuteClaimed(context.Background(), task)
	if err == nil {
		t.Error("expected FenceError for non-running task")
	}
	if _, ok := err.(FenceError); !ok {
		t.Errorf("expected FenceError, got %T", err)
	}
}
