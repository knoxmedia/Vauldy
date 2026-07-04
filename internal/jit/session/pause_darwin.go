//go:build darwin

package session

import (
	"syscall"
)

func platformResume(pid int) error {
	return syscall.Kill(pid, syscall.SIGCONT)
}
