package ffprobe

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func init() {
	if os.Getenv("GO_WANT_FFPROBE_PROBE_HELPER") == "1" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}
func TestOutputContextCancelsProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FFPROBE_HELPER") == "1" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	t.Setenv("GO_WANT_FFPROBE_HELPER", "1")
	started := time.Now()
	_, err := OutputContext(ctx, os.Args[0], []string{"-test.run=TestOutputContextCancelsProcess"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("cancel took %v", time.Since(started))
	}
}
func TestProbeOptionsContextCancelsProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	t.Setenv("GO_WANT_FFPROBE_PROBE_HELPER", "1")
	started := time.Now()
	_, err := ProbeOptionsContext(ctx, os.Args[0], nil, "ignored", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("cancel took %v", time.Since(started))
	}
}
