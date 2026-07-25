package postingest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/imagethumb"
	"knox-media/internal/store"
)

func TestThumbnailCommitUncertainAfterActualCommitReconcilesWithoutCleanup(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	worker := realThumbnailStager(t, db)
	staged, err := worker.Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	original := withImmediateThumbnailTx
	withImmediateThumbnailTx = func(ctx context.Context, dbArg *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		outcome, err := store.WithImmediateConnTx(ctx, dbArg, fn)
		if err != nil {
			return outcome, err
		}
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("lost response after commit")}
	}
	t.Cleanup(func() { withImmediateThumbnailTx = original })
	if err = commitStagedThumbnail(context.Background(), db, *task, staged); err != nil {
		t.Fatalf("reconcile=%v", err)
	}
	for _, path := range []string{staged.Thumb.Path, staged.Medium.Path} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("committed path removed %s: %v", path, err)
		}
	}
}

func TestThumbnailCommitUncertainFailedReconcilePreservesArtifactsAndTask(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	worker := realThumbnailStager(t, db)
	staged, err := worker.Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	original := withImmediateThumbnailTx
	withImmediateThumbnailTx = func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("unknown")}
	}
	reconcileOriginal := reconcileThumbnailCommit
	reconcileThumbnailCommit = func(context.Context, *sql.DB, Task, imagethumb.StagedThumbnail) (thumbnailCommitState, error) {
		return thumbnailCommitUnknown, errors.New("authoritative query failed")
	}
	t.Cleanup(func() { withImmediateThumbnailTx = original; reconcileThumbnailCommit = reconcileOriginal })
	err = commitStagedThumbnail(context.Background(), db, *task, staged)
	var uncertain *store.ImmediateCommitError
	if !errors.As(err, &uncertain) {
		t.Fatalf("err=%T %v", err, err)
	}
	for _, path := range []string{staged.Thumb.Path, staged.Medium.Path} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("uncertain removed %s: %v", path, statErr)
		}
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, task.ID).Scan(&status)
	if status != "running" {
		t.Fatalf("task=%s", status)
	}
}

func TestThumbnailCommitUncertainProvenNotCommittedCleansUnreferencedAndPreservesTask(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, _ := q.Claim(context.Background(), TaskThumbnail)
	staged, err := realThumbnailStager(t, db).Stage(context.Background(), *task)
	if err != nil {
		t.Fatal(err)
	}
	original := withImmediateThumbnailTx
	withImmediateThumbnailTx = func(context.Context, *sql.DB, func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: errors.New("unknown before commit")}
	}
	t.Cleanup(func() { withImmediateThumbnailTx = original })
	err = commitStagedThumbnail(context.Background(), db, *task, staged)
	var uncertain *store.ImmediateCommitError
	if !errors.As(err, &uncertain) {
		t.Fatalf("err=%v", err)
	}
	for _, path := range []string{staged.Thumb.Path, staged.Medium.Path} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("uncommitted path retained %s: %v", path, statErr)
		}
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM post_ingest_task WHERE id=?`, task.ID).Scan(&status)
	if status != "running" {
		t.Fatalf("task=%s", status)
	}
}

func TestCleanupUnreferencedThumbnailPathsPreservesOnQueryError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	db, _ := sql.Open("sqlite", "file:cleanup-query-error?mode=memory&cache=shared")
	defer db.Close()
	if err := cleanupUnreferencedThumbnailPaths(context.Background(), db, []string{path}); err == nil {
		t.Fatal("expected query error")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("removed on query error: %v", err)
	}
}
