package handler

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
)

func posterHandlerTestDB(t *testing.T) (*sql.DB, int64) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "poster-handler.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lr, err := db.Exec(`INSERT INTO library(name,type,path,image_providers) VALUES('posters','movie',?,'embedded,screen_grabber')`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lid, _ := lr.LastInsertId()
	mr, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,title,file_type,status,meta_json) VALUES(?,'poster-media','movie.mp4','movie','video','active','{}')`, lid)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := mr.LastInsertId()
	return db, mid
}

func TestImageSourceEnabled(t *testing.T) {
	cfg := scraper.Config{ImageSources: []string{"tmdb", "screen_grabber"}}
	if !imageSourceEnabled(cfg, "screen_grabber") {
		t.Fatal("expected screen_grabber enabled")
	}
	if imageSourceEnabled(cfg, "embedded") {
		t.Fatal("embedded should be disabled")
	}
}

func TestApplyScrapeLocalImagesSkipsQueueWhenProviderHasPoster(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: postingest.NewQueue(db, "handler", nil)}
	res := &scraper.ScrapeResult{Poster: "https://provider/poster.jpg", Extra: map[string]any{}}
	h.applyScrapeLocalImages(context.Background(), mid, 1, "video", scraper.Config{ImageSources: []string{"embedded"}}, res, true)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND task_type='poster'`, mid).Scan(&n); err != nil || n != 0 {
		t.Fatalf("poster tasks=(%d,%v), want zero", n, err)
	}
}

func TestApplyScrapeLocalImagesOnlyEnqueuesWithoutSynchronousCapture(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	upload := filepath.Join(t.TempDir(), "uploads")
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Upload: upload}}}, Queue: postingest.NewQueue(db, "handler", nil)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	res := &scraper.ScrapeResult{Extra: map[string]any{}}
	h.applyScrapeLocalImages(ctx, mid, 1, "video", scraper.Config{ImageSources: []string{"screen_grabber"}}, res, true)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND task_type='poster'`, mid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("poster tasks=(%d,%v), want one", n, err)
	}
	if _, err := os.Stat(filepath.Join(upload, "posters", "1.jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("synchronous poster unexpectedly created: %v", err)
	}
}

type countingPosterRunner struct{ calls atomic.Int32 }

func (r *countingPosterRunner) Capture(context.Context, int64, int64, scraper.Config) (string, string, error) {
	r.calls.Add(1)
	return "/uploads/posters/result.jpg", "embedded", nil
}

func TestConcurrentScanAndScrapeFallbackQueueAndExecutePosterOnce(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	q := postingest.NewQueue(db, "handler", nil)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: q}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := q.Enqueue(context.Background(), mid, nil, postingest.TaskPoster)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		res := &scraper.ScrapeResult{Extra: map[string]any{}}
		h.applyScrapeLocalImages(ctx, mid, 1, "video", scraper.Config{ImageSources: []string{"embedded"}}, res, true)
		errs <- nil
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND task_type='poster'`, mid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("poster tasks=(%d,%v), want one", n, err)
	}
	task, err := q.Claim(context.Background(), postingest.TaskPoster)
	if err != nil || task == nil {
		t.Fatalf("claim=(%v,%v)", task, err)
	}
	runner := &countingPosterRunner{}
	adapter := postingest.NewPosterAdapter(db, t.TempDir(), nil, runner)
	if err := adapter.Execute(context.Background(), *task); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(context.Background(), *task); err != nil {
		t.Fatal(err)
	}
	if again, err := q.Claim(context.Background(), postingest.TaskPoster); err != nil || again != nil {
		t.Fatalf("second claim=(%v,%v)", again, err)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("runner calls=%d, want one", runner.calls.Load())
	}
}

func TestWaitPosterResultFindsMetaPlainAndDerivedAndHonorsCancellation(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB, int64, string)
		want  string
	}{
		{"meta scrape poster", func(t *testing.T, db *sql.DB, id int64, _ string) {
			_, e := db.Exec(`UPDATE media SET meta_json=? WHERE id=?`, `{"scrape":{"poster":"/remote.jpg"}}`, id)
			if e != nil {
				t.Fatal(e)
			}
		}, "/remote.jpg"},
		{"plain poster", func(t *testing.T, _ *sql.DB, id int64, upload string) {
			p := filepath.Join(upload, "posters")
			if e := os.MkdirAll(p, 0755); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(p, "1.jpg"), []byte("jpg"), 0644); e != nil {
				t.Fatal(e)
			}
		}, "/uploads/posters/1.jpg"},
		{"derived poster", func(t *testing.T, db *sql.DB, id int64, _ string) {
			p := filepath.Join(t.TempDir(), "poster.jpg.enc")
			if e := os.WriteFile(p, []byte("enc"), 0600); e != nil {
				t.Fatal(e)
			}
			_, e := db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'poster','poster.jpg',?,'aa','bb')`, id, p)
			if e != nil {
				t.Fatal(e)
			}
		}, "/api/v1/media/1/poster.jpg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, mid := posterHandlerTestDB(t)
			upload := filepath.Join(t.TempDir(), "upload")
			h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Upload: upload}}}}
			tc.setup(t, db, mid, upload)
			got, ok, err := h.waitPosterResult(context.Background(), mid, 100*time.Millisecond)
			if err != nil || !ok || got != tc.want {
				t.Fatalf("wait=(%q,%v,%v), want (%q,true,nil)", got, ok, err, tc.want)
			}
		})
	}
	db, mid := posterHandlerTestDB(t)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if got, ok, err := h.waitPosterResult(ctx, mid, time.Second); ok || got != "" || !errors.Is(err, context.Canceled) || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("cancel wait=(%q,%v,%v) elapsed=%v", got, ok, err, time.Since(started))
	}
}

func TestManualExternalIDWithoutProviderPosterEnqueuesFallback(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: postingest.NewQueue(db, "handler", nil)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	res := &scraper.ScrapeResult{Title: "provider result", Extra: map[string]any{}}
	h.applyManualMatchLocalImages(ctx, mid, 1, "video", scraper.Config{ImageSources: []string{"embedded"}}, res)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND task_type='poster'`, mid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("external-ID poster tasks=(%d,%v), want one", n, err)
	}
}

func TestManualExternalIDWithProviderPosterSkipsFallback(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: postingest.NewQueue(db, "handler", nil)}
	res := &scraper.ScrapeResult{Title: "provider result", Poster: "https://provider/poster.jpg", Extra: map[string]any{}}
	h.applyManualMatchLocalImages(context.Background(), mid, 1, "video", scraper.Config{ImageSources: []string{"embedded"}}, res)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND task_type='poster'`, mid).Scan(&n); err != nil || n != 0 {
		t.Fatalf("external-ID poster tasks=(%d,%v), want zero", n, err)
	}
}

func TestWaitPosterResultRejectsStaleDerivedRowWithoutPlainPoster(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	missing := filepath.Join(t.TempDir(), "missing-poster.jpg.enc")
	if _, err := db.Exec(`INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(?,'poster','poster.jpg',?,'aa','bb')`, mid, missing); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Upload: filepath.Join(t.TempDir(), "upload")}}}}
	started := time.Now()
	got, ok, err := h.waitPosterResult(context.Background(), mid, 60*time.Millisecond)
	if err != nil || ok || got != "" {
		t.Fatalf("stale derived wait=(%q,%v,%v), want empty false nil", got, ok, err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("stale derived returned before polling timeout: %v", elapsed)
	}
}

func TestWaitPosterResultTimeoutIsNormalAndDatabaseErrorsReturnImmediately(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	got, ok, err := h.waitPosterResult(context.Background(), mid, 20*time.Millisecond)
	if err != nil || ok || got != "" {
		t.Fatalf("timeout=(%q,%v,%v), want empty false nil", got, ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	got, ok, err = h.waitPosterResult(context.Background(), mid, 2*time.Second)
	if err == nil || ok || got != "" || time.Since(started) > 200*time.Millisecond {
		t.Fatalf("closed db=(%q,%v,%v), elapsed=%v", got, ok, err, time.Since(started))
	}
}

func TestWaitPosterResultMissingMediaReturnsNoRows(t *testing.T) {
	db, _ := posterHandlerTestDB(t)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	_, ok, err := h.waitPosterResult(context.Background(), 999999, time.Second)
	if ok || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing media=(%v,%v), want false ErrNoRows", ok, err)
	}
}

func TestApplyScrapeLocalImagesPropagatesEnqueueCancellation(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: postingest.NewQueue(db, "handler", nil)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := &scraper.ScrapeResult{Extra: map[string]any{}}
	err := h.applyScrapeLocalImages(ctx, mid, 1, "video", scraper.Config{ImageSources: []string{"embedded"}}, res, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("apply error=%v, want context canceled", err)
	}
	var n int
	if e := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND task_type='poster'`, mid).Scan(&n); e != nil || n != 0 {
		t.Fatalf("queue=(%d,%v), want zero", n, e)
	}
}

func TestWaitPosterResultBoundsInitialBlockedQueryByMaxWait(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	started := time.Now()
	got, ok, err := h.waitPosterResult(context.Background(), mid, 50*time.Millisecond)
	elapsed := time.Since(started)
	if err != nil || ok || got != "" {
		t.Fatalf("blocked initial query=(%q,%v,%v), want empty false nil", got, ok, err)
	}
	if elapsed < 35*time.Millisecond || elapsed > 300*time.Millisecond {
		t.Fatalf("blocked initial query elapsed=%v, want bounded near maxWait", elapsed)
	}
}

func TestWaitPosterResultZeroWaitDoesNotQuery(t *testing.T) {
	db, mid := posterHandlerTestDB(t)
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}}
	started := time.Now()
	got, ok, err := h.waitPosterResult(context.Background(), mid, 0)
	if err != nil || ok || got != "" || time.Since(started) > 50*time.Millisecond {
		t.Fatalf("zero wait=(%q,%v,%v), elapsed=%v", got, ok, err, time.Since(started))
	}
}
