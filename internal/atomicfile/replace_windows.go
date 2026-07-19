//go:build windows

package atomicfile

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

const movefileReplaceExisting = 0x00000001
const movefileWriteThrough = 0x00000008

func ReplaceFile(source, target string) error {
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	var callErr error
	for attempt := 0; attempt < 200; attempt++ {
		r, _, e := moveFileExW.Call(uintptr(unsafe.Pointer(sourcePtr)), uintptr(unsafe.Pointer(targetPtr)), movefileReplaceExisting|movefileWriteThrough)
		if r != 0 {
			return nil
		}
		callErr = e
		if errno, ok := e.(syscall.Errno); !ok || (errno != syscall.Errno(32) && errno != syscall.ERROR_ACCESS_DENIED) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("MoveFileExW: %w", callErr)
}
