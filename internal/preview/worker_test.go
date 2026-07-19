package preview

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"knox-media/internal/keystore"
	"knox-media/internal/storage"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
CREATE TABLE library (
    id INTEGER PRIMARY KEY,
    path TEXT
);
CREATE TABLE media (
    id INTEGER PRIMARY KEY,
    library_id INTEGER,
    file_path TEXT,
    file_type TEXT,
    duration INTEGER
);
CREATE TABLE media_encrypted_assets (
    media_id INTEGER,
    plain_path TEXT,
    status TEXT
);
CREATE TABLE preview_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL UNIQUE,
    status TEXT DEFAULT 'waiting',
    interval_sec INTEGER DEFAULT 10,
    thumb_count INTEGER DEFAULT 0,
    thumb_width INTEGER DEFAULT 240,
    thumb_height INTEGER DEFAULT 135,
    sprite_path TEXT,
    vtt_path TEXT,
    error_message TEXT,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}

	return db
}

func TestFormatTS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   int
		want string
	}{
		{name: "negative clamped", in: -2, want: "00:00:00.000"},
		{name: "minutes seconds", in: 65, want: "00:01:05.000"},
		{name: "hours", in: 3661, want: "01:01:01.000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTS(tt.in); got != tt.want {
				t.Fatalf("formatTS(%d)=%q want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildVTT(t *testing.T) {
	t.Parallel()

	got := buildVTT(3, 10, 25)
	wantContains := []string{
		"WEBVTT",
		"00:00:00.000 --> 00:00:10.000",
		"sprite.jpg#xywh=0,0,240,135",
		"00:00:10.000 --> 00:00:20.000",
		"sprite.jpg#xywh=240,0,240,135",
		"00:00:20.000 --> 00:00:25.000",
		"sprite.jpg#xywh=480,0,240,135",
	}
	for _, s := range wantContains {
		if !strings.Contains(got, s) {
			t.Fatalf("vtt missing %q\n%s", s, got)
		}
	}
}

func TestTrimErr(t *testing.T) {
	t.Parallel()

	t.Run("prefer output", func(t *testing.T) {
		if got := trimErr(" ffmpeg failed ", errors.New("fallback")); got != "ffmpeg failed" {
			t.Fatalf("trimErr prefer output got %q", got)
		}
	})

	t.Run("fallback to error", func(t *testing.T) {
		if got := trimErr("", errors.New("exec failed")); got != "exec failed" {
			t.Fatalf("trimErr fallback got %q", got)
		}
	})

	t.Run("truncate", func(t *testing.T) {
		raw := strings.Repeat("a", 1600)
		got := trimErr(raw, nil)
		if len(got) != 1500 {
			t.Fatalf("trimErr length=%d want 1500", len(got))
		}
	})
}

func TestEnsureReadyReturnsExistingTask(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	_, err := db.Exec(`INSERT INTO media(id) VALUES (1)`)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}

	dir := t.TempDir()
	sprite := filepath.Join(dir, "sprite.jpg")
	vtt := filepath.Join(dir, "thumbs.vtt")
	if err := os.WriteFile(sprite, []byte("x"), 0o644); err != nil {
		t.Fatalf("write sprite: %v", err)
	}
	if err := os.WriteFile(vtt, []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO preview_task (media_id, status, interval_sec, thumb_count, thumb_width, thumb_height, sprite_path, vtt_path) VALUES (?, 'ready', 9, 12, 240, 135, ?, ?)`,
		1, sprite, vtt,
	)
	if err != nil {
		t.Fatalf("insert preview task: %v", err)
	}

	w := NewWorker(db, nil, nil, "ffmpeg", dir)
	info, err := w.Ensure(context.Background(), 1, "video.mp4", 120)
	if err != nil {
		t.Fatalf("Ensure error: %v", err)
	}
	if info.Status != "ready" || info.Interval != 9 || info.ThumbCount != 12 {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.SpritePath != sprite || info.VTTPath != vtt {
		t.Fatalf("paths mismatch: %+v", info)
	}
}

func TestEnsureCreatesWaitingTaskAndCalculatesBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mediaID      int64
		durationSec  int64
		wantInterval int
		wantCount    int
	}{
		{name: "default duration", mediaID: 101, durationSec: 0, wantInterval: 6, wantCount: 100},
		{name: "minimum interval", mediaID: 102, durationSec: 4, wantInterval: 5, wantCount: 1},
		{name: "count capped at 100", mediaID: 103, durationSec: 1_000_000, wantInterval: 10000, wantCount: 100},
	}

	db := newTestDB(t)
	w := NewWorker(db, nil, nil, "ffmpeg", t.TempDir())

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w.running[tt.mediaID] = true // skip background run() for deterministic unit tests

			info, err := w.Ensure(context.Background(), tt.mediaID, "input.mp4", tt.durationSec)
			if err != nil {
				t.Fatalf("Ensure error: %v", err)
			}

			if info.Status != "waiting" || info.Interval != tt.wantInterval || info.ThumbCount != tt.wantCount {
				t.Fatalf("unexpected info: %+v", info)
			}
			if info.Width != 240 || info.Height != 135 {
				t.Fatalf("unexpected size: %+v", info)
			}

			var status string
			var interval, count int
			err = db.QueryRow(
				`SELECT status, interval_sec, thumb_count FROM preview_task WHERE media_id = ?`,
				tt.mediaID,
			).Scan(&status, &interval, &count)
			if err != nil {
				t.Fatalf("query inserted task: %v", err)
			}
			if status != "waiting" || interval != tt.wantInterval || count != tt.wantCount {
				t.Fatalf("unexpected row status=%s interval=%d count=%d", status, interval, count)
			}
		})
	}
}

func writeFakeFFmpeg(t *testing.T, dir string, succeed bool) string {
	t.Helper()

	path := filepath.Join(dir, "fake-ffmpeg.bat")
	var script string
	if succeed {
		script = `@echo off
set "last="
:next
if "%~1"=="" goto done
set "last=%~1"
shift
goto next
:done
if "%last%"=="" exit /b 2
echo sprite> "%last%"
exit /b 0
`
	} else {
		script = `@echo off
echo ffmpeg failed for test 1>&2
exit /b 1
`
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

func TestRunSuccessUpdatesReadyAndWritesFiles(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	_, err := db.Exec(`INSERT INTO preview_task (media_id, status) VALUES (?, 'waiting')`, 201)
	if err != nil {
		t.Fatalf("insert preview task: %v", err)
	}

	previewDir := t.TempDir()
	w := NewWorker(db, nil, nil, "ffmpeg", previewDir)
	w.runFFmpeg = func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		return nil, os.WriteFile(post[len(post)-1], []byte("sprite"), 0o644)
	}

	err = w.run(context.Background(), 201, "input.mp4", 25, 10, 3)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}

	var status, spritePath, vttPath sql.NullString
	var interval, count, width, height sql.NullInt64
	err = db.QueryRow(
		`SELECT status, sprite_path, vtt_path, interval_sec, thumb_count, thumb_width, thumb_height FROM preview_task WHERE media_id = ?`,
		201,
	).Scan(&status, &spritePath, &vttPath, &interval, &count, &width, &height)
	if err != nil {
		t.Fatalf("query task: %v", err)
	}
	if status.String != "ready" {
		t.Fatalf("status=%q want ready", status.String)
	}
	if interval.Int64 != 10 || count.Int64 != 3 || width.Int64 != 240 || height.Int64 != 135 {
		t.Fatalf("unexpected dimensions/count interval=%d count=%d width=%d height=%d", interval.Int64, count.Int64, width.Int64, height.Int64)
	}
	if _, err := os.Stat(spritePath.String); err != nil {
		t.Fatalf("sprite file missing: %v", err)
	}
	rawVTT, err := os.ReadFile(vttPath.String)
	if err != nil {
		t.Fatalf("read vtt: %v", err)
	}
	if !strings.Contains(string(rawVTT), "00:00:20.000 --> 00:00:25.000") {
		t.Fatalf("unexpected vtt content: %s", string(rawVTT))
	}
}

func TestRunFailUpdatesFailedStatus(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	_, err := db.Exec(`INSERT INTO preview_task (media_id, status) VALUES (?, 'waiting')`, 202)
	if err != nil {
		t.Fatalf("insert preview task: %v", err)
	}

	previewDir := t.TempDir()
	w := NewWorker(db, nil, nil, "ffmpeg", previewDir)
	w.runFFmpeg = func(context.Context, *sql.DB, *keystore.Vault, string, int64, string, float64, float64, []string, []string, string) ([]byte, error) {
		return []byte("ffmpeg failed for test"), errors.New("ffmpeg failed")
	}

	err = w.run(context.Background(), 202, "input.mp4", 30, 10, 3)
	if err == nil {
		t.Fatal("expected run error, got nil")
	}

	var status, errMsg sql.NullString
	err = db.QueryRow(
		`SELECT status, error_message FROM preview_task WHERE media_id = ?`,
		202,
	).Scan(&status, &errMsg)
	if err != nil {
		t.Fatalf("query task: %v", err)
	}
	if status.String != "failed" {
		t.Fatalf("status=%q want failed", status.String)
	}
	if strings.TrimSpace(errMsg.String) == "" {
		t.Fatalf("error_message should not be empty")
	}
}

func writeCountingFFmpeg(t *testing.T, dir string, counterPath string) string {
	t.Helper()

	path := filepath.Join(dir, "counting-ffmpeg.bat")
	script := "@echo off\r\n" +
		"echo 1>> \"" + counterPath + "\"\r\n" +
		"ping -n 2 127.0.0.1 >nul\r\n" +
		"set \"last=\"\r\n" +
		":next\r\n" +
		"if \"%~1\"==\"\" goto done\r\n" +
		"set \"last=%~1\"\r\n" +
		"shift\r\n" +
		"goto next\r\n" +
		":done\r\n" +
		"if \"%last%\"==\"\" exit /b 2\r\n" +
		"echo sprite> \"%last%\"\r\n" +
		"exit /b 0\r\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write counting ffmpeg: %v", err)
	}
	return path
}

func TestStartOnceSameMediaIDRunsOnlyOneWorker(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	_, err := db.Exec(`INSERT INTO preview_task (media_id, status) VALUES (?, 'waiting')`, 203)
	if err != nil {
		t.Fatalf("insert preview task: %v", err)
	}

	w := NewWorker(db, nil, nil, "ffmpeg", t.TempDir())
	var counterMu sync.Mutex
	var calls int
	started := make(chan struct{}, 2)
	w.runFFmpeg = func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		counterMu.Lock()
		calls++
		counterMu.Unlock()
		started <- struct{}{}
		time.Sleep(100 * time.Millisecond)
		return nil, os.WriteFile(post[len(post)-1], []byte("sprite"), 0o644)
	}

	ctx := context.Background()
	w.startOnce(ctx, 203, "input.mp4", 25, 10, 3)
	w.startOnce(ctx, 203, "input.mp4", 25, 10, 3)
	<-started

	deadline := time.Now().Add(3 * time.Second)
	for {
		w.mu.Lock()
		running := w.running[203]
		w.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not finish within timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}

	counterMu.Lock()
	gotCalls := calls
	counterMu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("ffmpeg invocation count=%d want 1", gotCalls)
	}
}

func TestStartOnceDifferentMediaIDCanRunConcurrently(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	_, err := db.Exec(`INSERT INTO preview_task (media_id, status) VALUES (?, 'waiting'), (?, 'waiting')`, 204, 205)
	if err != nil {
		t.Fatalf("insert preview tasks: %v", err)
	}

	w := NewWorker(db, nil, nil, "ffmpeg", t.TempDir())
	var counterMu sync.Mutex
	var calls int
	started := make(chan struct{}, 2)
	w.runFFmpeg = func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		counterMu.Lock()
		calls++
		counterMu.Unlock()
		started <- struct{}{}
		time.Sleep(100 * time.Millisecond)
		return nil, os.WriteFile(post[len(post)-1], []byte("sprite"), 0o644)
	}

	ctx := context.Background()
	w.startOnce(ctx, 204, "input-a.mp4", 25, 10, 3)
	w.startOnce(ctx, 205, "input-b.mp4", 25, 10, 3)
	<-started
	<-started

	deadline := time.Now().Add(4 * time.Second)
	for {
		w.mu.Lock()
		runningA := w.running[204]
		runningB := w.running[205]
		w.mu.Unlock()
		if !runningA && !runningB {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("workers did not finish within timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}

	counterMu.Lock()
	gotCalls := calls
	counterMu.Unlock()
	if gotCalls != 2 {
		t.Fatalf("ffmpeg invocation count=%d want 2", gotCalls)
	}

	var statusA, statusB, spriteA, spriteB, vttA, vttB string
	if err := db.QueryRow(`SELECT status, sprite_path, vtt_path FROM preview_task WHERE media_id = ?`, 204).Scan(&statusA, &spriteA, &vttA); err != nil {
		t.Fatalf("query status 204: %v", err)
	}
	if err := db.QueryRow(`SELECT status, sprite_path, vtt_path FROM preview_task WHERE media_id = ?`, 205).Scan(&statusB, &spriteB, &vttB); err != nil {
		t.Fatalf("query status 205: %v", err)
	}
	if statusA != "ready" || statusB != "ready" {
		t.Fatalf("unexpected statuses media204=%s media205=%s", statusA, statusB)
	}
	if _, err := os.Stat(spriteA); err != nil {
		t.Fatalf("media204 sprite missing: %v", err)
	}
	if _, err := os.Stat(vttA); err != nil {
		t.Fatalf("media204 vtt missing: %v", err)
	}
	if _, err := os.Stat(spriteB); err != nil {
		t.Fatalf("media205 sprite missing: %v", err)
	}
	if _, err := os.Stat(vttB); err != nil {
		t.Fatalf("media205 vtt missing: %v", err)
	}
}

func TestEnsurePreservesFailedTask(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	if _, err := db.Exec(`INSERT INTO media (id) VALUES (301)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO preview_task (media_id, status, interval_sec, thumb_count, error_message)
		VALUES (301, 'failed', 10, 5, 'ffmpeg error')`); err != nil {
		t.Fatal(err)
	}

	w := &Worker{DB: db, PreviewDir: t.TempDir(), FFmpegPath: "ffmpeg"}
	info, err := w.Ensure(context.Background(), 301, "video.mp4", 600)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if info.Status != "failed" || info.Error != "ffmpeg error" {
		t.Fatalf("Ensure=%+v want failed preserved", info)
	}
	var status, errMsg string
	if err := db.QueryRow(`SELECT status, COALESCE(error_message,'') FROM preview_task WHERE media_id = 301`).Scan(&status, &errMsg); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errMsg != "ffmpeg error" {
		t.Fatalf("db status=%s err=%q", status, errMsg)
	}
}

func TestUpsertWaitingPreviewTaskPreservesFailed(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO preview_task (media_id, status, interval_sec, thumb_count, error_message)
		VALUES (401, 'failed', 10, 5, 'boom')`); err != nil {
		t.Fatal(err)
	}
	if err := UpsertWaitingPreviewTask(db, 401, 20, 8); err != nil {
		t.Fatal(err)
	}
	var status, errMsg string
	var interval, count int
	if err := db.QueryRow(`SELECT status, interval_sec, thumb_count, COALESCE(error_message,'') FROM preview_task WHERE media_id = 401`).
		Scan(&status, &interval, &count, &errMsg); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errMsg != "boom" {
		t.Fatalf("failed row mutated: status=%s err=%q", status, errMsg)
	}
	if interval != 20 || count != 8 {
		t.Fatalf("interval/count updated: %d %d", interval, count)
	}
}

func TestRunOneMissingTaskReturnsError(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec(`INSERT INTO media(id, library_id, file_path, file_type, duration) VALUES (501, 1, 'movie.mp4', 'video', 30)`); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffmpeg", t.TempDir())
	if err := w.RunOne(context.Background(), 501); err == nil || !strings.Contains(err.Error(), "preview task") {
		t.Fatalf("RunOne error=%v want explicit missing preview task error", err)
	}
}

func TestRunOneReadyWithExistingFilesIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	dir := t.TempDir()
	sprite, vtt := filepath.Join(dir, "sprite.jpg"), filepath.Join(dir, "thumbs.vtt")
	if err := os.WriteFile(sprite, []byte("sprite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vtt, []byte("WEBVTT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media VALUES (502, 1, 'movie.mp4', 'video', 30)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO preview_task(media_id,status,sprite_path,vtt_path) VALUES (502,'ready',?,?)`, sprite, vtt); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffmpeg", dir)
	w.runFFmpeg = func(context.Context, *sql.DB, *keystore.Vault, string, int64, string, float64, float64, []string, []string, string) ([]byte, error) {
		t.Fatal("ffmpeg must not run for intact ready task")
		return nil, nil
	}
	if err := w.RunOne(context.Background(), 502); err != nil {
		t.Fatalf("RunOne: %v", err)
	}
}

func TestRunOneFailedReturnsStoredErrorWithoutRerun(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec(`INSERT INTO media VALUES (503, 1, 'movie.mp4', 'video', 30)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO preview_task(media_id,status,error_message) VALUES (503,'failed','old failure')`); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffmpeg", t.TempDir())
	w.runFFmpeg = func(context.Context, *sql.DB, *keystore.Vault, string, int64, string, float64, float64, []string, []string, string) ([]byte, error) {
		t.Fatal("ffmpeg must not rerun failed task")
		return nil, nil
	}
	err := w.RunOne(context.Background(), 503)
	if err == nil || err.Error() != "old failure" {
		t.Fatalf("RunOne error=%v want stored error", err)
	}
}

func TestRunOneUsesPreferredPathAndContext(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	input := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(input, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,path) VALUES (1,?)`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media VALUES (504, 1, 'movie.mp4', 'video', 25)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO preview_task(media_id,status,interval_sec,thumb_count) VALUES (504,'waiting',10,3)`); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffmpeg", t.TempDir())
	ctx := context.WithValue(context.Background(), struct{}{}, "identity")
	w.runFFmpeg = func(got context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, gotPath string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		if got != ctx {
			t.Error("context identity not preserved")
		}
		if gotPath != input {
			t.Errorf("input=%q want preferred %q", gotPath, input)
		}
		if err := os.WriteFile(post[len(post)-1], []byte("sprite"), 0o644); err != nil {
			t.Fatal(err)
		}
		return nil, nil
	}
	if err := w.RunOne(ctx, 504); err != nil {
		t.Fatalf("RunOne: %v", err)
	}
}

func TestRunOnePreCancelledLeavesWaiting(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec(`INSERT INTO media VALUES (505, 1, 'movie.mp4', 'video', 25)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO preview_task(media_id,status,interval_sec,thumb_count) VALUES (505,'waiting',10,3)`); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "ffmpeg", t.TempDir())
	w.runFFmpeg = storage.RunFFmpeg
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.RunOne(ctx, 505); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOne error=%v want canceled", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM preview_task WHERE media_id=505`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" {
		t.Fatalf("status=%q want waiting", status)
	}
}

func TestWorker_RunOneContextCancellation(t *testing.T) {
	tests := []struct {
		name          string
		initialStatus string
	}{
		{name: "waiting", initialStatus: "waiting"},
		{name: "ready with missing assets", initialStatus: "ready"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestDB(t)
			root := t.TempDir()
			input := filepath.Join(root, "movie.mp4")
			if err := os.WriteFile(input, []byte("video"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO library(id,path) VALUES (1,?)`, root); err != nil {
				t.Fatal(err)
			}
			mediaID := int64(506 + i)
			if _, err := db.Exec(`INSERT INTO media VALUES (?, 1, 'movie.mp4', 'video', 25)`, mediaID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO preview_task(media_id,status,interval_sec,thumb_count,sprite_path,vtt_path) VALUES (?,?,10,3,'missing-sprite','missing-vtt')`, mediaID, tt.initialStatus); err != nil {
				t.Fatal(err)
			}

			previewDir := t.TempDir()
			w := NewWorker(db, nil, nil, "ffmpeg", previewDir)
			ctx, cancel := context.WithCancel(context.Background())
			runnerStarted := make(chan struct{})
			releaseRunner := make(chan struct{})
			w.runFFmpeg = func(got context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
				if got != ctx {
					t.Errorf("runner context identity changed")
				}
				if err := os.WriteFile(post[len(post)-1], []byte("partial sprite"), 0o644); err != nil {
					t.Errorf("write partial sprite: %v", err)
				}

				close(runnerStarted)
				<-got.Done()
				<-releaseRunner
				return nil, got.Err()
			}

			result := make(chan error, 1)
			go func() { result <- w.RunOne(ctx, mediaID) }()
			<-runnerStarted
			cancel()
			select {
			case err := <-result:
				t.Fatalf("RunOne returned before runner completed: %v", err)
			default:
			}
			close(releaseRunner)
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("RunOne error=%v want context.Canceled", err)
			}

			outDir := filepath.Join(previewDir, strconv.FormatInt(mediaID, 10))
			for _, path := range []string{filepath.Join(outDir, "sprite.jpg"), filepath.Join(outDir, "thumbs.vtt")} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("temporary output %q remains: %v", path, err)
				}
			}
			var status, errorMessage string
			if err := db.QueryRow(`SELECT status, COALESCE(error_message,'') FROM preview_task WHERE media_id=?`, mediaID).Scan(&status, &errorMessage); err != nil {
				t.Fatal(err)
			}
			if status != "waiting" {
				t.Fatalf("status=%q want waiting", status)
			}
			if !strings.Contains(strings.ToLower(errorMessage), "cancel") {
				t.Fatalf("error_message=%q want cancellation reason", errorMessage)
			}
		})
	}
}

func TestWorker_RunOneReturnsReadyPersistenceError(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	input := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(input, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library(id,path) VALUES (1,?)`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO media VALUES (508, 1, 'movie.mp4', 'video', 25)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO preview_task(media_id,status,interval_sec,thumb_count) VALUES (508,'waiting',10,3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TRIGGER block_preview_ready
		BEFORE UPDATE ON preview_task
		WHEN NEW.status='ready'
		BEGIN
			SELECT RAISE(ABORT, 'ready blocked');
		END`); err != nil {
		t.Fatal(err)
	}

	w := NewWorker(db, nil, nil, "ffmpeg", t.TempDir())
	w.runFFmpeg = func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		return nil, os.WriteFile(post[len(post)-1], []byte("sprite"), 0o644)
	}
	err := w.RunOne(context.Background(), 508)
	if err == nil || !strings.Contains(err.Error(), "ready blocked") {
		t.Fatalf("RunOne error=%v want ready persistence failure", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM preview_task WHERE media_id=508`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "ready" {
		t.Fatal("task must not report ready after persistence failure")
	}
}

func TestTaskParametersMatchPreviewScheduling(t *testing.T) {
	interval, count := TaskParameters(120)
	if interval != 5 || count != 24 || count <= 1 {
		t.Fatalf("parameters=(%d,%d)", interval, count)
	}
}

func TestEnsureWaitingTaskInitializesParametersWithoutResettingExisting(t *testing.T) {
	db := newTestDB(t)
	if err := EnsureWaitingTask(context.Background(), db, 601, 120); err != nil {
		t.Fatal(err)
	}
	var status string
	var interval, count int
	if err := db.QueryRow(`SELECT status,interval_sec,thumb_count FROM preview_task WHERE media_id=601`).Scan(&status, &interval, &count); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" || interval != 5 || count != 24 {
		t.Fatalf("new row=%s/%d/%d", status, interval, count)
	}
	if _, err := db.Exec(`UPDATE preview_task SET status='ready',interval_sec=9,thumb_count=7 WHERE media_id=601`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureWaitingTask(context.Background(), db, 601, 999); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,interval_sec,thumb_count FROM preview_task WHERE media_id=601`).Scan(&status, &interval, &count); err != nil {
		t.Fatal(err)
	}
	if status != "ready" || interval != 9 || count != 7 {
		t.Fatalf("existing row reset=%s/%d/%d", status, interval, count)
	}
}

func TestRunOneStaleCommitGuardPreservesPublishedPreview(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	input := filepath.Join(root, "movie.mp4")
	_ = os.WriteFile(input, []byte("video"), 0644)
	_, _ = db.Exec(`INSERT INTO library(id,path) VALUES(1,?)`, root)
	_, _ = db.Exec(`INSERT INTO media VALUES(901,1,'movie.mp4','video',25)`)
	outDir := filepath.Join(t.TempDir(), "901")
	_ = os.MkdirAll(outDir, 0755)
	sprite := filepath.Join(outDir, "sprite.jpg")
	vtt := filepath.Join(outDir, "thumbs.vtt")
	_ = os.WriteFile(sprite, []byte("old sprite"), 0644)
	_ = os.WriteFile(vtt, []byte("old vtt"), 0644)
	_, _ = db.Exec(`INSERT INTO preview_task(media_id,status,interval_sec,thumb_count,sprite_path,vtt_path) VALUES(901,'waiting',10,3,?,?)`, sprite, vtt)
	w := NewWorker(db, nil, nil, "ffmpeg", filepath.Dir(outDir))
	runnerDone := make(chan struct{})
	release := make(chan struct{})
	w.runFFmpeg = func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		if err := os.WriteFile(post[len(post)-1], []byte("new sprite"), 0644); err != nil {
			return nil, err
		}
		close(runnerDone)
		<-release
		return nil, nil
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
	go func() { done <- w.RunOne(ctx, 901) }()
	<-runnerDone
	stale = true
	close(release)
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "stale lease") {
		t.Fatalf("err=%v", err)
	}
	if got, _ := os.ReadFile(sprite); string(got) != "old sprite" {
		t.Fatalf("sprite=%q", got)
	}
	if got, _ := os.ReadFile(vtt); string(got) != "old vtt" {
		t.Fatalf("vtt=%q", got)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM preview_task WHERE media_id=901`).Scan(&status)
	if status == "ready" || status == "failed" {
		t.Fatalf("status=%s", status)
	}
}

func TestRunOneTxGuardTakeoverRollsBackPreviewPair(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	input := filepath.Join(root, "movie.mp4")
	_ = os.WriteFile(input, []byte("v"), 0644)
	_, _ = db.Exec(`INSERT INTO library(id,path) VALUES(1,?)`, root)
	_, _ = db.Exec(`INSERT INTO media VALUES(902,1,'movie.mp4','video',25)`)
	base := t.TempDir()
	dir := filepath.Join(base, "902")
	_ = os.MkdirAll(dir, 0755)
	sprite := filepath.Join(dir, "sprite.jpg")
	vtt := filepath.Join(dir, "thumbs.vtt")
	_ = os.WriteFile(sprite, []byte("old sprite"), 0644)
	_ = os.WriteFile(vtt, []byte("old vtt"), 0644)
	_, _ = db.Exec(`INSERT INTO preview_task(media_id,status,interval_sec,thumb_count,sprite_path,vtt_path) VALUES(902,'waiting',10,3,?,?)`, sprite, vtt)
	w := NewWorker(db, nil, nil, "ffmpeg", base)
	w.runFFmpeg = func(_ context.Context, _ *sql.DB, _ *keystore.Vault, _ string, _ int64, _ string, _ float64, _ float64, _ []string, post []string, _ string) ([]byte, error) {
		return nil, os.WriteFile(post[len(post)-1], []byte("new sprite"), 0644)
	}
	ctx := WithCommitGuardTx(context.Background(), func(context.Context, *sql.Tx) error { return errors.New("tx takeover") })
	err := w.RunOne(ctx, 902)
	if err == nil || !strings.Contains(err.Error(), "tx takeover") {
		t.Fatalf("err=%v", err)
	}
	if got, _ := os.ReadFile(sprite); string(got) != "old sprite" {
		t.Fatalf("sprite=%q", got)
	}
	if got, _ := os.ReadFile(vtt); string(got) != "old vtt" {
		t.Fatalf("vtt=%q", got)
	}
}
