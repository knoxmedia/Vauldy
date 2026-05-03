//go:build linux

package processctl

import "syscall"

func Suspend(pid int) error {
	if pid <= 0 {
		return syscall.EINVAL
	}
	return syscall.Kill(pid, syscall.SIGSTOP)
}

func Resume(pid int) error {
	if pid <= 0 {
		return syscall.EINVAL
	}
	return syscall.Kill(pid, syscall.SIGCONT)
}
