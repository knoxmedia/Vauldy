package taskcontrol

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupProjectionTestDB creates an in-memory SQLite database with the
// post_ingest_task and task projection tables, then returns the DB and
// a ProjectionBuilder with an oracle adapter.
func setupProjectionTestDB(t *testing.T) (*sql.DB, *ProjectionBuilder) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE post_ingest_task (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL DEFAULT 0,
			scan_task_id INTEGER,
			generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
			task_type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'waiting',
			attempts INTEGER NOT NULL DEFAULT 0,
			retry_round INTEGER NOT NULL DEFAULT 0 CHECK(retry_round >= 0),
			max_attempts INTEGER NOT NULL DEFAULT 3,
			available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			lease_owner TEXT,
			lease_until TIMESTAMP,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			priority INTEGER NOT NULL DEFAULT 0,
			removed_at TIMESTAMP,
			removed_by TEXT NOT NULL DEFAULT '',
			remove_reason TEXT NOT NULL DEFAULT '',
			source_class INTEGER NOT NULL DEFAULT 0,
			base_priority INTEGER NOT NULL DEFAULT 0,
			library_id INTEGER,
			run_now_expires TIMESTAMP
		)`,
		`CREATE TABLE task_projection_sequence (
			singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
			next_revision INTEGER NOT NULL DEFAULT 1 CHECK (next_revision >= 1)
		)`,
		`CREATE TABLE task_projection_revision (
			task_identity TEXT PRIMARY KEY,
			revision INTEGER NOT NULL UNIQUE,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE task_abort_intent (
			task_identity TEXT PRIMARY KEY,
			requested_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			requested_by TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			owner_fence TEXT NOT NULL DEFAULT '',
			deadline TIMESTAMP,
			acknowledged_at TIMESTAMP,
			outcome TEXT NOT NULL DEFAULT '',
			recovery_required_at TIMESTAMP
		)`,
		`INSERT INTO task_projection_sequence(singleton_id, next_revision) VALUES(1, 1)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("create schema: %v\n%s", err, stmt)
		}
	}
	builder := NewProjectionBuilder(db, NewRegistry())
	builder.RegisterAdapter(NewOracleAdapter(db))
	return db, builder
}

// insertOracleTask inserts a post_ingest_task row and returns its id.
func insertOracleTask(t *testing.T, db *sql.DB, typ, status string, opts map[string]any) int64 {
	t.Helper()
	ctx := context.Background()
	cols := []string{"task_type", "status"}
	vals := []any{typ, status}
	args := []string{"?", "?"}
	for k, v := range opts {
		cols = append(cols, k)
		vals = append(vals, v)
		args = append(args, "?")
	}
	query := fmt.Sprintf("INSERT INTO post_ingest_task(%s) VALUES(%s)",
		joinCols(cols), joinPlaceholders(len(args)))
	res, err := db.ExecContext(ctx, query, vals...)
	if err != nil {
		t.Fatalf("insert %s/%s: %v", typ, status, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func joinCols(cols []string) string {
	s := ""
	for i, c := range cols {
		if i > 0 {
			s += ", "
		}
		s += c
	}
	return s
}

func joinPlaceholders(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ", "
		}
		s += "?"
	}
	return s
}

// --- Normalization Tests ---

func TestNormalizationWaitingStatuses(t *testing.T) {
	tests := []string{"waiting", "pending", "queued", "ready"}
	for _, raw := range tests {
		got := normalizeStatus(raw, false)
		if got != StatusWaiting {
			t.Errorf("normalizeStatus(%q, false) = %q, want %q", raw, got, StatusWaiting)
		}
	}
}

func TestNormalizationRunningStatuses(t *testing.T) {
	tests := []string{"running", "processing", "active", "in_progress"}
	for _, raw := range tests {
		got := normalizeStatus(raw, false)
		if got != StatusRunning {
			t.Errorf("normalizeStatus(%q, false) = %q, want %q", raw, got, StatusRunning)
		}
	}
}

func TestNormalizationDoneStatuses(t *testing.T) {
	tests := []string{"done", "completed", "success", "finished"}
	for _, raw := range tests {
		got := normalizeStatus(raw, false)
		if got != StatusDone {
			t.Errorf("normalizeStatus(%q, false) = %q, want %q", raw, got, StatusDone)
		}
	}
}

func TestNormalizationFailedStatuses(t *testing.T) {
	tests := []string{"failed", "error", "permanent_failure"}
	for _, raw := range tests {
		got := normalizeStatus(raw, false)
		if got != StatusFailed {
			t.Errorf("normalizeStatus(%q, false) = %q, want %q", raw, got, StatusFailed)
		}
	}
}

func TestNormalizationCancelledStatuses(t *testing.T) {
	tests := []string{"cancelled", "canceled", "aborted"}
	for _, raw := range tests {
		got := normalizeStatus(raw, false)
		if got != StatusCancelled {
			t.Errorf("normalizeStatus(%q, false) = %q, want %q", raw, got, StatusCancelled)
		}
	}
}

func TestNormalizationSkippedStatuses(t *testing.T) {
	tests := []string{"skipped", "bypass"}
	for _, raw := range tests {
		got := normalizeStatus(raw, false)
		if got != StatusSkipped {
			t.Errorf("normalizeStatus(%q, false) = %q, want %q", raw, got, StatusSkipped)
		}
	}
}

func TestNormalizationUnknownFallsBackToWaiting(t *testing.T) {
	got := normalizeStatus("unknown_state", false)
	if got != StatusWaiting {
		t.Errorf("normalizeStatus(\"unknown_state\", false) = %q, want waiting", got)
	}
}

func TestNormalizationUnknownWithTerminalEvidence(t *testing.T) {
	got := normalizeStatus("unknown_state", true)
	if got != StatusFailed {
		t.Errorf("normalizeStatus(\"unknown_state\", true) = %q, want failed", got)
	}
}

func TestNormalizedStatusIsTerminal(t *testing.T) {
	for _, s := range AllNormalizedStatuses {
		terminal := s.IsTerminal()
		switch s {
		case StatusDone, StatusFailed, StatusCancelled, StatusSkipped:
			if !terminal {
				t.Errorf("%q.IsTerminal() = false, want true", s)
			}
		case StatusWaiting, StatusRunning:
			if terminal {
				t.Errorf("%q.IsTerminal() = true, want false", s)
			}
		}
	}
}

// --- Identity Tests ---

func TestBuildIdentityFormat(t *testing.T) {
	got := BuildIdentity("orchestration", 481)
	if got != "orchestration:481" {
		t.Errorf("BuildIdentity = %q, want %q", got, "orchestration:481")
	}
}

func TestParseIdentityValid(t *testing.T) {
	kind, id, err := parseIdentity("orchestration:481")
	if err != nil {
		t.Fatalf("parseIdentity: %v", err)
	}
	if kind != "orchestration" {
		t.Errorf("kind = %q, want %q", kind, "orchestration")
	}
	if id != 481 {
		t.Errorf("id = %d, want 481", id)
	}
}

func TestParseIdentityInvalidNoColon(t *testing.T) {
	_, _, err := parseIdentity("orchestration481")
	if err == nil {
		t.Error("expected error for identity without colon")
	}
}

func TestParseIdentityInvalidID(t *testing.T) {
	_, _, err := parseIdentity("orchestration:abc")
	if err == nil {
		t.Error("expected error for identity with non-numeric id")
	}
}

// --- Projection Tests ---

func TestProjectionWaitingTask(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "poster", "waiting", map[string]any{
		"media_id":      100,
		"generation":    1,
		"base_priority": 300,
	})
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row == nil {
		t.Fatal("expected row, got nil")
	}
	if row.NormalizedStatus != StatusWaiting {
		t.Errorf("normalized_status = %q, want %q", row.NormalizedStatus, StatusWaiting)
	}
	if row.TaskType != "poster" {
		t.Errorf("task_type = %q, want poster", row.TaskType)
	}
	if row.SourceKind != "orchestration" {
		t.Errorf("source_kind = %q, want orchestration", row.SourceKind)
	}
	if row.Generation != 1 {
		t.Errorf("generation = %d, want 1", row.Generation)
	}
}

func TestProjectionRunningTask(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "transcode", "running", map[string]any{
		"lease_owner": "worker-1",
		"attempts":    2,
	})
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.NormalizedStatus != StatusRunning {
		t.Errorf("normalized_status = %q, want %q", row.NormalizedStatus, StatusRunning)
	}
	if row.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", row.Attempt)
	}
	if row.OwnerLease == nil || row.OwnerLease.Owner != "worker-1" {
		t.Error("expected owner lease 'worker-1'")
	}
}

func TestProjectionDoneTask(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "keyframe", "done", nil)
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.NormalizedStatus != StatusDone {
		t.Errorf("normalized_status = %q, want %q", row.NormalizedStatus, StatusDone)
	}
	if !row.NormalizedStatus.IsTerminal() {
		t.Error("done status should be terminal")
	}
}

func TestProjectionFailedTask(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "poster", "failed", map[string]any{
		"last_error": "disk full",
	})
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.NormalizedStatus != StatusFailed {
		t.Errorf("normalized_status = %q, want %q", row.NormalizedStatus, StatusFailed)
	}
	if row.TerminalReason != "disk full" {
		t.Errorf("terminal_reason = %q, want 'disk full'", row.TerminalReason)
	}
}

func TestProjectionCancelledTask(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "atrack_extract", "cancelled", nil)
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.NormalizedStatus != StatusCancelled {
		t.Errorf("normalized_status = %q, want %q", row.NormalizedStatus, StatusCancelled)
	}
}

func TestProjectionSkippedTask(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "ai_analysis", "skipped", nil)
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.NormalizedStatus != StatusSkipped {
		t.Errorf("normalized_status = %q, want %q", row.NormalizedStatus, StatusSkipped)
	}
}

func TestProjectionGenerationFencing(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	// Insert task with generation 5 (current)
	id := insertOracleTask(t, db, "thumbnail", "running", map[string]any{
		"generation": 5,
	})
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.Generation != 5 {
		t.Errorf("generation = %d, want 5", row.Generation)
	}
}

func TestProjectionRetryRound(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "encrypt", "running", map[string]any{
		"retry_round": 3,
		"attempts":    1,
		"max_attempts": 5,
	})
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.RetryRound != 3 {
		t.Errorf("retry_round = %d, want 3", row.RetryRound)
	}
	if row.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", row.Attempt)
	}
	if row.MaxAttempts != 5 {
		t.Errorf("max_attempts = %d, want 5", row.MaxAttempts)
	}
}

func TestProjectionRemovedTask(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Second)
	id := insertOracleTask(t, db, "subtitle_extract", "cancelled", map[string]any{
		"removed_at":    now,
		"removed_by":    "admin",
		"remove_reason": "cleanup",
	})
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.RemovedAt == nil || !row.RemovedAt.Equal(now) {
		t.Errorf("removed_at mismatch")
	}
	if row.RemovedBy != "admin" {
		t.Errorf("removed_by = %q, want admin", row.RemovedBy)
	}
	if row.RemoveReason != "cleanup" {
		t.Errorf("remove_reason = %q, want cleanup", row.RemoveReason)
	}
}

func TestProjectionMissingIdentity(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	row, err := builder.Project(context.Background(), "orchestration:99999")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row != nil {
		t.Fatal("expected nil row for nonexistent identity")
	}
}

func TestProjectionInvalidIdentityFormat(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	_, err := builder.Project(context.Background(), "invalid-format")
	if err == nil {
		t.Error("expected error for invalid identity format")
	}
}

func TestProjectionUnknownSourceKind(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	row, err := builder.Project(context.Background(), "unknown_kind:1")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row != nil {
		t.Fatal("expected nil row for unregistered source kind")
	}
}

func TestProjectionFamilyResolution(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "transcode", "waiting", nil)
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.Family != "video_post_processing" {
		t.Errorf("family = %q, want video_post_processing", row.Family)
	}
}

func TestProjectionPriority(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "preview", "waiting", map[string]any{
		"base_priority": 500,
		"priority":      50,
	})
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.BasePriority != 500 {
		t.Errorf("base_priority = %d, want 500", row.BasePriority)
	}
}

func TestProjectionRevisionAllocated(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	rev, err := builder.StoreRevision(ctx, tx, "orchestration:100")
	if err != nil {
		t.Fatalf("StoreRevision: %v", err)
	}
	if rev < 1 {
		t.Errorf("revision = %d, want >= 1", rev)
	}
	// Second revision must be strictly greater
	rev2, err := builder.StoreRevision(ctx, tx, "orchestration:200")
	if err != nil {
		t.Fatalf("StoreRevision 2: %v", err)
	}
	if rev2 <= rev {
		t.Errorf("revision2 = %d, must be > %d", rev2, rev)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestProjectionRevisionReadback(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	rev, err := builder.StoreRevision(ctx, tx, "orchestration:500")
	if err != nil {
		t.Fatalf("StoreRevision: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Read back via ProjectionRevision
	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	defer tx2.Rollback()

	stored, err := builder.projectionRevision(ctx, tx2, "orchestration:500")
	if err != nil {
		t.Fatalf("projectionRevision: %v", err)
	}
	if stored != rev {
		t.Errorf("stored revision = %d, want %d", stored, rev)
	}
	_ = tx2.Rollback()
}

func TestProjectionSnapshotRevision(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	snap, err := builder.snapshotRevision(ctx, tx)
	if err != nil {
		t.Fatalf("snapshotRevision: %v", err)
	}
	if snap != 0 {
		t.Errorf("snapshot revision before any writes = %d, want 0", snap)
	}

	if _, err := builder.StoreRevision(ctx, tx, "orchestration:1"); err != nil {
		t.Fatalf("StoreRevision: %v", err)
	}
	snap, err = builder.snapshotRevision(ctx, tx)
	if err != nil {
		t.Fatalf("snapshotRevision after write: %v", err)
	}
	if snap < 1 {
		t.Errorf("snapshot revision after write = %d, want >= 1", snap)
	}
	_ = tx.Rollback()
}

func TestProjectionEffectivePriorityComputation(t *testing.T) {
	ep := EffectivePriority(300, 50, 120, 2)
	if ep != 354 {
		t.Errorf("EffectivePriority(300,50,120,2) = %d, want 354", ep)
	}
}

func TestProjectionNullLibraryID(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "subtitle_recognize", "waiting", nil)
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.LibraryID != nil {
		t.Errorf("expected nil LibraryID, got %v", *row.LibraryID)
	}
}

func TestProjectionMediaID(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "package", "waiting", map[string]any{
		"media_id": int64(555),
	})
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if row.MediaID == nil || *row.MediaID != 555 {
		t.Errorf("MediaID mismatch")
	}
}

func TestProjectionValidIdentityJSON(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	id := insertOracleTask(t, db, "poster", "done", nil)
	taskID := BuildIdentity("orchestration", id)

	row, err := builder.Project(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	j, err := row.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if j == "" {
		t.Error("empty JSON")
	}
	// Should contain task_id
	if !contains(j, `"task_id":"orchestration:`) {
		t.Errorf("JSON missing task_id: %s", j)
	}
}

func TestProjectionAllSixNormalizedStatuses(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	statuses := []string{"waiting", "running", "done", "failed", "cancelled", "skipped"}
	types := []string{
		"poster", "thumbnail", "preview", "keyframe", "subtitle_extract", "subtitle_recognize",
	}
	for i, rawStatus := range statuses {
		id := insertOracleTask(t, db, types[i], rawStatus, nil)
		taskID := BuildIdentity("orchestration", id)
		row, err := builder.Project(context.Background(), taskID)
		if err != nil {
			t.Fatalf("Project %s/%s: %v", types[i], rawStatus, err)
		}
		expected := NormalizedStatus(rawStatus)
		if row.NormalizedStatus != expected {
			t.Errorf("%s/%s: normalized = %q, want %q", types[i], rawStatus, row.NormalizedStatus, expected)
		}
	}
}

func TestProjectionMultipleTasksInSequence(t *testing.T) {
	db, builder := setupProjectionTestDB(t)
	defer db.Close()

	ids := make([]int64, 10)
	for i := 0; i < 10; i++ {
		ids[i] = insertOracleTask(t, db, "poster", "waiting", map[string]any{
			"base_priority": int64(100 * (i + 1)),
		})
	}
	// Project each and verify identity ordering
	for i, id := range ids {
		taskID := BuildIdentity("orchestration", id)
		row, err := builder.Project(context.Background(), taskID)
		if err != nil {
			t.Fatalf("Project task %d: %v", i, err)
		}
		if row.SourceID != id {
			t.Errorf("task %d: source_id = %d, want %d", i, row.SourceID, id)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
