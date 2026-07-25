package postingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"knox-media/internal/storage"
	"knox-media/internal/store"
)

func TestPosterGCQuarantinesUnderImmediateLock(t *testing.T) {
	db, upload, _ := seedCurrentLinkedPosterTask(t)
	p, _ := writePosterObject(t, upload, "gc-lock", 2*time.Hour)
	orig := withImmediatePosterObjectTx
	inside := false
	withImmediatePosterObjectTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.WithImmediateConnTx(ctx, d, func(tx store.ImmediateConnTx) error { inside = true; defer func() { inside = false }(); return fn(tx) })
	}
	origRename := posterObjectRename
	posterObjectRename = func(a, b string) error {
		if !inside {
			t.Fatal("rename outside immediate tx")
		}
		return os.Rename(a, b)
	}
	t.Cleanup(func() { withImmediatePosterObjectTx = orig; posterObjectRename = origRename })
	_, cleaned, e := ReconcilePosterObjects(context.Background(), db, upload, 100, time.Hour)
	if e != nil || cleaned != 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, e)
	}
	if _, e = os.Stat(p); !os.IsNotExist(e) {
		t.Fatalf("final retained: %v", e)
	}
}
func TestPosterGCCommitBarrierRetainsAdoptedObject(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	p, url := writePosterObject(t, upload, "adopted", 2*time.Hour)
	orig := posterObjectBeforeImmediate
	posterObjectBeforeImmediate = func() {
		_, _ = db.Exec(`UPDATE media SET meta_json=json_object('scrape',json_object('poster',?)) WHERE id=?`, url, task.MediaID)
	}
	t.Cleanup(func() { posterObjectBeforeImmediate = orig })
	_, cleaned, e := ReconcilePosterObjects(context.Background(), db, upload, 100, time.Hour)
	if e != nil || cleaned != 0 {
		t.Fatalf("cleaned=%d err=%v", cleaned, e)
	}
	if _, e = os.Stat(p); e != nil {
		t.Fatalf("adopted removed: %v", e)
	}
}
func TestPosterCommitRejectsCASQuarantinedAfterSeal(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	r := realPosterStageRunner(t, db, upload)
	s, e := r.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	orig := posterAfterSealHook
	posterAfterSealHook = func() {
		final := storage.PosterObjectPath(upload, s.Hash, ".jpg")
		q := filepath.Join(upload, "posters", "objects", "sha256", "quarantine", filepath.Base(final)+".token")
		_ = os.MkdirAll(filepath.Dir(q), 0755)
		_ = os.Rename(final, q)
	}
	t.Cleanup(func() { posterAfterSealHook = orig })
	e = commitStagedPoster(context.Background(), db, task, s, PosterRecoveryRoots{Upload: upload})
	if e == nil || (!strings.Contains(e.Error(), "staged stat changed") && !strings.Contains(strings.ToLower(e.Error()), "cannot find")) {
		t.Fatalf("err=%v", e)
	}
	var meta string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, task.MediaID).Scan(&meta)
	if strings.Contains(meta, "/objects/") {
		t.Fatalf("pointer committed: %s", meta)
	}
}
func TestPosterGCQuarantineDoesNotDeleteReplacement(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	p, url := writePosterObject(t, upload, "same-content", 2*time.Hour)
	origDelete := posterObjectDeleteQuarantine
	posterObjectDeleteQuarantine = func(q string) error {
		if e := os.WriteFile(p, []byte("same-content"), 0444); e != nil {
			t.Fatal(e)
		}
		_, _ = db.Exec(`UPDATE media SET meta_json=json_object('scrape',json_object('poster',?)) WHERE id=?`, url, task.MediaID)
		return os.Remove(q)
	}
	t.Cleanup(func() { posterObjectDeleteQuarantine = origDelete })
	_, cleaned, e := ReconcilePosterObjects(context.Background(), db, upload, 100, time.Hour)
	if e != nil || cleaned != 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, e)
	}
	if _, e = os.Stat(p); e != nil {
		t.Fatalf("replacement removed: %v", e)
	}
}
func TestPosterGCUncertainCommitPreservesQuarantine(t *testing.T) {
	db, upload, _ := seedCurrentLinkedPosterTask(t)
	p, _ := writePosterObject(t, upload, "uncertain", 2*time.Hour)
	orig := withImmediatePosterObjectTx
	withImmediatePosterObjectTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		out, e := store.WithImmediateConnTx(ctx, d, fn)
		if e != nil {
			return out, e
		}
		return store.ImmediateOutcome{CommitAttempted: true}, &store.ImmediateCommitError{Cause: context.DeadlineExceeded}
	}
	t.Cleanup(func() { withImmediatePosterObjectTx = orig })
	_, _, e := ReconcilePosterObjects(context.Background(), db, upload, 100, time.Hour)
	if e == nil {
		t.Fatal("uncertain accepted")
	}
	if _, e = os.Stat(p); !os.IsNotExist(e) {
		t.Fatalf("final restored: %v", e)
	}
	q := filepath.Join(upload, "posters", "objects", "sha256", "quarantine")
	entries, _ := os.ReadDir(q)
	if len(entries) != 1 {
		t.Fatalf("quarantine entries=%d", len(entries))
	}
}

func TestPosterGCSweepsOldQuarantineOnly(t *testing.T) {
	db, upload, _ := seedCurrentLinkedPosterTask(t)
	qdir := filepath.Join(upload, "posters", "objects", "sha256", "quarantine")
	_ = os.MkdirAll(qdir, 0700)
	old := filepath.Join(qdir, strings.Repeat("a", 64)+".old.quarantine")
	fresh := filepath.Join(qdir, strings.Repeat("b", 64)+".fresh.quarantine")
	_ = os.WriteFile(old, []byte("old"), 0600)
	_ = os.WriteFile(fresh, []byte("fresh"), 0600)
	when := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, when, when)
	_, cleaned, e := ReconcilePosterObjects(context.Background(), db, upload, 100, time.Hour)
	if e != nil || cleaned != 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, e)
	}
	if _, e = os.Stat(old); !os.IsNotExist(e) {
		t.Fatalf("old quarantine retained: %v", e)
	}
	if _, e = os.Stat(fresh); e != nil {
		t.Fatalf("fresh quarantine removed: %v", e)
	}
}

func TestPostCommitCleanupUsesImmediateQuarantineProtocol(t *testing.T) {
	db, upload, _ := seedCurrentLinkedPosterTask(t)
	p, url := writePosterObject(t, upload, "postcommit", 0)
	orig := posterObjectRename
	inside := false
	origTx := withImmediatePosterObjectTx
	withImmediatePosterObjectTx = func(ctx context.Context, d *sql.DB, fn func(store.ImmediateConnTx) error) (store.ImmediateOutcome, error) {
		return store.WithImmediateConnTx(ctx, d, func(tx store.ImmediateConnTx) error { inside = true; defer func() { inside = false }(); return fn(tx) })
	}
	posterObjectRename = func(a, b string) error {
		if !inside {
			t.Fatal("postcommit rename outside writer lock")
		}
		return os.Rename(a, b)
	}
	t.Cleanup(func() { posterObjectRename = orig; withImmediatePosterObjectTx = origTx })
	if e := cleanupPosterPaths(context.Background(), db, []string{url}, upload); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(p); !os.IsNotExist(e) {
		t.Fatalf("CAS final retained: %v", e)
	}
}
func TestPostCommitCleanupBarrierRetainsConcurrentReference(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	p, url := writePosterObject(t, upload, "postcommit-race", 0)
	orig := posterObjectBeforeImmediate
	posterObjectBeforeImmediate = func() {
		_, _ = db.Exec(`UPDATE media SET meta_json=json_object('scrape',json_object('poster',?)) WHERE id=?`, url, task.MediaID)
	}
	t.Cleanup(func() { posterObjectBeforeImmediate = orig })
	if e := cleanupPosterPaths(context.Background(), db, []string{url}, upload); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(p); e != nil {
		t.Fatalf("referenced CAS removed: %v", e)
	}
}
func TestPostCommitCleanupQuarantineDoesNotDeleteReplacement(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	p, url := writePosterObject(t, upload, "postcommit-replacement", 0)
	orig := posterObjectDeleteQuarantine
	posterObjectDeleteQuarantine = func(q string) error {
		if e := os.WriteFile(p, []byte("postcommit-replacement"), 0444); e != nil {
			t.Fatal(e)
		}
		_, _ = db.Exec(`UPDATE media SET meta_json=json_object('scrape',json_object('poster',?)) WHERE id=?`, url, task.MediaID)
		return os.Remove(q)
	}
	t.Cleanup(func() { posterObjectDeleteQuarantine = orig })
	if e := cleanupPosterPaths(context.Background(), db, []string{url}, upload); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(p); e != nil {
		t.Fatalf("replacement removed: %v", e)
	}
}
