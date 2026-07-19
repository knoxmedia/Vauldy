package handler

import (
	"context"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/metadatalib"
	"knox-media/internal/store"
)

func TestResolvePosterFilePath(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "uploads")
	meta := filepath.Join(root, "metadata")
	if err := os.MkdirAll(filepath.Join(upload, "posters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(metadatalib.MediaDir(meta, 42), 0o755); err != nil {
		t.Fatal(err)
	}
	localPoster := filepath.Join(upload, "posters", "42.jpg")
	if err := writeTestJPEG(localPoster, color.RGBA{255, 0, 0, 255}); err != nil {
		t.Fatal(err)
	}
	metaPoster := filepath.Join(metadatalib.MediaDir(meta, 42), "poster.jpg")
	if err := writeTestJPEG(metaPoster, color.RGBA{0, 255, 0, 255}); err != nil {
		t.Fatal(err)
	}

	h := &Handler{App: &app.App{Config: &config.Config{Data: config.DataConfig{
		Upload:          upload,
		MetadataLibrary: meta,
	}}}}

	if got := h.resolvePosterFilePath(42, "/uploads/posters/42.jpg"); got != localPoster {
		t.Fatalf("upload poster: got %q want %q", got, localPoster)
	}
	if got := h.resolvePosterFilePath(42, metadatalib.PublicURL(42, "poster.jpg")); got != metaPoster {
		t.Fatalf("metadata poster: got %q want %q", got, metaPoster)
	}
	if got := h.resolvePosterFilePath(42, ""); got != localPoster {
		t.Fatalf("fallback poster: got %q want %q", got, localPoster)
	}
}

func TestComposeLibraryPreviewImage(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "uploads")
	if err := os.MkdirAll(filepath.Join(upload, "posters"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		id int64
		c  color.RGBA
	}{
		{11, color.RGBA{255, 0, 0, 255}},
		{12, color.RGBA{0, 255, 0, 255}},
		{13, color.RGBA{0, 0, 255, 255}},
		{14, color.RGBA{255, 255, 0, 255}},
	} {
		if err := writeTestJPEG(filepath.Join(upload, "posters", fmt.Sprintf("%d.jpg", spec.id)), spec.c); err != nil {
			t.Fatal(err)
		}
	}

	h := &Handler{App: &app.App{Config: &config.Config{Data: config.DataConfig{Upload: upload}}}}
	sources := []libraryPreviewSource{
		{mediaID: 11, posterURL: "/uploads/posters/11.jpg"},
		{mediaID: 12, posterURL: "/uploads/posters/12.jpg"},
		{mediaID: 13, posterURL: "/uploads/posters/13.jpg"},
		{mediaID: 14, posterURL: "/uploads/posters/14.jpg"},
	}
	out := filepath.Join(root, "preview.jpg")
	if err := composeLibraryPreviewImage(h, sources, out); err != nil {
		t.Fatalf("compose: %v", err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() == 0 {
		t.Fatalf("output missing or empty: %v", err)
	}
}

func TestLatestLibraryPreviewSources(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "uploads")
	dbPath := filepath.Join(root, "preview.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (name, type, path) VALUES ('Movies', 'movie', '/movies')`); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"d", "c", "b", "a", "z"} {
		poster := filepath.Join(upload, "posters", title+".jpg")
		if err := writeTestJPEG(poster, color.RGBA{byte(i), 32, 64, 255}); err != nil {
			t.Fatal(err)
		}
		_, err := db.Exec(
			`INSERT INTO media (library_id, file_id, title, file_path, file_type, status, created_at, meta_json)
			 VALUES (1, ?, ?, ?, 'video', 'active', datetime('2026-07-18 12:00:00', ?), ?)`,
			"f"+title, title, "/v/"+title, fmt.Sprintf("-%d seconds", i), `{"scrape":{"poster":"/uploads/posters/`+title+`.jpg"}}`,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _ = db.Exec(`INSERT INTO media (library_id, file_id, title, file_path, file_type, created_at)
	 VALUES (1, 'audio1', 'song', '/a.mp3', 'audio', datetime('now'))`)

	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Upload: upload}}}}
	got, err := h.latestLibraryPreviewSources(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("len=%d want 4", len(got))
	}
	if got[0].posterURL != "/uploads/posters/d.jpg" || got[3].posterURL != "/uploads/posters/a.jpg" {
		t.Fatalf("unexpected order: %+v", got)
	}
}

func TestLatestLibraryPreviewSourcesPhoto(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "preview", "photos")
	dbPath := filepath.Join(root, "preview.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (2, 'Photos', 'photo', '/photos')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{21, 22, 23} {
		thumb := filepath.Join(upload, fmt.Sprintf("%d", id), "thumb.jpg")
		if err := writeTestJPEG(thumb, color.RGBA{byte(id), 64, 128, 255}); err != nil {
			t.Fatal(err)
		}
		_, err := db.Exec(
			`INSERT INTO media (id, library_id, file_id, title, file_path, file_type, status, created_at)
			 VALUES (?, 2, ?, 'p', ?, 'image', 'active', datetime('now'))`,
			id, fmt.Sprintf("f-%d", id), fmt.Sprintf("/photos/%d.jpg", id),
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Preview: filepath.Join(root, "preview")}}}}
	got, err := h.latestLibraryPreviewSources(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d want 3", len(got))
	}
	if got[0].kind != libraryPreviewKindPhotoThumb || got[0].mediaID != 23 {
		t.Fatalf("unexpected first source: %+v", got[0])
	}
}

func TestLatestLibraryPreviewSourcesMusic(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "preview.sqlite")
	db, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO library (id, name, type, path) VALUES (3, 'Music', 'music', '/music')`); err != nil {
		t.Fatal(err)
	}
	art := filepath.Join(root, "art1.jpg")
	if err := writeTestJPEG(art, color.RGBA{255, 128, 0, 255}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO music_album (id, library_id, title, title_norm, artwork_path) VALUES (1, 3, 'A', 'a', ?)`, art); err != nil {
		t.Fatal(err)
	}

	h := &Handler{App: &app.App{DB: db, Config: &config.Config{Data: config.DataConfig{Preview: root}}}}
	got, err := h.latestLibraryPreviewSources(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d want 1", len(got))
	}
	if got[0].kind != libraryPreviewKindMusicArtwork || got[0].albumID != 1 {
		t.Fatalf("unexpected source: %+v", got[0])
	}
}

func writeTestJPEG(path string, c color.Color) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rect(0, 0, 120, 180))
	if c == nil {
		c = color.RGBA{128, 128, 128, 255}
	}
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 80})
}

func newTestLibraryPreviewHandler(t *testing.T, maxPending int, refresh func(context.Context, int64) error) (*Handler, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	background := &BackgroundGroup{}
	h := &Handler{App: &app.App{DB: &sql.DB{}, Config: &config.Config{}}, Background: background, ServerContext: ctx, libraryPreviewRefresh: refresh}
	h.libraryPreviewScheduler = newLibraryPreviewScheduler(ctx, background, libraryPreviewMaxConcurrent, maxPending, h.runLibraryPreviewRefresh)
	t.Cleanup(func() {
		cancel()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := background.Wait(waitCtx); err != nil {
			t.Fatalf("preview scheduler shutdown: %v", err)
		}
	})
	return h, cancel
}

func TestLibraryPreviewSchedulerRerunsOnceWhenDirty(t *testing.T) {
	libraryID := time.Now().UnixNano()
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int64
	h, _ := newTestLibraryPreviewHandler(t, 16, func(context.Context, int64) error {
		if executions.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	})
	if !h.scheduleLibraryPreviewRefresh(libraryID) {
		t.Fatal("first enqueue rejected")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("preview refresh did not start")
	}
	for i := 0; i < 20; i++ {
		if !h.scheduleLibraryPreviewRefresh(libraryID) {
			t.Fatal("dirty enqueue rejected")
		}
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for executions.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := executions.Load(); got != 2 {
		t.Fatalf("executions=%d want exactly 2", got)
	}
}

func TestLibraryPreviewSchedulerBoundsPendingAndAllowsRetry(t *testing.T) {
	const maxPending = 2
	ctx, cancel := context.WithCancel(context.Background())
	background := &BackgroundGroup{}
	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan int64, 4)
	var startedOnce sync.Once
	s := newLibraryPreviewScheduler(ctx, background, 1, maxPending, func(ctx context.Context, id int64) error {
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
			completed <- id
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	t.Cleanup(func() {
		cancel()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := background.Wait(waitCtx); err != nil {
			t.Fatalf("preview scheduler shutdown: %v", err)
		}
	})
	if !s.enqueueDirty(1) {
		t.Fatal("running enqueue rejected")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if !s.enqueueDirty(2) || !s.enqueueDirty(3) {
		t.Fatal("pending capacity rejected early")
	}
	if s.enqueueDirty(4) {
		t.Fatal("overflow enqueue accepted")
	}
	if got := s.pendingCount(); got != maxPending {
		t.Fatalf("pending=%d want %d", got, maxPending)
	}
	close(release)
	for i := 0; i < 3; i++ {
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Fatal("accepted task did not finish")
		}
	}
	deadline := time.Now().Add(time.Second)
	for !s.enqueueDirty(4) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	select {
	case id := <-completed:
		if id != 4 {
			t.Fatalf("retry completed id=%d", id)
		}
	case <-time.After(time.Second):
		t.Fatal("rejected task was not accepted on retry")
	}
}
func TestLibraryPreviewSchedulerCancellationReachesRunner(t *testing.T) {
	started := make(chan struct{})
	exited := make(chan struct{})
	h, cancel := newTestLibraryPreviewHandler(t, 4, func(ctx context.Context, _ int64) error {
		close(started)
		<-ctx.Done()
		close(exited)
		return nil
	})
	if !h.scheduleLibraryPreviewRefresh(1) {
		t.Fatal("enqueue rejected")
	}
	<-started
	cancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("runner did not receive cancellation")
	}
}

func TestLibraryPreviewSchedulerDirtyRerunsStayBoundedWhenPendingFull(t *testing.T) {
	const workers, maxPending = 4, 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background := &BackgroundGroup{}
	started := make(chan int64, workers)
	release := make(chan struct{})
	completed := make(chan int64, 16)
	s := newLibraryPreviewScheduler(ctx, background, workers, maxPending, func(_ context.Context, id int64) error {
		select {
		case started <- id:
		default:
		}
		<-release
		completed <- id
		return nil
	})
	for id := int64(1); id <= workers; id++ {
		if !s.enqueueDirty(id) {
			t.Fatalf("running enqueue %d rejected", id)
		}
		<-started
	}
	if !s.enqueueDirty(5) || !s.enqueueDirty(6) {
		t.Fatal("pending fill rejected")
	}
	for id := int64(1); id <= workers; id++ {
		if !s.enqueueDirty(id) {
			t.Fatalf("dirty enqueue %d rejected", id)
		}
	}
	if got := s.pendingCount(); got > maxPending {
		t.Fatalf("pending=%d exceeds %d before finish", got, maxPending)
	}
	close(release)
	seen := map[int64]int{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 6 || seen[1] < 2 || seen[2] < 2 || seen[3] < 2 || seen[4] < 2 {
		select {
		case id := <-completed:
			seen[id]++
			if s.pendingCount() > maxPending {
				t.Fatalf("pending exceeds cap: %d", s.pendingCount())
			}
		case <-deadline:
			t.Fatalf("dirty reruns not promoted: %v", seen)
		}
	}
	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := background.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryPreviewSchedulerMissingPollingDoesNotDirtyRunningJob(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int64
	h, _ := newTestLibraryPreviewHandler(t, 16, func(context.Context, int64) error {
		if runs.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	})
	if !h.scheduleLibraryPreviewIfMissing(77) {
		t.Fatal("initial missing enqueue rejected")
	}
	<-started
	for i := 0; i < 20; i++ {
		if !h.scheduleLibraryPreviewIfMissing(77) {
			t.Fatal("polling ensure rejected")
		}
	}
	close(release)
	time.Sleep(30 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("polling caused %d runs want 1", got)
	}
}

func TestLibraryPreviewSchedulerMissingFailureCooldownAndDirtyBypass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background := &BackgroundGroup{}
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	var runs atomic.Int64
	results := make(chan error, 4)
	s := newLibraryPreviewSchedulerWithOptions(ctx, background, 1, 8, libraryPreviewSchedulerOptions{Now: func() time.Time { return now }, InitialRetry: time.Minute, MaxRetry: 30 * time.Minute, MaxFailures: 8}, func(context.Context, int64) error { runs.Add(1); return <-results })
	results <- ErrLibraryPreviewUnavailable
	if !s.enqueueIfMissing(1) {
		t.Fatal("first missing rejected")
	}
	deadline := time.Now().Add(time.Second)
	for runs.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	for i := 0; i < 10; i++ {
		if !s.enqueueIfMissing(1) {
			t.Fatal("cooldown ensure should be accepted")
		}
	}
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatalf("cooldown runs=%d", runs.Load())
	}
	now = now.Add(time.Minute)
	results <- ErrLibraryPreviewUnavailable
	if !s.enqueueIfMissing(1) {
		t.Fatal("retry after cooldown rejected")
	}
	deadline = time.Now().Add(time.Second)
	for runs.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	results <- nil
	if !s.enqueueDirty(1) {
		t.Fatal("dirty bypass rejected")
	}
	deadline = time.Now().Add(time.Second)
	for runs.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runs.Load() != 3 {
		t.Fatalf("dirty bypass runs=%d", runs.Load())
	}
	if !s.enqueueIfMissing(1) {
		t.Fatal("success-cleared missing rejected")
	}
	results <- nil
	deadline = time.Now().Add(time.Second)
	for runs.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runs.Load() != 4 {
		t.Fatalf("success did not clear cooldown runs=%d", runs.Load())
	}
	cancel()
	waitCtx, wc := context.WithTimeout(context.Background(), time.Second)
	defer wc()
	if err := background.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryPreviewSchedulerCancellationDoesNotSetCooldown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	background := &BackgroundGroup{}
	started := make(chan struct{})
	s := newLibraryPreviewScheduler(ctx, background, 1, 4, func(ctx context.Context, _ int64) error { close(started); <-ctx.Done(); return ctx.Err() })
	if !s.enqueueIfMissing(9) {
		t.Fatal("enqueue rejected")
	}
	<-started
	cancel()
	wc, wcancel := context.WithTimeout(context.Background(), time.Second)
	defer wcancel()
	if err := background.Wait(wc); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, found := s.failures[9]
	s.mu.Unlock()
	if found {
		t.Fatal("cancellation created cooldown")
	}
}
func TestLibraryPreviewSchedulerFailureStateIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	background := &BackgroundGroup{}
	done := make(chan struct{}, 10)
	s := newLibraryPreviewSchedulerWithOptions(ctx, background, 1, 10, libraryPreviewSchedulerOptions{InitialRetry: time.Hour, MaxRetry: time.Hour, MaxFailures: 3}, func(context.Context, int64) error { done <- struct{}{}; return ErrLibraryPreviewUnavailable })
	for id := int64(1); id <= 10; id++ {
		for !s.enqueueIfMissing(id) {
			time.Sleep(time.Millisecond)
		}
	}
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("run timeout")
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		n := len(s.states)
		failures := len(s.failures)
		s.mu.Unlock()
		if n == 0 {
			if failures > 3 {
				t.Fatalf("failure states=%d want <=3", failures)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not settle")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	wc, wcancel := context.WithTimeout(context.Background(), time.Second)
	defer wcancel()
	if err := background.Wait(wc); err != nil {
		t.Fatal(err)
	}
}
