package doccover

import (
	"context"
	"database/sql"
	"knox-media/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCoverJobTimeoutSec(t *testing.T) {
	if got := coverJobTimeoutSec(nil); got != minCoverJobTimeoutSec {
		t.Fatalf("nil fn: got %d want %d", got, minCoverJobTimeoutSec)
	}
	fn := func() int { return 180 }
	if got := coverJobTimeoutSec(fn); got != minCoverJobTimeoutSec {
		t.Fatalf("180: got %d want %d", got, minCoverJobTimeoutSec)
	}
	fn = func() int { return 900 }
	if got := coverJobTimeoutSec(fn); got != 900 {
		t.Fatalf("900: got %d want 900", got)
	}
}

func TestRunOneContextPreCancelledSkipsEnsure(t *testing.T) {
	db := docCoverTestDB(t)
	mediaID := seedDocCoverMedia(t, db, t.TempDir())
	called := false
	w := NewWorker(WorkerConfig{DB: db, PreviewDir: t.TempDir(), EnsureFunc: func(context.Context, Options, int64, string, int64) error { called = true; return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.RunOnceContext(ctx, mediaID)
	if called {
		t.Fatal("Ensure called after parent cancellation")
	}
}

func TestRunOneContextCancelsBlockingEnsure(t *testing.T) {
	db := docCoverTestDB(t)
	mediaID := seedDocCoverMedia(t, db, t.TempDir())
	started := make(chan struct{})
	w := NewWorker(WorkerConfig{DB: db, PreviewDir: t.TempDir(), EnsureFunc: func(ctx context.Context, _ Options, _ int64, _ string, _ int64) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.RunOnceContext(ctx, mediaID); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunOnceContext ignored cancellation")
	}
}

func docCoverTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "doccover.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedDocCoverMedia(t *testing.T, db *sql.DB, root string) int64 {
	t.Helper()
	file := filepath.Join(root, "doc.pdf")
	if err := os.WriteFile(file, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	lib, err := db.Exec(`INSERT INTO library(name,type,path) VALUES('docs','document',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := lib.LastInsertId()
	res, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,title,file_type,status,file_mtime) VALUES(?,'doc',?,'doc','document','active',1)`, libraryID, file)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}
