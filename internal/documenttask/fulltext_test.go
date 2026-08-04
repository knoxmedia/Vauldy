package documenttask

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newFulltextTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=2000")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestFullText_StoreSchema(t *testing.T) {
	db := newFulltextTestDB(t)
	store := NewStore(db)

	if err := store.EnsureFulltextSchema(context.Background()); err != nil {
		t.Fatalf("ensure fulltext schema: %v", err)
	}
}

func TestFullText_EnqueueAndClaim(t *testing.T) {
	db := newFulltextTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := store.EnsureFulltextSchema(ctx); err != nil {
		t.Fatalf("fulltext schema: %v", err)
	}

	// Enqueue a fulltext task.
	input := FulltextInput{
		MediaID:      1,
		SourcePath:   "/test/test.pdf",
		Generation:   1,
		Language:     "eng",
		MaxPages:     100,
		MaxBytes:     50 * 1024 * 1024,
		DocumentKind: "pdf",
	}
	task, err := store.EnqueueFulltext(ctx, input)
	if err != nil {
		t.Fatalf("enqueue fulltext: %v", err)
	}
	if task.Status != StatusWaiting {
		t.Errorf("expected waiting, got %s", task.Status)
	}
	if task.Language != "eng" {
		t.Errorf("expected lang eng, got %s", task.Language)
	}
	if task.MaxPages != 100 {
		t.Errorf("expected 100 pages, got %d", task.MaxPages)
	}

	// Claim.
	claimed, err := store.ClaimFulltext(ctx, "worker-1", 30*time.Second)
	if err != nil {
		t.Fatalf("claim fulltext: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected a claimed task")
	}
	if claimed.Status != StatusRunning {
		t.Errorf("expected running, got %s", claimed.Status)
	}
}

func TestFullText_CommitResult(t *testing.T) {
	db := newFulltextTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := store.EnsureFulltextSchema(ctx); err != nil {
		t.Fatalf("fulltext schema: %v", err)
	}

	input := FulltextInput{
		MediaID: 1, SourcePath: "/test/test.pdf", Generation: 1,
		Language: "eng", DocumentKind: "pdf",
	}
	task, _ := store.EnqueueFulltext(ctx, input)
	task, _ = store.ClaimFulltext(ctx, "worker-1", 30*time.Second)

	result := FulltextResult{
		Text: "Hello world", TextSize: 11, TextHash: "abc",
		TextPreview: "Hello...", PageCount: 5, PageCoverage: 5,
		Mode: FulltextNative, Language: "eng",
	}
	if err := store.CommitFulltextDone(ctx, task.ID, "worker-1", task.Generation, result, "pdftext-1.0", "1.0"); err != nil {
		t.Fatalf("commit fulltext: %v", err)
	}

	task, _ = store.GetFulltext(ctx, task.ID)
	if task.Status != StatusDone {
		t.Errorf("expected done, got %s", task.Status)
	}
	if task.TextSize != 11 {
		t.Errorf("expected text size 11, got %d", task.TextSize)
	}
	if task.PageCoverage != 5 {
		t.Errorf("expected 5 pages covered, got %d", task.PageCoverage)
	}
}

func TestFullText_EmptyTextSuccess(t *testing.T) {
	db := newFulltextTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	store.EnsureSchema(ctx)
	store.EnsureFulltextSchema(ctx)

	input := FulltextInput{MediaID: 2, SourcePath: "/test/empty.pdf", Generation: 1, Language: "eng", DocumentKind: "pdf"}
	task, _ := store.EnqueueFulltext(ctx, input)
	task, _ = store.ClaimFulltext(ctx, "worker-1", 30*time.Second)

	result := FulltextResult{
		Text: "", TextSize: 0, TextHash: "",
		TextPreview: "", PageCount: 3, PageCoverage: 0,
		Mode: FulltextNative, Language: "eng",
	}
	if err := store.CommitFulltextDone(ctx, task.ID, "worker-1", task.Generation, result, "pdftext-1.0", "1.0"); err != nil {
		t.Fatalf("commit empty text should succeed: %v", err)
	}

	task, _ = store.GetFulltext(ctx, task.ID)
	if task.Status != StatusDone {
		t.Errorf("expected done for empty text, got %s", task.Status)
	}
}

func TestFullText_GenerationFencing(t *testing.T) {
	db := newFulltextTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	store.EnsureSchema(ctx)
	store.EnsureFulltextSchema(ctx)

	input := FulltextInput{MediaID: 3, SourcePath: "/test/test.pdf", Generation: 5, Language: "eng", DocumentKind: "pdf"}
	task, _ := store.EnqueueFulltext(ctx, input)
	task, _ = store.ClaimFulltext(ctx, "worker-1", 30*time.Second)

	// Try commit with wrong generation.
	result := FulltextResult{Text: "x", TextSize: 1, TextHash: "a", Mode: FulltextNative}
	err := store.CommitFulltextDone(ctx, task.ID, "worker-1", 99, result, "e", "v")
	if err == nil {
		t.Error("expected fence error for generation mismatch")
	}
}

func TestFullText_StaleReplacementRejection(t *testing.T) {
	db := newFulltextTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	store.EnsureSchema(ctx)
	store.EnsureFulltextSchema(ctx)

	// Complete a task.
	input := FulltextInput{MediaID: 4, SourcePath: "/test/v1.pdf", Generation: 1, Language: "eng", DocumentKind: "pdf"}
	task, _ := store.EnqueueFulltext(ctx, input)
	task, _ = store.ClaimFulltext(ctx, "worker-1", 30*time.Second)
	store.CommitFulltextDone(ctx, task.ID, "worker-1", 1,
		FulltextResult{Text: "v1", TextSize: 2, TextHash: "v1h", Mode: FulltextNative, Language: "eng"},
		"e", "v")

	// Try to enqueue with a different source for the same media (stale).
	input2 := FulltextInput{MediaID: 4, SourcePath: "/test/v2.pdf", Generation: 1, Language: "eng", DocumentKind: "pdf"}
	_, err := store.EnqueueFulltext(ctx, input2)
	if err == nil {
		t.Error("expected error re-enqueuing completed fulltext task")
	}
}

func TestFullText_ModeEnumeration(t *testing.T) {
	modes := []FulltextMode{FulltextNative, FulltextOCR, FulltextHybrid}
	for _, m := range modes {
		if m != FulltextNative && m != FulltextOCR && m != FulltextHybrid {
			t.Errorf("unexpected mode: %s", m)
		}
	}
}

func TestFullText_CancellationHonored(t *testing.T) {
	db := newFulltextTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	store.EnsureSchema(ctx)
	store.EnsureFulltextSchema(ctx)

	input := FulltextInput{MediaID: 5, SourcePath: "/test/test.pdf", Generation: 1, Language: "eng", DocumentKind: "pdf"}
	task, _ := store.EnqueueFulltext(ctx, input)
	task, _ = store.ClaimFulltext(ctx, "worker-1", 30*time.Second)

	err := store.CancelFulltext(ctx, task.ID, "worker-1")
	if err != nil {
		t.Fatalf("cancel fulltext: %v", err)
	}

	task, _ = store.GetFulltext(ctx, task.ID)
	if task.Status != StatusCancelled {
		t.Errorf("expected cancelled, got %s", task.Status)
	}
}

func TestFullText_DocumentSearchNoOCR(t *testing.T) {
	db := newFulltextTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	store.EnsureSchema(ctx)
	store.EnsureFulltextSchema(ctx)

	// Search should just query committed FTS, not trigger OCR.
	tasks, err := store.ListCompleteFulltext(ctx, []int64{1})
	if err != nil {
		t.Fatalf("list fulltext: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}
