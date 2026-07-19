package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"knox-media/internal/keystore"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
)

type fakePosterRunner struct {
	calls       int
	ctx         context.Context
	cfg         scraper.Config
	url, source string
	err         error
}

func (r *fakePosterRunner) Capture(ctx context.Context, mediaID, libraryID int64, cfg scraper.Config) (string, string, error) {
	r.calls++
	r.ctx = ctx
	r.cfg = cfg
	return r.url, r.source, r.err
}

func seedPosterTest(t *testing.T, meta, fileType string) (*sql.DB, string, int64, int64) {
	t.Helper()
	db, _ := openQueueTestDB(t)
	upload := t.TempDir()
	res, err := db.Exec(`INSERT INTO library(name,type,path,image_providers) VALUES('posters','video',?,'embedded,screen_grabber')`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_type,meta_json) VALUES(?,'poster-media',?,?)`, libraryID, fileType, meta)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	return db, upload, mediaID, libraryID
}

func executePoster(t *testing.T, db *sql.DB, upload string, mediaID int64, runner PosterRunner) error {
	t.Helper()
	return NewPosterAdapter(db, upload, nil, runner).Execute(context.Background(), Task{ID: 1, MediaID: mediaID, Type: TaskPoster})
}

func TestPosterAdapter_MetaExists(t *testing.T) {
	for _, meta := range []string{`{"scrape":{"poster":"/existing.jpg"}}`, `{"scrape":{"extra":{"poster":"/extra.jpg"}}}`} {
		db, upload, id, _ := seedPosterTest(t, meta, "video")
		r := &fakePosterRunner{}
		if err := executePoster(t, db, upload, id, r); err != nil {
			t.Fatal(err)
		}
		if r.calls != 0 {
			t.Fatalf("runner calls=%d", r.calls)
		}
	}
}
func TestPosterAdapter_PlainExists(t *testing.T) {
	db, upload, id, _ := seedPosterTest(t, "{}", "video")
	p := filepath.Join(upload, "posters", "1.jpg")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("jpeg"), 0644); err != nil {
		t.Fatal(err)
	}
	r := &fakePosterRunner{}
	if err := executePoster(t, db, upload, id, r); err != nil {
		t.Fatal(err)
	}
	if r.calls != 0 {
		t.Fatal("runner called")
	}
	var meta string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, id).Scan(&meta)
	if !strings.Contains(meta, storage.PlainPosterURL(id)) {
		t.Fatalf("meta=%s", meta)
	}
}
func TestPosterAdapter_DerivedExists(t *testing.T) {
	db, upload, id, _ := seedPosterTest(t, "{}", "video")
	enc := filepath.Join(t.TempDir(), "poster.enc")
	_ = os.WriteFile(enc, []byte("enc"), 0600)
	_, err := db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'poster','poster.jpg',?,'w','i')`, id, enc)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakePosterRunner{}
	if err := executePoster(t, db, upload, id, r); err != nil {
		t.Fatal(err)
	}
	if r.calls != 0 {
		t.Fatal("runner called")
	}
	var meta string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, id).Scan(&meta)
	if !strings.Contains(meta, storage.DerivedPosterAPIPath(id)) {
		t.Fatalf("meta=%s", meta)
	}
}
func TestPosterAdapter_InvalidDerivedRuns(t *testing.T) {
	db, upload, id, _ := seedPosterTest(t, "{}", "video")
	_, _ = db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'poster','poster.jpg',?,'w','i')`, id, filepath.Join(t.TempDir(), "missing"))
	r := &fakePosterRunner{url: "/new.jpg", source: "embedded"}
	if err := executePoster(t, db, upload, id, r); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 {
		t.Fatalf("calls=%d", r.calls)
	}
}
func TestPosterAdapter_GeneratesIdempotently(t *testing.T) {
	db, upload, id, _ := seedPosterTest(t, `{"keep":true}`, "video")
	r := &fakePosterRunner{url: "/generated.jpg", source: "screen_grabber"}
	a := NewPosterAdapter(db, upload, nil, r)
	task := Task{ID: 1, MediaID: id, Type: TaskPoster}
	if err := a.Execute(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := a.Execute(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 {
		t.Fatalf("calls=%d", r.calls)
	}
	var meta string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, id).Scan(&meta)
	for _, s := range []string{"/generated.jpg", "local_poster_source", "screen_grabber", "\"keep\":true"} {
		if !strings.Contains(meta, s) {
			t.Fatalf("meta missing %q: %s", s, meta)
		}
	}
	if len(r.cfg.ImageSources) != 2 {
		t.Fatalf("cfg=%+v", r.cfg)
	}
}
func TestPosterAdapter_ContextAndErrors(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		db, upload, id, _ := seedPosterTest(t, "{}", "video")
		ctx := context.WithValue(context.Background(), struct{}{}, "poster-context")
		r := &fakePosterRunner{err: context.Canceled}
		err := NewPosterAdapter(db, upload, nil, r).Execute(ctx, Task{ID: 1, MediaID: id, Type: TaskPoster})
		if !errors.Is(err, context.Canceled) || r.ctx != ctx {
			t.Fatalf("err=%v ctx=%v", err, r.ctx)
		}
	})
	t.Run("empty URL permanent", func(t *testing.T) {
		db, upload, id, _ := seedPosterTest(t, "{}", "video")
		err := executePoster(t, db, upload, id, &fakePosterRunner{})
		var ce ClassifiedError
		if !errors.As(err, &ce) || ce.Kind != FailurePermanent {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("nonvideo permanent", func(t *testing.T) {
		db, upload, id, _ := seedPosterTest(t, "{}", "audio")
		err := executePoster(t, db, upload, id, &fakePosterRunner{url: "x"})
		var ce ClassifiedError
		if !errors.As(err, &ce) || ce.Kind != FailurePermanent {
			t.Fatalf("err=%v", err)
		}
	})
}
func TestPosterAdapter_UpdateErrorReturned(t *testing.T) {
	db, upload, id, _ := seedPosterTest(t, "{}", "video")
	_, err := db.Exec(`CREATE TRIGGER fail_poster_update BEFORE UPDATE OF meta_json ON media BEGIN SELECT RAISE(FAIL,'poster update failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	err = executePoster(t, db, upload, id, &fakePosterRunner{url: "/generated.jpg", source: "embedded"})
	if err == nil || !strings.Contains(err.Error(), "poster update failed") {
		t.Fatalf("err=%v", err)
	}
}
func TestLocalPosterRunner_CancelCleansTemp(t *testing.T) {
	db, upload, id, libraryID := seedPosterTest(t, "{}", "video")
	video := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(video, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE library SET path=? WHERE id=?`, filepath.Dir(video), libraryID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE media SET file_path=?, duration=50 WHERE id=?`, filepath.Base(video), id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake", RunFFmpeg: func(got context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _, _ float64, _, post []string, _ string) ([]byte, error) {
		out := post[len(post)-1]
		if !strings.Contains(filepath.Base(out), ".tmp.jpg") {
			t.Fatalf("non-temp output %q", out)
		}
		if err := os.WriteFile(out, []byte("partial"), 0644); err != nil {
			t.Fatal(err)
		}
		cancel()
		return nil, context.Canceled
	}}
	_, _, err := runner.Capture(ctx, id, libraryID, scraper.Config{ImageSources: []string{"screen_grabber"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(upload, "posters", fmt.Sprintf("%d.*.tmp.jpg", id)))
	if len(matches) != 0 {
		t.Fatalf("left temp: %v", matches)
	}
	if _, err := os.Stat(filepath.Join(upload, "posters", fmt.Sprintf("%d.jpg", id))); !os.IsNotExist(err) {
		t.Fatalf("final exists: %v", err)
	}
}

func TestLocalPosterRunner_EmbeddedPrecedesFrame(t *testing.T) {
	db, upload, id, libraryID := seedPosterTest(t, "{}", "video")
	video := filepath.Join(t.TempDir(), "video.mp4")
	_ = os.WriteFile(video, []byte("video"), 0644)
	_, _ = db.Exec(`UPDATE media SET file_path=? WHERE id=?`, video, id)
	calls := 0
	runner := &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake", FFprobePath: "fake", ProbeOutput: func(context.Context, *sql.DB, *keystore.Vault, string, int64, string, []string) ([]byte, func(), error) {
		return []byte(`{"streams":[{"codec_type":"video","index":2,"disposition":{"attached_pic":1}}]}`), nil, nil
	}, RunFFmpeg: func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _, _ float64, _, post []string, _ string) ([]byte, error) {
		calls++
		return nil, os.WriteFile(post[len(post)-1], []byte("jpeg"), 0644)
	}}
	url, source, err := runner.Capture(context.Background(), id, libraryID, scraper.Config{ImageSources: []string{"embedded", "screen_grabber"}})
	if err != nil {
		t.Fatal(err)
	}
	if source != "embedded" || url != storage.PlainPosterURL(id) || calls != 1 {
		t.Fatalf("url=%q source=%q calls=%d", url, source, calls)
	}
}

func TestPosterAdapter_RejectsInvalidDependencies(t *testing.T) {
	db, upload, id, _ := seedPosterTest(t, "{}", "video")
	tests := []*PosterAdapter{nil, NewPosterAdapter(nil, upload, nil, &fakePosterRunner{}), NewPosterAdapter(db, upload, nil, nil)}
	for _, a := range tests {
		var err error
		if a == nil {
			err = a.Execute(context.Background(), Task{Type: TaskPoster, MediaID: id})
		} else {
			err = a.Execute(context.Background(), Task{Type: TaskPoster, MediaID: id})
		}
		var ce ClassifiedError
		if !errors.As(err, &ce) || ce.Kind != FailurePermanent {
			t.Fatalf("adapter=%#v err=%v", a, err)
		}
	}
	err := NewPosterAdapter(db, upload, nil, &fakePosterRunner{}).Execute(context.Background(), Task{Type: TaskPreview, MediaID: id})
	var ce ClassifiedError
	if !errors.As(err, &ce) || ce.Kind != FailurePermanent {
		t.Fatalf("wrong type err=%v", err)
	}
}

func jsonMeta(t *testing.T, db *sql.DB, id int64) map[string]any {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDerivedPosterExists_ReturnsQueryError(t *testing.T) {
	db, _, id, _ := seedPosterTest(t, "{}", "video")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := derivedPosterExists(context.Background(), db, id)
	if err == nil {
		t.Fatal("expected closed database query error")
	}
}

func TestPosterAdapter_MergeReloadsConcurrentMeta(t *testing.T) {
	db, upload, id, _ := seedPosterTest(t, `{"scrape":{"extra":{"seed":true}}}`, "video")
	started, release := make(chan struct{}), make(chan struct{})
	r := &blockingPosterRunner{started: started, release: release, url: "/generated.jpg", source: "screen_grabber"}
	done := make(chan error, 1)
	go func() {
		done <- NewPosterAdapter(db, upload, nil, r).Execute(context.Background(), Task{ID: 1, MediaID: id, Type: TaskPoster})
	}()
	<-started
	if _, err := db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, `{"concurrent":"kept","scrape":{"extra":{"seed":true,"local_poster_source":"existing"}}}`, id); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	meta := jsonMeta(t, db, id)
	if meta["concurrent"] != "kept" {
		t.Fatalf("concurrent key lost: %#v", meta)
	}
	scrape := meta["scrape"].(map[string]any)
	extra := scrape["extra"].(map[string]any)
	if extra["local_poster_source"] != "existing" {
		t.Fatalf("source overwritten: %#v", extra)
	}
	if scrape["poster"] != "/generated.jpg" || extra["poster"] != "/generated.jpg" {
		t.Fatalf("poster not merged: %#v", scrape)
	}
}

type blockingPosterRunner struct {
	started     chan struct{}
	release     chan struct{}
	url, source string
}

func (r *blockingPosterRunner) Capture(context.Context, int64, int64, scraper.Config) (string, string, error) {
	close(r.started)
	<-r.release
	return r.url, r.source, nil
}

func TestLocalPosterRunner_ReplacesZeroLengthPoster(t *testing.T) {
	db, upload, id, libraryID := seedPosterTest(t, "{}", "video")
	video := filepath.Join(t.TempDir(), "video.mp4")
	_ = os.WriteFile(video, []byte("video"), 0644)
	_, _ = db.Exec(`UPDATE media SET file_path=? WHERE id=?`, video, id)
	final := filepath.Join(upload, "posters", fmt.Sprintf("%d.jpg", id))
	if err := os.MkdirAll(filepath.Dir(final), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(final, nil, 0644); err != nil {
		t.Fatal(err)
	}
	r := &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake", RunFFmpeg: func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _, _ float64, _, post []string, _ string) ([]byte, error) {
		return nil, os.WriteFile(post[len(post)-1], []byte("new-jpeg"), 0644)
	}}
	url, source, err := r.Capture(context.Background(), id, libraryID, scraper.Config{ImageSources: []string{"screen_grabber"}})
	if err != nil {
		t.Fatal(err)
	}
	if url != storage.PlainPosterURL(id) || source != "screen_grabber" {
		t.Fatalf("url=%q source=%q", url, source)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-jpeg" {
		t.Fatalf("final=%q", got)
	}
	artifacts, _ := filepath.Glob(final + ".*")
	if len(artifacts) != 0 {
		t.Fatalf("backup/temp remain: %v", artifacts)
	}
}

func TestReplaceFilePreservingOld_RestoresBackup(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "new.tmp")
	final := filepath.Join(dir, "poster.jpg")
	_ = os.WriteFile(tmp, []byte("new"), 0644)
	_ = os.WriteFile(final, []byte("old"), 0644)
	backup, err := replaceFilePreservingOld(tmp, final)
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Fatal("expected backup")
	}
	if got, _ := os.ReadFile(final); string(got) != "new" {
		t.Fatalf("final=%q", got)
	}
	if got, _ := os.ReadFile(backup); string(got) != "old" {
		t.Fatalf("backup=%q", got)
	}
	if err := restoreReplacedFile(final, backup); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(final); string(got) != "old" {
		t.Fatalf("restored=%q", got)
	}
}

func TestLocalPosterRunner_RestoresExistingPosterWhenFinalizeFails(t *testing.T) {
	db, upload, id, libraryID := seedPosterTest(t, "{}", "video")
	video := filepath.Join(t.TempDir(), "video.mp4")
	_ = os.WriteFile(video, []byte("video"), 0644)
	_, _ = db.Exec(`UPDATE media SET file_path=? WHERE id=?`, video, id)
	final := filepath.Join(upload, "posters", fmt.Sprintf("%d.jpg", id))
	if err := os.MkdirAll(filepath.Dir(final), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(final, []byte("old-poster"), 0644); err != nil {
		t.Fatal(err)
	}
	want := errors.New("finalize failed")
	r := &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake", RunFFmpeg: func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _, _ float64, _, post []string, _ string) ([]byte, error) {
		return nil, os.WriteFile(post[len(post)-1], []byte("new-poster"), 0644)
	}, finalize: func(context.Context, *storage.DerivedAssetStore, *sql.DB, int64, string) (string, error) {
		return "", want
	}}
	_, _, err := r.Capture(context.Background(), id, libraryID, scraper.Config{ImageSources: []string{"screen_grabber"}})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	got, readErr := os.ReadFile(final)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old-poster" {
		t.Fatalf("old poster lost: %q", got)
	}
	artifacts, _ := filepath.Glob(final + ".*")
	if len(artifacts) != 0 {
		t.Fatalf("backup/temp remain: %v", artifacts)
	}
}

type concurrentPosterRunner struct {
	mu                    sync.Mutex
	calls, inflight, peak int
	started               chan struct{}
	release               chan struct{}
}

func (r *concurrentPosterRunner) Capture(context.Context, int64, int64, scraper.Config) (string, string, error) {
	r.mu.Lock()
	r.calls++
	r.inflight++
	if r.inflight > r.peak {
		r.peak = r.inflight
	}
	if r.started != nil && r.calls == 1 {
		close(r.started)
	}
	r.mu.Unlock()
	if r.release != nil {
		<-r.release
	}
	r.mu.Lock()
	r.inflight--
	r.mu.Unlock()
	return "/generated.jpg", "embedded", nil
}
func (r *concurrentPosterRunner) snapshot() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.peak
}

func TestPosterAdapter_ConcurrentSameMediaRunsOnce(t *testing.T) {
	db, upload, id, _ := seedPosterTest(t, "{}", "video")
	r := &concurrentPosterRunner{started: make(chan struct{}), release: make(chan struct{})}
	a := NewPosterAdapter(db, upload, nil, r)
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func() { errs <- a.Execute(context.Background(), Task{ID: 1, MediaID: id, Type: TaskPoster}) }()
	}
	<-r.started
	close(r.release)
	for i := 0; i < 10; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	calls, _ := r.snapshot()
	if calls != 1 {
		t.Fatalf("runner calls=%d", calls)
	}
}

func TestPosterAdapter_DifferentMediaRunInParallel(t *testing.T) {
	db, upload, id1, lib := seedPosterTest(t, "{}", "video")
	res, err := db.Exec(`INSERT INTO media(library_id,file_id,file_type,meta_json) VALUES(?,'poster-media-2','video','{}')`, lib)
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := res.LastInsertId()
	r := &concurrentPosterRunner{release: make(chan struct{})}
	a := NewPosterAdapter(db, upload, nil, r)
	errs := make(chan error, 2)
	go func() { errs <- a.Execute(context.Background(), Task{MediaID: id1, Type: TaskPoster}) }()
	go func() { errs <- a.Execute(context.Background(), Task{MediaID: id2, Type: TaskPoster}) }()
	deadline := time.After(2 * time.Second)
	for {
		_, peak := r.snapshot()
		if peak >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("peak=%d", peak)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(r.release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPosterAdapter_RejectsStaleLeaseAfterRunner(t *testing.T) {
	db, upload, id, _ := seedPosterTest(t, "{}", "video")
	res, err := db.Exec(`INSERT INTO post_ingest_task(media_id,task_type,status,lease_owner) VALUES(?,'poster','running','owner/old')`, id)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	r := &blockingPosterRunner{started: make(chan struct{}), release: make(chan struct{}), url: "/stale.jpg", source: "embedded"}
	done := make(chan error, 1)
	go func() {
		done <- NewPosterAdapter(db, upload, nil, r).Execute(context.Background(), Task{ID: taskID, MediaID: id, Type: TaskPoster, LeaseOwner: "owner/old"})
	}()
	<-r.started
	if _, err := db.Exec(`UPDATE post_ingest_task SET lease_owner='owner/new' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	close(r.release)
	err = <-done
	var ce ClassifiedError
	if !errors.As(err, &ce) || ce.Kind != FailureShutdown {
		t.Fatalf("err=%v", err)
	}
	var meta string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, id).Scan(&meta)
	if strings.Contains(meta, "stale.jpg") {
		t.Fatalf("stale meta=%s", meta)
	}
}

func TestLocalPosterRunner_CommitGuardPreventsReplace(t *testing.T) {
	db, upload, id, lib := seedPosterTest(t, "{}", "video")
	video := filepath.Join(t.TempDir(), "v.mp4")
	_ = os.WriteFile(video, []byte("v"), 0644)
	_, _ = db.Exec(`UPDATE media SET file_path=? WHERE id=?`, video, id)
	final := filepath.Join(upload, "posters", fmt.Sprintf("%d.jpg", id))
	_ = os.MkdirAll(filepath.Dir(final), 0755)
	_ = os.WriteFile(final, []byte("old"), 0644)
	ctx := context.WithValue(context.Background(), posterCommitGuardKey{}, func(context.Context) error { return ClassifiedError{Kind: FailureShutdown, Err: errors.New("stale")} })
	r := &LocalPosterRunner{DB: db, UploadDir: upload, FFmpegPath: "fake", RunFFmpeg: func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _, _ float64, _, post []string, _ string) ([]byte, error) {
		return nil, os.WriteFile(post[len(post)-1], []byte("new"), 0644)
	}}
	_, _, err := r.Capture(ctx, id, lib, scraper.Config{ImageSources: []string{"screen_grabber"}})
	var ce ClassifiedError
	if !errors.As(err, &ce) || ce.Kind != FailureShutdown {
		t.Fatalf("err=%v", err)
	}
	got, _ := os.ReadFile(final)
	if string(got) != "old" {
		t.Fatalf("final=%q", got)
	}
}
