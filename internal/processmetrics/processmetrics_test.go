package processmetrics

import (
	"context"
	"os/exec"
	"testing"
)

func TestFFmpegCommandCountsEachExecutionMethodOnce(t *testing.T) {
	tests := []struct {
		name string
		run  func(*FFmpegCommand) error
	}{
		{"start", func(c *FFmpegCommand) error {
			if err := c.Start(); err != nil {
				return err
			}
			return c.Wait()
		}},
		{"run", func(c *FFmpegCommand) error { return c.Run() }},
		{"output", func(c *FFmpegCommand) error { _, err := c.Output(); return err }},
		{"combined output", func(c *FFmpegCommand) error { _, err := c.CombinedOutput(); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := FFmpegLaunchCount()
			cmd := helperCommand(t)
			if err := tt.run(cmd); err != nil {
				t.Fatal(err)
			}
			if got := FFmpegLaunchCount() - before; got != 1 {
				t.Fatalf("launch delta=%d want 1", got)
			}
		})
	}
}

func TestFFmpegCommandDoesNotCountPreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := FFmpegLaunchCount()
	cmd := NewFFmpegCommandContext(ctx, "missing-ffmpeg")
	if err := cmd.Start(); err == nil {
		t.Fatal("Start unexpectedly succeeded")
	}
	if got := FFmpegLaunchCount() - before; got != 0 {
		t.Fatalf("launch delta=%d want 0", got)
	}
}

func TestFFmpegCommandRepeatedExecutionCountsOnce(t *testing.T) {
	before := FFmpegLaunchCount()
	cmd := helperCommand(t)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Run()
	if got := FFmpegLaunchCount() - before; got != 1 {
		t.Fatalf("launch delta=%d want 1", got)
	}
}

func helperCommand(t *testing.T) *FFmpegCommand {
	t.Helper()
	return NewFFmpegCommandContext(context.Background(), exec.Command("go", "env", "GOEXE").Path, "env", "GOEXE")
}
