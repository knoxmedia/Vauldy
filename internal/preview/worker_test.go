package preview

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
CREATE TABLE media (
    id INTEGER PRIMARY KEY
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
	ffmpegPath := writeFakeFFmpeg(t, t.TempDir(), true)
	w := NewWorker(db, nil, nil, ffmpegPath, previewDir)

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
	ffmpegPath := writeFakeFFmpeg(t, t.TempDir(), false)
	w := NewWorker(db, nil, nil, ffmpegPath, previewDir)

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

	counterFile := filepath.Join(t.TempDir(), "ffmpeg-count.txt")
	ffmpegPath := writeCountingFFmpeg(t, t.TempDir(), counterFile)
	w := NewWorker(db, nil, nil, ffmpegPath, t.TempDir())

	ctx := context.Background()
	w.startOnce(ctx, 203, "input.mp4", 25, 10, 3)
	w.startOnce(ctx, 203, "input.mp4", 25, 10, 3)

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

	raw, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatalf("read counter file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("ffmpeg invocation count=%d want 1", len(lines))
	}
}

func TestStartOnceDifferentMediaIDCanRunConcurrently(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	_, err := db.Exec(`INSERT INTO preview_task (media_id, status) VALUES (?, 'waiting'), (?, 'waiting')`, 204, 205)
	if err != nil {
		t.Fatalf("insert preview tasks: %v", err)
	}

	counterFile := filepath.Join(t.TempDir(), "ffmpeg-count-all.txt")
	ffmpegPath := writeCountingFFmpeg(t, t.TempDir(), counterFile)
	w := NewWorker(db, nil, nil, ffmpegPath, t.TempDir())

	ctx := context.Background()
	w.startOnce(ctx, 204, "input-a.mp4", 25, 10, 3)
	w.startOnce(ctx, 205, "input-b.mp4", 25, 10, 3)

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

	raw, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatalf("read counter file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("ffmpeg invocation count=%d want 2", len(lines))
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
