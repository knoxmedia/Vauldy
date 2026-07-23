package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"knox-media/internal/imagethumb"
	"knox-media/internal/publication"
)

type testThumbnailWorker struct {
	base  string
	err   error
	calls int
}

func (w *testThumbnailWorker) Ensure(ctx context.Context, mediaID int64) (imagethumb.Paths, error) {
	w.calls++
	paths := imagethumb.ExpectedPaths(w.base, mediaID)
	if w.err != nil {
		return paths, w.err
	}
	if err := os.MkdirAll(filepath.Dir(paths.Thumb), 0o755); err != nil {
		return paths, err
	}
	for _, path := range []string{paths.Thumb, paths.Medium} {
		f, err := os.Create(path)
		if err != nil {
			return paths, err
		}
		err = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 4, 3)), nil)
		closeErr := f.Close()
		if err != nil {
			return paths, err
		}
		if closeErr != nil {
			return paths, closeErr
		}
	}
	return paths, nil
}

func planThumbnailFixture(t *testing.T, encrypted bool) (*sql.DB, int64, int64) {
	t.Helper()
	db, _ := openQueueTestDB(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "photo.jpg")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	if err = jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	enc := 0
	if encrypted {
		enc = 1
	}
	res, err := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_enabled) VALUES('photos','photo',?,?)`, dir, enc)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,meta_json,publication_state) VALUES(?,'photo-fixture',?,'image','{}','published')`, libraryID, source)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task(library_id,status,source) VALUES(?,'running','thumbnail-test')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanID, _ := res.LastInsertId()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := publication.NewPlanner(publication.PlanOptions{EncryptGlobal: encrypted}).PlanNewMediaTx(context.Background(), tx, publication.NewMedia{MediaID: mediaID, ScanTaskID: scanID, FileType: "image"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db, mediaID, run.ID
}

func waitPublicationState(t *testing.T, db *sql.DB, mediaID int64, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var got string
		if err := db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&got); err == nil && got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got string
	_ = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&got)
	t.Fatalf("publication_state=%q want %q", got, want)
}

func TestThumbnailDispatcherExecutesPlannedPhotoAndPublishes(t *testing.T) {
	db, mediaID, _ := planThumbnailFixture(t, false)
	worker := &testThumbnailWorker{base: t.TempDir()}
	q := NewQueue(db, "thumbnail-owner", nil)
	opts := DefaultDispatcherOptions()
	opts.OwnerID = "thumbnail-owner"
	opts.PollInterval = 5 * time.Millisecond
	opts.HeartbeatInterval = time.Second
	dispatcher, err := NewDispatcher(q, AdapterSet{Thumbnail: NewThumbnailAdapter(db, worker)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Start(ctx) }()
	waitPublicationState(t, db, mediaID, "published")
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if worker.calls != 1 {
		t.Fatalf("worker calls=%d", worker.calls)
	}
	var raw string
	if err = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err = json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatal(err)
	}
	photo, _ := meta["photo"].(map[string]any)
	if photo["thumb_path"] == "" || photo["medium_path"] == "" {
		t.Fatalf("photo metadata=%v", photo)
	}
}

func TestThumbnailCompletionMakesEncryptDependencyReadyThenEncryptPublishes(t *testing.T) {
	db, mediaID, runID := planThumbnailFixture(t, true)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || task == nil {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	worker := &testThumbnailWorker{base: t.TempDir()}
	if err = NewThumbnailAdapter(db, worker).Execute(context.Background(), *task); err != nil {
		t.Fatal(err)
	}
	if err = q.Complete(context.Background(), *task); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.QueryRow(`SELECT publication_state FROM media WHERE id=?`, mediaID).Scan(&state); err != nil || state != "processing" {
		t.Fatalf("after thumbnail state=%q err=%v", state, err)
	}
	var ready int
	if err = db.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM media_ingest_step_dependency d JOIN media_ingest_step dep ON dep.id=d.depends_on_step_id WHERE d.step_id=s.id AND d.dependency_kind='step_done' AND dep.status<>'done') FROM media_ingest_step s WHERE s.run_id=? AND s.step_type='encrypt'`, runID).Scan(&ready); err != nil || ready != 1 {
		t.Fatalf("encrypt ready=%d err=%v", ready, err)
	}
	encrypt, err := q.Claim(context.Background(), TaskEncrypt)
	if err != nil || encrypt == nil {
		t.Fatalf("encrypt claim=%+v err=%v", encrypt, err)
	}
	if err = q.Complete(context.Background(), *encrypt); err != nil {
		t.Fatal(err)
	}
	waitPublicationState(t, db, mediaID, "published")
}

func TestThumbnailAdapterStaleGenerationCannotMutate(t *testing.T) {
	db, mediaID, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	task, err := q.Claim(context.Background(), TaskThumbnail)
	if err != nil || task == nil {
		t.Fatalf("claim=%+v err=%v", task, err)
	}
	if _, err = db.Exec(`UPDATE media SET ingest_generation=ingest_generation+1 WHERE id=?`, mediaID); err != nil {
		t.Fatal(err)
	}
	worker := &testThumbnailWorker{base: t.TempDir()}
	err = NewThumbnailAdapter(db, worker).Execute(context.Background(), *task)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "stale") {
		t.Fatalf("err=%v", err)
	}
	if worker.calls != 0 {
		t.Fatalf("stale worker calls=%d", worker.calls)
	}
	var raw string
	_ = db.QueryRow(`SELECT meta_json FROM media WHERE id=?`, mediaID).Scan(&raw)
	if raw != "{}" {
		t.Fatalf("stale metadata=%s", raw)
	}
}

func TestThumbnailInvalidImageFailsWithBoundedQueueRetry(t *testing.T) {
	db, _, _ := planThumbnailFixture(t, false)
	q := NewQueue(db, "thumbnail-owner", nil)
	worker := &testThumbnailWorker{base: t.TempDir(), err: errors.New("ffmpeg thumb: invalid image data")}
	for attempt := 1; attempt <= 3; attempt++ {
		task, err := q.Claim(context.Background(), TaskThumbnail)
		if err != nil || task == nil {
			t.Fatalf("claim %d=%+v err=%v", attempt, task, err)
		}
		err = NewThumbnailAdapter(db, worker).Execute(context.Background(), *task)
		if err == nil {
			t.Fatal("expected invalid image error")
		}
		if err = q.Fail(context.Background(), task, failureKind(err), err); err != nil {
			t.Fatal(err)
		}
		_, _ = db.Exec(`UPDATE post_ingest_task SET available_at=CURRENT_TIMESTAMP WHERE id=?`, task.ID)
	}
	var status string
	var attempts int
	if err := db.QueryRow(`SELECT status,attempts FROM post_ingest_task WHERE task_type='thumbnail'`).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 3 || worker.calls != 3 {
		t.Fatalf("status=%s attempts=%d calls=%d", status, attempts, worker.calls)
	}
}
