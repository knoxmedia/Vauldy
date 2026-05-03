//go:build windows

package processctl

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/windows"
)

var (
	modNtDll             = windows.NewLazyDLL("ntdll.dll")
	procNtSuspendProcess = modNtDll.NewProc("NtSuspendProcess")
	procNtResumeProcess  = modNtDll.NewProc("NtResumeProcess")
)

func ntErr(r uintptr) error {
	// NTSTATUS: success is 0.
	if r == 0 {
		return nil
	}
	return fmt.Errorf("NTSTATUS 0x%x", uint32(r))
}

func Suspend(pid int) error {
	if pid <= 0 {
		return syscall.EINVAL
	}
	h, err := windows.OpenProcess(windows.PROCESS_SUSPEND_RESUME, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	r, _, _ := procNtSuspendProcess.Call(uintptr(h))
	return ntErr(r)
}

func Resume(pid int) error {
	if pid <= 0 {
		return syscall.EINVAL
	}
	h, err := windows.OpenProcess(windows.PROCESS_SUSPEND_RESUME, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	r, _, _ := procNtResumeProcess.Call(uintptr(h))
	return ntErr(r)
}
