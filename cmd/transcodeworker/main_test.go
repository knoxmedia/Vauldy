package transcodeworker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"

	"knox-media/internal/jit/hwenc"
	models "knox-media/internal/model"
)

func newTestTranscodeWorker(t *testing.T) (*TranscodeWorker, *redis.Client, string) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	base := t.TempDir()
	w := &TranscodeWorker{
		redis:     rdb,
		storage:   NewStorage(base),
		ffmpeg:    "ffmpeg",
		logger:    zap.NewNop(),
		workerID:  "tw-test",
		semaphore: make(chan struct{}, 1),
		hwEncoder: hwenc.Libx264,
	}
	return w, rdb, base
}

func TestBuildTranscodeArgs_SeekBoostUsesUltrafastPreset(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()
	if err := rdb.Set(ctx, "jit:session_seek_boost:seek-1", "1", 0).Err(); err != nil {
		t.Fatalf("set seek boost key: %v", err)
	}

	task := &models.TranscodeTask{
		FileID:    "f1",
		SegmentID: 3,
		Bitrate:   "2000k",
		SessionID: "seek-1",
	}

	args := w.buildTranscodeArgs("/in.mp4", "/out.ts", task, 12.0, 6.0, hwenc.Libx264)
	var sawPreset bool
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-preset" {
			sawPreset = true
			if args[i+1] != "ultrafast" {
				t.Fatalf("preset=%q, want ultrafast when seek boost active; args=%v", args[i+1], args)
			}
			break
		}
	}
	if !sawPreset {
		t.Fatalf("missing -preset in args: %v", args)
	}
}

func TestResolveSegmentSource_UsesSourceFileForVirtualSegments(t *testing.T) {
	w, rdb, base := newTestTranscodeWorker(t)
	ctx := context.Background()

	srcPath := filepath.Join(base, "media", "movie.mkv")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(srcPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	index := models.SegmentIndex{
		FileID: "f-virtual",
		VideoSegments: []models.VideoSegmentInfo{
			{ID: 0, Status: "indexed", StartTime: 6.5, Duration: 5.25, SlicePath: ""},
		},
	}
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if err := rdb.Set(ctx, "video:index:f-virtual", raw, 0).Err(); err != nil {
		t.Fatalf("set video index: %v", err)
	}
	if err := rdb.HSet(ctx, "video:meta:f-virtual", "file_path", srcPath).Err(); err != nil {
		t.Fatalf("set video meta: %v", err)
	}

	task := &models.TranscodeTask{
		FileID:    "f-virtual",
		SegmentID: 0,
	}
	inputPath, ss, dur, err := w.resolveSegmentSource(task)
	if err != nil {
		t.Fatalf("resolve segment source: %v", err)
	}
	if inputPath != srcPath {
		t.Fatalf("inputPath=%q, want %q", inputPath, srcPath)
	}
	if ss != 6.5 || dur != 5.25 {
		t.Fatalf("seek/duration=(%.2f, %.2f), want (6.50, 5.25)", ss, dur)
	}
}

func TestIsSessionTranscodePaused_TrueForPauseFlag(t *testing.T) {
	w, rdb, _ := newTestTranscodeWorker(t)
	ctx := context.Background()

	if err := rdb.HSet(ctx, "session:s-paused", "transcode_paused", "1").Err(); err != nil {
		t.Fatalf("set pause flag: %v", err)
	}
	if !w.isSessionTranscodePaused("s-paused") {
		t.Fatalf("expected paused=true")
	}
}
