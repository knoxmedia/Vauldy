package processmetrics

import (
	"context"
	"os/exec"
	"sync"
	"sync/atomic"
)

var ffmpegLaunches atomic.Uint64

func FFmpegLaunchCount() uint64 { return ffmpegLaunches.Load() }

type FFmpegCommand struct {
	*exec.Cmd
	ctx  context.Context
	once sync.Once
}

func NewFFmpegCommand(path string, args ...string) *FFmpegCommand {
	return &FFmpegCommand{Cmd: exec.Command(path, args...)}
}

func NewFFmpegCommandContext(ctx context.Context, path string, args ...string) *FFmpegCommand {
	return &FFmpegCommand{Cmd: exec.CommandContext(ctx, path, args...), ctx: ctx}
}

func (c *FFmpegCommand) record() {
	if c.ctx != nil && c.ctx.Err() != nil {
		return
	}
	c.once.Do(func() { ffmpegLaunches.Add(1) })
}

func (c *FFmpegCommand) Start() error                    { c.record(); return c.Cmd.Start() }
func (c *FFmpegCommand) Run() error                      { c.record(); return c.Cmd.Run() }
func (c *FFmpegCommand) Output() ([]byte, error)         { c.record(); return c.Cmd.Output() }
func (c *FFmpegCommand) CombinedOutput() ([]byte, error) { c.record(); return c.Cmd.CombinedOutput() }
