//go:build windows

package atomicfile

import (
	"errors"
	"io"
	"os"
	"syscall"
	"unsafe"
)

var createFileW = syscall.NewLazyDLL("kernel32.dll").NewProc("CreateFileW")

func ReadFile(path string) ([]byte, error) {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, _, callErr := createFileW.Call(uintptr(unsafe.Pointer(ptr)), syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, 0, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if h == uintptr(syscall.InvalidHandle) {
		return nil, callErr
	}
	f := os.NewFile(h, path)
	if f == nil {
		syscall.CloseHandle(syscall.Handle(h))
		return nil, errors.New("invalid Windows file handle")
	}
	defer f.Close()
	return io.ReadAll(f)
}
