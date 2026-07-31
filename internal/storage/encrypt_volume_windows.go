//go:build windows

package storage

import (
	"path/filepath"
	"strings"
)

// SameVolume reports whether a and b resolve to the same Windows volume/drive.
func SameVolume(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	va := filepath.VolumeName(absA)
	vb := filepath.VolumeName(absB)
	if va == "" || vb == "" {
		return false, nil
	}
	return strings.EqualFold(va, vb), nil
}
