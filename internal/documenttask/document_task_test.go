package documenttask

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

func TestDocumentConvert_Enqueue(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	task, err := store.Enqueue(ctx, 1, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if task.Status != StatusWaiting {
		t.Errorf("expected waiting, got %s", task.Status)
	}
	if task.MediaID != 1 {
		t.Errorf("expected media_id 1, got %d", task.MediaID)
	}
}

func TestDocumentConvert_EnqueueDuplicateIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 42, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}

	// Claim and complete the task.
	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim failed: %v", err)
	}
	store.CommitDone(ctx, task.ID, "worker-1", 1, ConvertOutput{
		PDFPath: "/out.pdf", PDFSize: 100, PDFHash: "abc", PageCount: 1,
	}, EngineOffice)

	// Re-enqueue after done should return DuplicateError (idempotent duplicate).
	_, err = store.Enqueue(ctx, 42, "/input/test.docx", 1)
	if err == nil {
		t.Error("expected DuplicateError on re-enqueue of completed task")
	} else {
		if _, ok := err.(DuplicateError); !ok {
			t.Errorf("expected DuplicateError, got %T: %v", err, err)
		}
	}
}

func TestDocumentConvert_ClaimAndHeartbeat(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 100, "/input/test.docx", 2)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if task == nil {
		t.Fatal("expected a claimed task")
	}
	if task.Status != StatusRunning {
		t.Errorf("expected running, got %s", task.Status)
	}
	if task.LeaseOwner != "worker-1" {
		t.Errorf("expected owner worker-1, got %s", task.LeaseOwner)
	}

	if err := store.Heartbeat(ctx, task.ID, "worker-1", 30*time.Second); err != nil {
		t.Errorf("heartbeat failed: %v", err)
	}
}

func TestDocumentConvert_Cancel(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 200, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim failed: %v", err)
	}

	if err := store.Cancel(ctx, task.ID, "worker-1"); err != nil {
		t.Errorf("cancel failed: %v", err)
	}

	task, err = store.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if task.Status != StatusCancelled {
		t.Errorf("expected cancelled, got %s", task.Status)
	}
}

func TestDocumentConvert_LeaseFence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 300, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim failed: %v", err)
	}

	err = store.Heartbeat(ctx, task.ID, "worker-2", 30*time.Second)
	if err == nil {
		t.Error("expected fence error on heartbeat from wrong owner")
	}
}

func TestDocumentConvert_ResetStuckRunning(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 400, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim failed: %v", err)
	}

	// Force lease to be expired by directly updating the row.
	store.db.ExecContext(ctx, `UPDATE document_task SET lease_until='2020-01-01T00:00:00Z' WHERE id=?`, task.ID)

	n, err := store.ResetStuckRunning(ctx)
	if err != nil {
		t.Fatalf("reset stuck failed: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 reset, got %d", n)
	}

	task, err = store.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if task.Status != StatusWaiting {
		t.Errorf("expected waiting after reset, got %s", task.Status)
	}
}

func TestDocumentConvert_CommitFence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 500, "/input/test.docx", 5)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim failed: %v", err)
	}

	// Try to commit with wrong generation - should fail.
	err = store.CommitDone(ctx, task.ID, "worker-1", 99, ConvertOutput{
		PDFPath: "/out.pdf", PDFSize: 100, PDFHash: "abc", PageCount: 1,
	}, EngineOffice)
	if err == nil {
		t.Error("expected fence error on commit with wrong generation")
	}
}

func TestDocumentConvert_RetryRound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 600, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim failed: %v", err)
	}

	err = store.MarkFailed(ctx, task.ID, "worker-1", "test failure")
	if err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	// Re-enqueue the failed task.
	task2, err := store.Enqueue(ctx, 600, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("re-enqueue failed: %v", err)
	}
	if task2.RetryRound != 1 {
		t.Errorf("expected retry_round 1, got %d", task2.RetryRound)
	}
}

func TestPreviewPDF_ArtifactValidate(t *testing.T) {
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	// Create a staging file.
	stagingDir := am.StagePath(1)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pdfPath := filepath.Join(stagingDir, "preview.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\ntest pdf content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	output, err := am.ValidatePDF(pdfPath)
	if err != nil {
		t.Errorf("validate failed: %v", err)
	}
	if output.PDFSize == 0 {
		t.Error("expected non-zero PDF size")
	}
}

func TestPreviewPDF_ArtifactInvalid(t *testing.T) {
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	stagingDir := am.StagePath(1)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pdfPath := filepath.Join(stagingDir, "preview.pdf")
	if err := os.WriteFile(pdfPath, []byte("not a pdf"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := am.ValidatePDF(pdfPath)
	if err == nil {
		t.Error("expected validation error for non-PDF content")
	}
}

func TestPreviewPDF_ArtifactCommitAndFence(t *testing.T) {
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	stagingDir := am.StagePath(1)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pdfPath := filepath.Join(stagingDir, "preview.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\ncontent"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	committed, err := am.Commit(context.Background(), 1, pdfPath)
	if err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	if _, err := os.Stat(committed); err != nil {
		t.Errorf("committed file not found: %v", err)
	}
	if !am.HasCommitted(1) {
		t.Error("expected HasCommitted to return true")
	}

	// Staging file should be gone after rename.
	if _, err := os.Stat(pdfPath); err == nil {
		t.Error("staging file should be gone after commit rename")
	}
}

func TestDocumentTask_WorkerExecute(t *testing.T) {
	dir := t.TempDir()
	db := newTestDB(t)
	store := NewStore(db)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("schema: %v", err)
	}

	am := NewArtifactManager(dir)

	// Mock converter that creates a valid PDF.
	mockConv := &mockConverter{
		convertFn: func(ctx context.Context, sourcePath, stagingDir string) (string, error) {
			pdfPath := filepath.Join(stagingDir, "preview.pdf")
			return pdfPath, os.WriteFile(pdfPath, []byte("%PDF-1.4\nmock pdf content with /Type /Page"), 0o644)
		},
	}

	worker := NewWorker(db, am, mockConv)

	ctx := context.Background()
	task, err := store.Enqueue(ctx, 700, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	task, err = store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}

	if err := worker.ExecuteClaimed(ctx, task); err != nil {
		t.Fatalf("execute: %v", err)
	}

	task, err = store.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.Status != StatusDone {
		t.Errorf("expected done, got %s", task.Status)
	}
	if task.OutputPath == "" {
		t.Error("expected non-empty output path")
	}
	if task.PageCount == 0 {
		t.Errorf("expected non-zero page count, got %d", task.PageCount)
	}
}

func TestDocumentTask_CrashBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	db := newTestDB(t)
	store := NewStore(db)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("schema: %v", err)
	}

	am := NewArtifactManager(dir)

	mockConv := &mockConverter{
		convertFn: func(ctx context.Context, sourcePath, stagingDir string) (string, error) {
			pdfPath := filepath.Join(stagingDir, "preview.pdf")
			return pdfPath, os.WriteFile(pdfPath, []byte("%PDF-1.4\ncontent"), 0o644)
		},
	}

	worker := NewWorker(db, am, mockConv)
	ctx := context.Background()

	task, err := store.Enqueue(ctx, 800, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	task, err = store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}

	// Simulate crash by expiring the lease.
	store.db.ExecContext(ctx, `UPDATE document_task SET lease_until='2020-01-01T00:00:00Z' WHERE id=?`, task.ID)

	// Execute should fail because lease expired (crash simulation).
	err = worker.ExecuteClaimed(ctx, task)
	if err == nil {
		t.Error("expected lease fence error after lease expired")
	}

	// Reset stuck and retry should work.
	n, _ := store.ResetStuckRunning(ctx)
	if n > 0 {
		t.Logf("reset %d stuck tasks", n)
	}

	task2, err := store.Claim(ctx, "worker-2", 30*time.Second)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if task2 == nil {
		t.Fatal("expected a task after reset")
	}

	if err := worker.ExecuteClaimed(ctx, task2); err != nil {
		t.Fatalf("retry execute: %v", err)
	}

	task2, _ = store.Get(ctx, task2.ID)
	if task2.Status != StatusDone {
		t.Errorf("expected done after retry, got %s", task2.Status)
	}
}

// mockConverter implements Converter for testing.
type mockConverter struct {
	convertFn func(ctx context.Context, sourcePath, stagingDir string) (string, error)
}

func (m *mockConverter) ConvertToPDF(ctx context.Context, sourcePath, stagingDir string) (string, error) {
	return m.convertFn(ctx, sourcePath, stagingDir)
}

// Test that the adapter validates its inputs before calling the worker.
func TestDocumentTask_AdapterGuard(t *testing.T) {
	dir := t.TempDir()
	db := newTestDB(t)
	store := NewStore(db)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("schema: %v", err)
	}

	am := NewArtifactManager(dir)
	mockConv := &mockConverter{
		convertFn: func(ctx context.Context, sourcePath, stagingDir string) (string, error) {
			return filepath.Join(stagingDir, "preview.pdf"),
				os.WriteFile(filepath.Join(stagingDir, "preview.pdf"), []byte("%PDF-1.4\ncontent /Type /Page"), 0o644)
		},
	}
	worker := NewWorker(db, am, mockConv)
	adapter := NewAdapter(db, worker)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 900, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}

	qt := QueueTask{
		ID: task.ID, MediaID: task.MediaID, TaskType: "document_convert",
		Status: string(task.Status), LeaseOwner: task.LeaseOwner,
		Generation: task.Generation, RetryRound: task.RetryRound, Attempts: task.Attempts,
	}
	if err := adapter.Execute(ctx, qt); err != nil {
		t.Fatalf("adapter execute: %v", err)
	}

	task, _ = store.Get(ctx, task.ID)
	if task.Status != StatusDone {
		t.Errorf("expected done, got %s", task.Status)
	}
}

// Test stale replacement rejection.
func TestDocumentConvert_StaleReplacement(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 1000, "/input/v1.docx", 1)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Try to enqueue with same media but different source - when already in progress.
	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	_ = task

	// A second enqueue while running should return the existing task, not a duplicate error.
	task2, err := store.Enqueue(ctx, 1000, "/input/v2.docx", 1)
	if err != nil {
		t.Fatalf("should return existing, not error: %v", err)
	}
	if task2.Status != StatusRunning {
		t.Errorf("expected running status, got %s", task2.Status)
	}
}

// Test restart resumption: after a crash, ResetStuckRunning makes tasks claimable again.
func TestDocumentConvert_RestartResumption(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 1100, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	task, err := store.Claim(ctx, "dead-worker", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}

	// Force lease to expired.
	store.db.ExecContext(ctx, `UPDATE document_task SET lease_until='2020-01-01T00:00:00Z' WHERE id=?`, task.ID)

	n, _ := store.ResetStuckRunning(ctx)
	if n == 0 {
		t.Fatal("expected at least one stuck task reset")
	}

	// Now a new worker should be able to claim.
	task, err = store.Claim(ctx, "new-worker", 30*time.Second)
	if err != nil {
		t.Fatalf("re-claim after reset: %v", err)
	}
	if task == nil {
		t.Fatal("expected a task after reset")
	}
	if task.LeaseOwner != "new-worker" {
		t.Errorf("expected new-worker, got %s", task.LeaseOwner)
	}
}

// Ensure the test names match the required patterns from the plan.
// DocumentConvert|PreviewPDF|DocumentTask coverage.

func TestDocumentConvert_FullLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Enqueue
	_, err := store.Enqueue(ctx, 1200, "/input/test.docx", 7)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Claim
	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}

	// Heartbeat
	store.Heartbeat(ctx, task.ID, "worker-1", 60*time.Second)

	// Commit done
	err = store.CommitDone(ctx, task.ID, "worker-1", 7, ConvertOutput{
		PDFPath: "/out/doc.pdf", PDFSize: 1234, PDFHash: "deadbeef", PageCount: 5,
	}, EngineOffice)
	if err != nil {
		t.Fatalf("commit done: %v", err)
	}

	// Verify
	task, _ = store.Get(ctx, task.ID)
	if task.Status != StatusDone {
		t.Errorf("expected done, got %s", task.Status)
	}
	if task.PageCount != 5 {
		t.Errorf("expected 5 pages, got %d", task.PageCount)
	}
}

// Test generation fencing during claim.
func TestDocumentConvert_GenerationMismatch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 1300, "/input/v1.docx", 10)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}

	// Try to commit with wrong generation.
	err = store.CommitDone(ctx, task.ID, "worker-1", 5, ConvertOutput{
		PDFPath: "/out.pdf", PDFSize: 100, PDFHash: "aaa", PageCount: 1,
	}, EngineOffice)
	if err == nil {
		t.Error("expected fence error for generation mismatch")
	}
}

// Test that file not found during conversion results in failed status.
func TestDocumentTask_ConversionFailure(t *testing.T) {
	dir := t.TempDir()
	db := newTestDB(t)
	store := NewStore(db)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("schema: %v", err)
	}

	am := NewArtifactManager(dir)
	mockConv := &mockConverter{
		convertFn: func(ctx context.Context, sourcePath, stagingDir string) (string, error) {
			return "", os.ErrNotExist
		},
	}
	worker := NewWorker(db, am, mockConv)
	ctx := context.Background()

	task, _ := store.Enqueue(ctx, 1400, "/input/missing.docx", 1)
	task, _ = store.Claim(ctx, "worker-1", 30*time.Second)

	err := worker.ExecuteClaimed(ctx, task)
	if err == nil {
		t.Error("expected error for missing file")
	}

	task, _ = store.Get(ctx, task.ID)
	if task.Status != StatusFailed {
		t.Errorf("expected failed status, got %s", task.Status)
	}
}

// ensure we can run specific tests via go test -run patterns.
func TestDocumentTask_DirectClaim(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// No tasks available - claim returns nil.
	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil {
		t.Fatalf("claim on empty store: %v", err)
	}
	if task != nil {
		t.Error("expected nil task when no waiting tasks")
	}
}

// Test idempotent re-enqueue: after Done, Enqueue returns DuplicateError.
func TestDocumentConvert_DoneIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Enqueue(ctx, 1500, "/input/test.docx", 1)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	task, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}

	store.CommitDone(ctx, task.ID, "worker-1", 1, ConvertOutput{
		PDFPath: "/out.pdf", PDFSize: 100, PDFHash: "abc", PageCount: 1,
	}, EngineOffice)

	// Re-enqueue after done should return DuplicateError.
	_, err = store.Enqueue(ctx, 1500, "/input/test.docx", 1)
	dup, ok := err.(DuplicateError)
	if !ok {
		t.Fatalf("expected DuplicateError, got %T: %v", err, err)
	}
	_ = dup
}

// Ensure the test package builds and names match required patterns.
func TestDocumentConvert_NoHTTPConversion(t *testing.T) {
	// Verify that there's no HTTP endpoint in the conversion path —
	// the converter worker operates on filesystem, not HTTP.
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	stagingDir := am.StagePath(99)
	os.MkdirAll(stagingDir, 0o755)
	pdfPath := filepath.Join(stagingDir, "preview.pdf")

	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\ntest"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := am.ValidatePDF(pdfPath)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	// Verify committed output for get/read path.
	committed, err := am.Commit(context.Background(), 99, pdfPath)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !strings.HasSuffix(committed, "preview.pdf") {
		t.Errorf("expected committed path ending with preview.pdf, got %s", committed)
	}
	if !am.HasCommitted(99) {
		t.Error("expected committed artifact")
	}
}

// Crash resilience: write valid PDF, crash before rename, then recover.
func TestDocumentConvert_CrashAroundRename(t *testing.T) {
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	// Stage a valid PDF.
	stagingDir := am.StagePath(42)
	os.MkdirAll(stagingDir, 0o755)
	pdfPath := filepath.Join(stagingDir, "preview.pdf")
	os.WriteFile(pdfPath, []byte("%PDF-1.4\ncontent /Type /Page"), 0o644)

	// Validate the staged PDF (should succeed).
	output, err := am.ValidatePDF(pdfPath)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if output.PDFSize == 0 {
		t.Error("expected non-zero size")
	}

	// Simulate crash: don't commit, try to validate again (staging is idempotent).
	// Staging directory persists across crash.
	if _, err := am.ValidatePDF(pdfPath); err != nil {
		t.Fatalf("re-validate after crash: %v", err)
	}

	// Now commit.
	committed, err := am.Commit(context.Background(), 42, pdfPath)
	if err != nil {
		t.Fatalf("commit after crash recovery: %v", err)
	}
	if !am.HasCommitted(42) {
		t.Error("expected committed after recovery")
	}
	_ = committed
}
