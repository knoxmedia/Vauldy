//go:build windows

package postingest

import (
	"os"
	"syscall"
)

const posterFileAttributeReparsePoint = uint32(0x400)

func posterPathPlatformLinkedDefault(path string, _ os.FileInfo) bool {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attrs, err := syscall.GetFileAttributes(ptr)
	return err != nil || uint32(attrs)&posterFileAttributeReparsePoint != 0
}
