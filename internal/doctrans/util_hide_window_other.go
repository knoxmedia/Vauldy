//go:build !windows

package doctrans

import "os/exec"

func setHideWindow(cmd *exec.Cmd) {}
