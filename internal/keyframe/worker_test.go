package keyframe

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knox-media/internal/jit/keyframes"
	_ "modernc.org/sqlite"
)

func keyframeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE media(id INTEGER PRIMARY KEY, library_id INTEGER, file_id TEXT, file_path TEXT, duration INTEGER, file_type TEXT);
CREATE TABLE library(id INTEGER PRIMARY KEY, path TEXT);
CREATE TABLE keyframe_task(media_id INTEGER UNIQUE, status TEXT, output_dir TEXT, keyframe_count INTEGER DEFAULT 0, error_message TEXT, updated_at TEXT);
CREATE TABLE media_encrypted_assets(media_id INTEGER, plain_path TEXT, status TEXT);
CREATE TABLE media_derived_assets(media_id INTEGER, artifact_kind TEXT, logical_name TEXT, enc_path TEXT);
INSERT INTO library(id,path) VALUES(1,'');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func seedKeyframe(t *testing.T, db *sql.DB, status string) (int64, string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(src, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO media(id,library_id,file_id,file_path,duration,file_type) VALUES(51,1,'file-51',?,120,'video')`, src)
	if err == nil {
		_, err = db.Exec(`INSERT INTO keyframe_task(media_id,status) VALUES(51,?)`, status)
	}
	if err != nil {
		t.Fatal(err)
	}
	return 51, src
}

func TestRunOneQueriesTaskAndUsesPreferredPath(t *testing.T) {
	db := keyframeDB(t)
	id, src := seedKeyframe(t, db, "waiting")
	w := NewWorker(db, nil, nil, "ffprobe", t.TempDir())
	var gotCtx context.Context
	var gotPath string
	w.extract = func(ctx context.Context, mediaID int64, fileID, filePath string, duration float64) (*keyframes.Meta, error) {
		gotCtx = ctx
		gotPath = filePath
		return &keyframes.Meta{FileID: fileID, FilePath: filePath, PTS: []float64{0}}, nil
	}
	ctx := context.WithValue(context.Background(), struct{}{}, "same")
	if err := w.RunOne(ctx, id); err != nil {
		t.Fatal(err)
	}
	if gotCtx != ctx || gotPath != src {
		t.Fatalf("context/path not propagated: %v %q", gotCtx, gotPath)
	}
	var status string
	var count int
	if err := db.QueryRow(`SELECT status,keyframe_count FROM keyframe_task WHERE media_id=?`, id).Scan(&status, &count); err != nil || status != "done" || count != 1 {
		t.Fatalf("status=%q count=%d err=%v", status, count, err)
	}
}

func TestRunOneDoneWithArtifactIsIdempotent(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "done")
	out := t.TempDir()
	artifact := filepath.Join(out, "file-51.json")
	if err := os.WriteFile(artifact, []byte(`{"pts":[0]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE keyframe_task SET output_dir=?,keyframe_count=1 WHERE media_id=?`, out, id); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffprobe", out)
	w.extract = func(context.Context, int64, string, string, float64) (*keyframes.Meta, error) {
		t.Fatal("extract called")
		return nil, nil
	}
	if err := w.RunOne(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestRunOneFailedReturnsStoredError(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "failed")
	_, _ = db.Exec(`UPDATE keyframe_task SET error_message='old failure' WHERE media_id=?`, id)
	w := NewWorker(db, nil, nil, "ffprobe", t.TempDir())
	w.extract = func(context.Context, int64, string, string, float64) (*keyframes.Meta, error) {
		t.Fatal("extract called")
		return nil, nil
	}
	if err := w.RunOne(context.Background(), id); err == nil || err.Error() != "old failure" {
		t.Fatalf("failed task returned %v", err)
	}
}

func TestRunOneCancellationReturnsTaskToWaiting(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "waiting")
	w := NewWorker(db, nil, nil, "ffprobe", t.TempDir())
	w.extract = func(ctx context.Context, _ int64, _ string, _ string, _ float64) (*keyframes.Meta, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.RunOne(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM keyframe_task WHERE media_id=?`, id).Scan(&status); err != nil || status != "waiting" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestRunOneDoesNotSwallowFinalStatusError(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "waiting")
	w := NewWorker(db, nil, nil, "ffprobe", t.TempDir())
	w.extract = func(context.Context, int64, string, string, float64) (*keyframes.Meta, error) {
		_, _ = db.Exec(`DROP TABLE keyframe_task`)
		return &keyframes.Meta{FileID: "file-51", PTS: []float64{0}}, nil
	}
	if err := w.RunOne(context.Background(), id); err == nil || !strings.Contains(strings.ToLower(err.Error()), "keyframe_task") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunOneDoneWithEncryptedArtifactIsIdempotent(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "done")
	out := t.TempDir()
	enc := filepath.Join(out, "file-51.json.enc")
	if err := os.WriteFile(enc, []byte("encrypted"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE keyframe_task SET output_dir=?,keyframe_count=2 WHERE media_id=?`, out, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path) VALUES(?,'keyframe_meta','file-51.json',?)`, id, enc); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffprobe", out)
	calls := 0
	w.extract = func(context.Context, int64, string, string, float64) (*keyframes.Meta, error) {
		calls++
		return nil, errors.New("must not extract")
	}
	if err := w.RunOne(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("extract calls=%d", calls)
	}
}

func TestRunOneDoneWithMissingEncryptedArtifactRebuilds(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "done")
	out := t.TempDir()
	if _, err := db.Exec(`UPDATE keyframe_task SET output_dir=?,keyframe_count=2 WHERE media_id=?`, out, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path) VALUES(?,'keyframe_meta','file-51.json',?)`, id, filepath.Join(out, "missing.enc")); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffprobe", out)
	calls := 0
	w.extract = func(context.Context, int64, string, string, float64) (*keyframes.Meta, error) {
		calls++
		return &keyframes.Meta{FileID: "file-51", PTS: []float64{0}}, nil
	}
	if err := w.RunOne(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("extract calls=%d", calls)
	}
}

func TestRunOneDoneReturnsDerivedLookupDatabaseError(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "done")
	if _, err := db.Exec(`UPDATE keyframe_task SET keyframe_count=2 WHERE media_id=?; DROP TABLE media_derived_assets`, id); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffprobe", t.TempDir())
	calls := 0
	w.extract = func(context.Context, int64, string, string, float64) (*keyframes.Meta, error) {
		calls++
		return nil, nil
	}
	err := w.RunOne(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "media_derived_assets") {
		t.Fatalf("err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("extract calls=%d", calls)
	}
}

func TestEnqueueRetryContextReturnsDatabaseError(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "failed")
	if _, err := db.Exec(`CREATE TRIGGER block_keyframe_retry BEFORE UPDATE ON keyframe_task BEGIN SELECT RAISE(ABORT,'retry blocked'); END`); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffprobe", t.TempDir())
	err := w.EnqueueRetryContext(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "retry blocked") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunOneStaleCommitGuardPreservesPublishedKeyframe(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "waiting")
	out := t.TempDir()
	final := filepath.Join(out, "file-51.json")
	_ = os.WriteFile(final, []byte("old json"), 0644)
	w := NewWorker(db, nil, nil, "ffprobe", out)
	extractDone := make(chan struct{})
	release := make(chan struct{})
	w.extract = func(context.Context, int64, string, string, float64) (*keyframes.Meta, error) {
		close(extractDone)
		<-release
		return &keyframes.Meta{FileID: "file-51", PTS: []float64{0}}, nil
	}
	stale := false
	ctx := WithCommitGuard(context.Background(), func(context.Context) error {
		if stale {
			return errors.New("stale lease")
		}
		return nil
	})
	ctx = WithCommitGuardTx(ctx, func(context.Context, *sql.Tx) error {
		if stale {
			return errors.New("stale lease")
		}
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- w.RunOne(ctx, id) }()
	<-extractDone
	stale = true
	close(release)
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "stale lease") {
		t.Fatalf("err=%v", err)
	}
	if got, _ := os.ReadFile(final); string(got) != "old json" {
		t.Fatalf("json=%q", got)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM keyframe_task WHERE media_id=?`, id).Scan(&status)
	if status == "done" || status == "failed" {
		t.Fatalf("status=%s", status)
	}
}

func TestRunOneTxGuardTakeoverRollsBackKeyframe(t *testing.T) {
	db := keyframeDB(t)
	id, _ := seedKeyframe(t, db, "waiting")
	out := t.TempDir()
	final := filepath.Join(out, "file-51.json")
	_ = os.WriteFile(final, []byte("old json"), 0644)
	w := NewWorker(db, nil, nil, "ffprobe", out)
	w.extract = func(context.Context, int64, string, string, float64) (*keyframes.Meta, error) {
		return &keyframes.Meta{FileID: "file-51", PTS: []float64{0}}, nil
	}
	ctx := WithCommitGuardTx(context.Background(), func(context.Context, *sql.Tx) error { return errors.New("tx takeover") })
	err := w.RunOne(ctx, id)
	if err == nil || !strings.Contains(err.Error(), "tx takeover") {
		t.Fatalf("err=%v", err)
	}
	if got, _ := os.ReadFile(final); string(got) != "old json" {
		t.Fatalf("json=%q", got)
	}
}
