//go:build !windows

package storage

import (
	"os"
	"path/filepath"
	"syscall"
)

// SameVolume reports whether a and b share the same Unix device ID.
func SameVolume(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	dev := func(path string) (uint64, error) {
		candidates := []string{path, filepath.Dir(path)}
		var lastErr error
		for _, candidate := range candidates {
			info, err := os.Stat(candidate)
			if err != nil {
				lastErr = err
				continue
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return 0, nil
			}
			return uint64(stat.Dev), nil
		}
		return 0, lastErr
	}
	da, err := dev(absA)
	if err != nil {
		return false, err
	}
	db, err := dev(absB)
	if err != nil {
		return false, err
	}
	return da == db, nil
}
