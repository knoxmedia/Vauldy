package storage

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunFFmpegCountsActualLaunchAttempt(t *testing.T) {
	input := filepath.Join(t.TempDir(), "input.mp4")
	if err := os.WriteFile(input, []byte("input"), 0o644); err != nil {
		t.Fatal(err)
	}
	executable := "cmd.exe"
	post := []string{"/c", "exit", "0"}
	if runtime.GOOS != "windows" {
		executable = "/bin/true"
		post = nil
	}
	before := FFmpegLaunchCount()
	_, _ = RunFFmpeg(context.Background(), nil, nil, executable, 0, input, 0, 0, nil, post, "")
	if got := FFmpegLaunchCount() - before; got != 1 {
		t.Fatalf("launch delta=%d want 1", got)
	}
}

func TestRunFFmpegPreCancelledDoesNotCountLaunch(t *testing.T) {
	input := filepath.Join(t.TempDir(), "input.mp4")
	if err := os.WriteFile(input, []byte("input"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := FFmpegLaunchCount()
	_, _ = RunFFmpeg(ctx, nil, nil, "missing-ffmpeg", 0, input, 0, 0, nil, nil, "")
	if got := FFmpegLaunchCount() - before; got != 0 {
		t.Fatalf("launch delta=%d want 0", got)
	}
}
