//go:build windows

package discovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

var ErrHandleResolutionUnsupported = errors.New("safe open-handle path resolution is unsupported")

func PathKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return strings.ToLower(filepath.Clean(abs)), nil
}

func PathWithinRoot(path, root string) bool {
	pathKey, err := PathKey(path)
	if err != nil {
		return false
	}
	rootKey, err := PathKey(root)
	if err != nil {
		return false
	}
	if pathKey == rootKey {
		return true
	}
	if rootKey == filepath.VolumeName(rootKey)+string(filepath.Separator) {
		return strings.HasPrefix(pathKey, rootKey)
	}
	return strings.HasPrefix(pathKey, rootKey+string(filepath.Separator))
}

func LongestRoot(path string, roots []string) (string, bool, error) {
	best := ""
	for _, root := range roots {
		key, err := PathKey(root)
		if err != nil {
			return "", false, err
		}
		if PathWithinRoot(path, key) && len(key) > len(best) {
			best = key
		}
	}
	return best, best != "", nil
}

func OpenFile(path string) (*os.File, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// ResolveOpenFile returns the final target of the already-open hashing handle.
func ResolveOpenFile(file *os.File, _ string) (string, error) {
	return finalPathByHandle(windows.Handle(file.Fd()))
}

func finalPathByHandle(h windows.Handle) (string, error) {
	size := uint32(512)
	for {
		buf := make([]uint16, size)
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)) {
			raw := windows.UTF16ToString(buf[:n])
			resolved := strings.TrimPrefix(raw, `\\?\UNC\`)
			if strings.HasPrefix(raw, `\\?\UNC\`) {
				resolved = `\\` + resolved
			}
			resolved = strings.TrimPrefix(resolved, `\\?\`)
			return filepath.Clean(resolved), nil
		}
		if n == 0 || n > 32768 {
			return "", errors.New("resolved path exceeds Windows path limit")
		}
		size = n + 1
	}
}

func IsRetryablePathError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_DELETE_PENDING)
}
