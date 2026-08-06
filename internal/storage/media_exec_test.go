package storage

import (
	"bytes"
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

func TestRunFFmpegWithLivenessReportsOnOutput(t *testing.T) {
	input := filepath.Join(t.TempDir(), "input.mp4")
	if err := os.WriteFile(input, []byte("input"), 0o644); err != nil {
		t.Fatal(err)
	}
	var executable string
	var post []string
	if runtime.GOOS == "windows" {
		executable = "cmd.exe"
		post = []string{"/c", "echo", "progress-token"}
	} else {
		executable = "/bin/sh"
		post = []string{"-c", "echo progress-token"}
	}
	var reports int
	out, err := RunFFmpegWithLiveness(context.Background(), nil, nil, executable, 0, input, 0, 0, nil, post, "", func() { reports++ })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if reports == 0 {
		t.Fatal("expected liveness reports from process output")
	}
	if !bytes.Contains(out, []byte("progress-token")) {
		t.Fatalf("output=%q missing progress-token", out)
	}
}

func TestLivenessWriterBuffersAndReports(t *testing.T) {
	var buf bytes.Buffer
	var reports int
	w := &livenessWriter{buf: &buf, report: func() { reports++ }}
	if _, err := w.Write([]byte("chunk")); err != nil {
		t.Fatal(err)
	}
	if reports != 1 {
		t.Fatalf("reports=%d want 1", reports)
	}
	if buf.String() != "chunk" {
		t.Fatalf("buffer=%q want chunk", buf.String())
	}
	// Empty writes must not report liveness.
	if _, err := w.Write(nil); err != nil {
		t.Fatal(err)
	}
	if reports != 1 {
		t.Fatalf("reports after empty write=%d want 1", reports)
	}
}
