//go:build !windows

package discovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var ErrHandleResolutionUnsupported = errors.New("safe open-handle path resolution is unsupported")

func PathKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
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
	if rootKey == string(filepath.Separator) {
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
	return os.Open(path)
}

// ResolveOpenFile resolves the object represented by the open descriptor. On
// platforms without procfs this fails closed rather than trusting a pathname.
func ResolveOpenFile(file *os.File, _ string) (string, error) {
	resolved, err := os.Readlink("/proc/self/fd/" + fmt.Sprint(file.Fd()))
	if err != nil {
		// Do not retain ENOENT in the unwrap chain: for an already-open descriptor
		// it means this platform cannot prove the final path, not that the source vanished.
		return "", fmt.Errorf("%w: /proc/self/fd unavailable: %v", ErrHandleResolutionUnsupported, err)
	}
	return filepath.Abs(strings.TrimSuffix(resolved, " (deleted)"))
}

func IsRetryablePathError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.ETXTBSY) || errors.Is(err, syscall.ENOENT)
}
