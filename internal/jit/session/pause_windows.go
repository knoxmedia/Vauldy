//go:build windows

package session

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ntdll               = syscall.NewLazyDLL("ntdll.dll")
	procNtResumeProcess = ntdll.NewProc("NtResumeProcess")
)

func platformResume(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_SUSPEND_RESUME, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	r0, _, _ := procNtResumeProcess.Call(uintptr(h))
	if r0 != 0 {
		ntStatus := int64(r0)
		return syscall.Errno(ntStatus & 0xFFFF)
	}
	return nil
}

var _ = unsafe.Pointer(nil)
