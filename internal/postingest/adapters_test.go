package postingest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type recordingAdapter struct {
	ctx   context.Context
	task  Task
	err   error
	calls int
}

func (a *recordingAdapter) Execute(ctx context.Context, task Task) error {
	a.ctx, a.task = ctx, task
	a.calls++
	return a.err
}

func TestAdapterSetDispatchesEachTaskType(t *testing.T) {
	tests := []struct {
		typ           TaskType
		selectAdapter func(*AdapterSet) *recordingAdapter
	}{
		{TaskPoster, func(s *AdapterSet) *recordingAdapter { return s.Poster.(*recordingAdapter) }},
		{TaskPreview, func(s *AdapterSet) *recordingAdapter { return s.Preview.(*recordingAdapter) }},
		{TaskKeyframe, func(s *AdapterSet) *recordingAdapter { return s.Keyframe.(*recordingAdapter) }},
		{TaskSubtitle, func(s *AdapterSet) *recordingAdapter { return s.Subtitle.(*recordingAdapter) }},
		{TaskAtrack, func(s *AdapterSet) *recordingAdapter { return s.Atrack.(*recordingAdapter) }},
	}
	for _, tt := range tests {
		t.Run(string(tt.typ), func(t *testing.T) {
			set := AdapterSet{
				Poster: &recordingAdapter{}, Preview: &recordingAdapter{}, Keyframe: &recordingAdapter{},
				Subtitle: &recordingAdapter{}, Atrack: &recordingAdapter{},
			}
			task := Task{ID: 17, MediaID: 23, Type: tt.typ}
			ctx := context.WithValue(context.Background(), struct{}{}, "identity")
			if err := set.Execute(ctx, task); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			selected := tt.selectAdapter(&set)
			if selected.calls != 1 {
				t.Fatalf("selected calls=%d want 1", selected.calls)
			}
			if selected.ctx != ctx {
				t.Fatal("context identity was not preserved")
			}
			if selected.task != task {
				t.Fatalf("task=%+v want %+v", selected.task, task)
			}
		})
	}
}

func TestAdapterSetReturnsAdapterErrorUnchanged(t *testing.T) {
	want := errors.New("adapter failed")
	set := AdapterSet{Poster: &recordingAdapter{err: want}}
	if got := set.Execute(context.Background(), Task{Type: TaskPoster}); got != want {
		t.Fatalf("error=%v want identical %v", got, want)
	}
}

func TestAdapterSetPassesCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	adapter := &recordingAdapter{}
	if err := (AdapterSet{Preview: adapter}).Execute(ctx, Task{Type: TaskPreview}); err != nil {
		t.Fatal(err)
	}
	if adapter.ctx != ctx || !errors.Is(adapter.ctx.Err(), context.Canceled) {
		t.Fatal("cancelled context not propagated")
	}
}

func TestAdapterSetRejectsUnknownAndNilAdapter(t *testing.T) {
	tests := []struct {
		name string
		set  AdapterSet
		task Task
	}{
		{"unknown", AdapterSet{}, Task{Type: TaskType("bogus")}},
		{"nil", AdapterSet{}, Task{Type: TaskPoster}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.set.Execute(context.Background(), tt.task)
			var classified ClassifiedError
			if !errors.As(err, &classified) {
				t.Fatalf("error %T %v is not ClassifiedError", err, err)
			}
			if classified.Kind != FailurePermanent {
				t.Fatalf("kind=%v want permanent", classified.Kind)
			}
			if classified.Err == nil {
				t.Fatal("classified cause is nil")
			}
		})
	}
}

type recordingRunOne struct {
	ctx     context.Context
	mediaID int64
	calls   int
	err     error
}

func (w *recordingRunOne) RunOne(ctx context.Context, mediaID int64) error {
	w.ctx, w.mediaID = ctx, mediaID
	w.calls++
	return w.err
}

func adapterTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
		CREATE TABLE media(id INTEGER PRIMARY KEY, duration INTEGER);
		CREATE TABLE preview_task(media_id INTEGER UNIQUE, status TEXT DEFAULT 'waiting', interval_sec INTEGER DEFAULT 10, thumb_count INTEGER DEFAULT 0, thumb_width INTEGER DEFAULT 240, thumb_height INTEGER DEFAULT 135, updated_at TEXT, error_message TEXT);
		CREATE TABLE keyframe_task(media_id INTEGER UNIQUE, status TEXT DEFAULT 'waiting', updated_at TEXT, error_message TEXT, keyframe_count INTEGER DEFAULT 0);
		CREATE TABLE post_ingest_task(id INTEGER PRIMARY KEY, media_id INTEGER, task_type TEXT, status TEXT, lease_owner TEXT);
		INSERT INTO media(id,duration) VALUES(41,120);`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPreviewAdapter_EnsuresTaskAndPassesContext(t *testing.T) {
	db := adapterTestDB(t)
	worker := &recordingRunOne{}
	ctx := context.WithValue(context.Background(), struct{ name string }{"ctx"}, "same")
	err := NewPreviewAdapter(db, worker).Execute(ctx, Task{ID: 7, MediaID: 41, Type: TaskPreview})
	if err != nil {
		t.Fatal(err)
	}
	if worker.calls != 1 || worker.mediaID != 41 || worker.ctx == nil || worker.ctx.Value(struct{ name string }{"ctx"}) != "same" {
		t.Fatalf("worker=%+v", worker)
	}
	var status string
	var interval, count int
	if err := db.QueryRow(`SELECT status,interval_sec,thumb_count FROM preview_task WHERE media_id=41`).Scan(&status, &interval, &count); err != nil || status != "waiting" || interval != 5 || count != 24 {
		t.Fatalf("preview task=%q/%d/%d err=%v", status, interval, count, err)
	}
}

func TestKeyframeAdapter_EnsuresTaskAndPassesContext(t *testing.T) {
	db := adapterTestDB(t)
	worker := &recordingRunOne{}
	ctx := context.WithValue(context.Background(), struct{ name string }{"ctx"}, "same")
	err := NewKeyframeAdapter(db, worker).Execute(ctx, Task{ID: 8, MediaID: 41, Type: TaskKeyframe})
	if err != nil {
		t.Fatal(err)
	}
	if worker.calls != 1 || worker.mediaID != 41 || worker.ctx == nil || worker.ctx.Value(struct{ name string }{"ctx"}) != "same" {
		t.Fatalf("worker=%+v", worker)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM keyframe_task WHERE media_id=41`).Scan(&status); err != nil || status != "waiting" {
		t.Fatalf("keyframe task status=%q err=%v", status, err)
	}
}

func TestPreviewAndKeyframeAdapters_RejectInvalidTasksPermanently(t *testing.T) {
	db := adapterTestDB(t)
	cases := []struct {
		name    string
		adapter Adapter
		task    Task
	}{
		{"preview type", NewPreviewAdapter(db, &recordingRunOne{}), Task{ID: 1, MediaID: 41, Type: TaskKeyframe}},
		{"preview media", NewPreviewAdapter(db, &recordingRunOne{}), Task{ID: 1, MediaID: 0, Type: TaskPreview}},
		{"preview task", NewPreviewAdapter(db, &recordingRunOne{}), Task{ID: 0, MediaID: 41, Type: TaskPreview}},
		{"keyframe type", NewKeyframeAdapter(db, &recordingRunOne{}), Task{ID: 1, MediaID: 41, Type: TaskPreview}},
		{"keyframe media", NewKeyframeAdapter(db, &recordingRunOne{}), Task{ID: 1, MediaID: 0, Type: TaskKeyframe}},
		{"keyframe task", NewKeyframeAdapter(db, &recordingRunOne{}), Task{ID: 0, MediaID: 41, Type: TaskKeyframe}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.adapter.Execute(context.Background(), tc.task)
			var ce ClassifiedError
			if !errors.As(err, &ce) || ce.Kind != FailurePermanent {
				t.Fatalf("error=%T %v", err, err)
			}
		})
	}
}

func TestPreviewAndKeyframeAdapters_FenceLeaseBeforeAndAfterWorker(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  TaskType
		make func(*sql.DB, interface {
			RunOne(context.Context, int64) error
		}) Adapter
	}{
		{"preview", TaskPreview, func(db *sql.DB, w interface {
			RunOne(context.Context, int64) error
		}) Adapter {
			return NewPreviewAdapter(db, w)
		}},
		{"keyframe", TaskKeyframe, func(db *sql.DB, w interface {
			RunOne(context.Context, int64) error
		}) Adapter {
			return NewKeyframeAdapter(db, w)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := adapterTestDB(t)
			_, err := db.Exec(`INSERT INTO post_ingest_task(id,media_id,task_type,status,lease_owner) VALUES(9,41,?,'running','old')`, tc.typ)
			if err != nil {
				t.Fatal(err)
			}
			w := &recordingRunOne{}
			if err := tc.make(db, w).Execute(context.Background(), Task{ID: 9, MediaID: 41, Type: tc.typ, LeaseOwner: "wrong"}); err == nil || w.calls != 0 {
				t.Fatalf("pre-fence err=%v calls=%d", err, w.calls)
			}
			w.err = func() error { return nil }()
			// A worker wrapper changes the token while preserving the same synchronous contract.
			changing := runOneFunc(func(ctx context.Context, id int64) error {
				_, e := db.Exec(`UPDATE post_ingest_task SET lease_owner='new' WHERE id=9`)
				return e
			})
			if err := tc.make(db, changing.recording()).Execute(context.Background(), Task{ID: 9, MediaID: 41, Type: tc.typ, LeaseOwner: "old"}); err == nil {
				t.Fatal("post-fence accepted stale lease")
			}
		})
	}
}

type runOneFunc func(context.Context, int64) error

func (f runOneFunc) recording() *recordingRunOneFunc { return &recordingRunOneFunc{fn: f} }

type recordingRunOneFunc struct{ fn runOneFunc }

func (f *recordingRunOneFunc) RunOne(ctx context.Context, id int64) error { return f.fn(ctx, id) }

func TestKeyframeAdapter_PropagatesHistoricalFailedError(t *testing.T) {
	db := adapterTestDB(t)
	want := errors.New("old failure")
	worker := &recordingRunOne{err: want}
	err := NewKeyframeAdapter(db, worker).Execute(context.Background(), Task{ID: 1, MediaID: 41, Type: TaskKeyframe})
	if !errors.Is(err, want) || worker.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, worker.calls)
	}
}

// Task 11 RED tests.
type recordingSubtitleService struct {
	ensureCalls  int
	processCalls int
	ctx          context.Context
	mediaID      int64
	err          error
}

func (s *recordingSubtitleService) EnsurePendingSubtitleTask(id int64) error {
	s.ensureCalls++
	s.mediaID = id
	return s.err
}
func (s *recordingSubtitleService) ProcessMedia(ctx context.Context, id int64) error {
	s.processCalls++
	s.ctx = ctx
	s.mediaID = id
	return s.err
}

type recordingAtrackWorker struct {
	calls   int
	ctx     context.Context
	mediaID int64
	path    string
	err     error
}

func (w *recordingAtrackWorker) Run(ctx context.Context, id int64, path string) error {
	w.calls++
	w.ctx = ctx
	w.mediaID = id
	w.path = path
	return w.err
}

func task11AdapterDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
CREATE TABLE library(id INTEGER PRIMARY KEY, path TEXT);
CREATE TABLE media(id INTEGER PRIMARY KEY, library_id INTEGER, file_path TEXT, file_type TEXT);
CREATE TABLE subtitle_task(media_id INTEGER UNIQUE, status TEXT, message TEXT, created_at TEXT, started_at TEXT, finished_at TEXT, updated_at TEXT);
CREATE TABLE media_subtitle(id INTEGER PRIMARY KEY, media_id INTEGER, status TEXT, vtt_path TEXT);
CREATE TABLE atrack_task(media_id INTEGER UNIQUE, status TEXT, output_dir TEXT, error_message TEXT, updated_at TEXT);
CREATE TABLE media_encrypted_assets(media_id INTEGER, plain_path TEXT, status TEXT);
CREATE TABLE media_derived_assets(media_id INTEGER, artifact_kind TEXT, logical_name TEXT, enc_path TEXT);
CREATE TABLE post_ingest_task(id INTEGER PRIMARY KEY, media_id INTEGER, task_type TEXT, status TEXT, lease_owner TEXT, attempts INTEGER);
INSERT INTO library(id,path) VALUES(3,'');
INSERT INTO media(id,library_id,file_path,file_type) VALUES(41,3,'video.mp4','video');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSubtitleAdapter_EnsuresDomainTaskAndPassesContext(t *testing.T) {
	db := task11AdapterDB(t)
	svc := &recordingSubtitleService{}
	ctx := context.WithValue(context.Background(), struct{ name string }{"ctx"}, "same")
	if err := NewSubtitleAdapter(db, svc).Execute(ctx, Task{ID: 7, MediaID: 41, Type: TaskSubtitle}); err != nil {
		t.Fatal(err)
	}
	if svc.ensureCalls != 1 || svc.processCalls != 1 || svc.ctx.Value(struct{ name string }{"ctx"}) != "same" || svc.mediaID != 41 {
		t.Fatalf("service=%+v", svc)
	}
}

func TestSubtitleAdapter_ReadyRequiresActualOutput(t *testing.T) {
	db := task11AdapterDB(t)
	missing := t.TempDir() + "/missing.vtt"
	if _, err := db.Exec(`INSERT INTO media_subtitle(media_id,status,vtt_path) VALUES(41,'ready',?)`, missing); err != nil {
		t.Fatal(err)
	}
	svc := &recordingSubtitleService{}
	if err := NewSubtitleAdapter(db, svc).Execute(context.Background(), Task{ID: 7, MediaID: 41, Type: TaskSubtitle}); err != nil {
		t.Fatal(err)
	}
	if svc.processCalls != 1 {
		t.Fatalf("process calls=%d want 1", svc.processCalls)
	}
}

func TestSubtitleAdapter_ExistingUsableReadyOutputIsSuccess(t *testing.T) {
	db := task11AdapterDB(t)
	p := t.TempDir() + "/ready.vtt"
	if err := os.WriteFile(p, []byte("WEBVTT\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`INSERT INTO media_subtitle(media_id,status,vtt_path) VALUES(41,'ready',?)`, p)
	svc := &recordingSubtitleService{}
	if err := NewSubtitleAdapter(db, svc).Execute(context.Background(), Task{ID: 7, MediaID: 41, Type: TaskSubtitle}); err != nil {
		t.Fatal(err)
	}
	if svc.processCalls != 0 {
		t.Fatalf("process calls=%d", svc.processCalls)
	}
}

func TestAtrackAdapter_EnsuresTaskResolvesPathAndPassesContext(t *testing.T) {
	db := task11AdapterDB(t)
	root := t.TempDir()
	input := filepath.Join(root, "video.mp4")
	if err := os.WriteFile(input, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`UPDATE library SET path=? WHERE id=3`, root)
	w := &recordingAtrackWorker{}
	ctx := context.WithValue(context.Background(), struct{ name string }{"ctx"}, "same")
	if err := NewAtrackAdapter(db, w).Execute(ctx, Task{ID: 8, MediaID: 41, Type: TaskAtrack}); err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 || w.ctx.Value(struct{ name string }{"ctx"}) != "same" || w.mediaID != 41 || w.path != input {
		t.Fatalf("worker=%+v want path=%q", w, input)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM atrack_task WHERE media_id=41`).Scan(&status); err != nil || status != "waiting" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestAtrackAdapter_DoneRequiresActualOutput(t *testing.T) {
	db := task11AdapterDB(t)
	root := t.TempDir()
	input := filepath.Join(root, "video.mp4")
	_ = os.WriteFile(input, []byte("video"), 0644)
	_, _ = db.Exec(`UPDATE library SET path=? WHERE id=3`, root)
	_, _ = db.Exec(`INSERT INTO atrack_task(media_id,status,output_dir) VALUES(41,'done',?)`, filepath.Join(root, "missing"))
	w := &recordingAtrackWorker{}
	if err := NewAtrackAdapter(db, w).Execute(context.Background(), Task{ID: 8, MediaID: 41, Type: TaskAtrack}); err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 {
		t.Fatalf("calls=%d want 1", w.calls)
	}
}

func TestTask11Adapters_RejectStaleGenerationBeforeWorker(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  TaskType
		make func(*sql.DB) (Adapter, func() int)
	}{
		{"subtitle", TaskSubtitle, func(db *sql.DB) (Adapter, func() int) {
			s := &recordingSubtitleService{}
			return NewSubtitleAdapter(db, s), func() int { return s.processCalls }
		}},
		{"atrack", TaskAtrack, func(db *sql.DB) (Adapter, func() int) {
			w := &recordingAtrackWorker{}
			return NewAtrackAdapter(db, w), func() int { return w.calls }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := task11AdapterDB(t)
			_, _ = db.Exec(`INSERT INTO post_ingest_task(id,media_id,task_type,status,lease_owner,attempts) VALUES(9,41,?,'running','same',2)`, tc.typ)
			a, calls := tc.make(db)
			err := a.Execute(context.Background(), Task{ID: 9, MediaID: 41, Type: tc.typ, LeaseOwner: "same", Attempts: 1})
			if err == nil || calls() != 0 {
				t.Fatalf("err=%v calls=%d", err, calls())
			}
		})
	}
}

func TestAtrackAdapter_PreservesFailedDomainTask(t *testing.T) {
	db := task11AdapterDB(t)
	root := t.TempDir()
	input := filepath.Join(root, "video.mp4")
	_ = os.WriteFile(input, []byte("video"), 0644)
	_, _ = db.Exec(`UPDATE library SET path=? WHERE id=3`, root)
	_, _ = db.Exec(`INSERT INTO atrack_task(media_id,status,error_message) VALUES(41,'failed','saved failure')`)
	w := &recordingAtrackWorker{}
	err := NewAtrackAdapter(db, w).Execute(context.Background(), Task{ID: 8, MediaID: 41, Type: TaskAtrack})
	if err == nil || !strings.Contains(err.Error(), "saved failure") || w.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, w.calls)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM atrack_task WHERE media_id=41`).Scan(&status)
	if status != "failed" {
		t.Fatalf("status=%q", status)
	}
}

func TestAtrackAdapter_DoesNotResetRunningDomainTask(t *testing.T) {
	db := task11AdapterDB(t)
	root := t.TempDir()
	input := filepath.Join(root, "video.mp4")
	_ = os.WriteFile(input, []byte("video"), 0644)
	_, _ = db.Exec(`UPDATE library SET path=? WHERE id=3`, root)
	_, _ = db.Exec(`INSERT INTO atrack_task(media_id,status) VALUES(41,'running')`)
	w := &recordingAtrackWorker{}
	_ = NewAtrackAdapter(db, w).Execute(context.Background(), Task{ID: 8, MediaID: 41, Type: TaskAtrack})
	var status string
	_ = db.QueryRow(`SELECT status FROM atrack_task WHERE media_id=41`).Scan(&status)
	if status != "running" {
		t.Fatalf("status=%q", status)
	}
}

func TestAtrackAdapter_RootRandomFileIsNotUsableOutput(t *testing.T) {
	db := task11AdapterDB(t)
	root := t.TempDir()
	input := filepath.Join(root, "video.mp4")
	_ = os.WriteFile(input, []byte("video"), 0644)
	_, _ = db.Exec(`UPDATE library SET path=? WHERE id=3`, root)
	out := filepath.Join(root, "out")
	_ = os.MkdirAll(out, 0755)
	_ = os.WriteFile(filepath.Join(out, "random.txt"), []byte("x"), 0644)
	_, _ = db.Exec(`INSERT INTO atrack_task(media_id,status,output_dir) VALUES(41,'done',?)`, out)
	w := &recordingAtrackWorker{}
	if err := NewAtrackAdapter(db, w).Execute(context.Background(), Task{ID: 8, MediaID: 41, Type: TaskAtrack}); err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 {
		t.Fatalf("calls=%d", w.calls)
	}
}

func TestAtrackAdapter_ValidManifestAndSegmentIsUsable(t *testing.T) {
	db := task11AdapterDB(t)
	out := filepath.Join(t.TempDir(), "out", "0")
	_ = os.MkdirAll(out, 0755)
	_ = os.WriteFile(filepath.Join(out, "index.m3u8"), []byte("#EXTM3U\n#EXTINF:6,\nseg_000.ts\n"), 0644)
	_ = os.WriteFile(filepath.Join(out, "seg_000.ts"), []byte("segment"), 0644)
	_, _ = db.Exec(`INSERT INTO atrack_task(media_id,status,output_dir) VALUES(41,'done',?)`, filepath.Dir(out))
	w := &recordingAtrackWorker{}
	if err := NewAtrackAdapter(db, w).Execute(context.Background(), Task{ID: 8, MediaID: 41, Type: TaskAtrack}); err != nil {
		t.Fatal(err)
	}
	if w.calls != 0 {
		t.Fatalf("calls=%d", w.calls)
	}
}

func TestAtrackAdapter_EncryptedPlaylistAndSegmentAreUsable(t *testing.T) {
	db := task11AdapterDB(t)
	root := t.TempDir()
	playlist := filepath.Join(root, "playlist.enc")
	segment := filepath.Join(root, "segment.enc")
	_ = os.WriteFile(playlist, []byte("enc-playlist"), 0644)
	_ = os.WriteFile(segment, []byte("enc-segment"), 0644)
	_, _ = db.Exec(`INSERT INTO atrack_task(media_id,status,output_dir) VALUES(41,'done','missing'); INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path) VALUES(41,'atrack_playlist','0/index.m3u8',?),(41,'atrack_segment','0/seg_000.ts',?)`, playlist, segment)
	w := &recordingAtrackWorker{}
	if err := NewAtrackAdapter(db, w).Execute(context.Background(), Task{ID: 8, MediaID: 41, Type: TaskAtrack}); err != nil {
		t.Fatal(err)
	}
	if w.calls != 0 {
		t.Fatalf("calls=%d", w.calls)
	}
}
