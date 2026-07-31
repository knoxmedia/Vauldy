package storage

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestQuarantineRootForSourceUsesPreferredWhenSameVolume(t *testing.T) {
	orig := sameVolumeCompare
	t.Cleanup(func() { sameVolumeCompare = orig })
	sameVolumeCompare = func(a, b string) (bool, error) { return true, nil }

	source := filepath.Join("E:", "media", "photo.jpg")
	preferred := filepath.Join("E:", "data", ".encryption_private")
	got := QuarantineRootForSource(source, preferred)
	if got != preferred {
		t.Fatalf("got %q want preferred %q", got, preferred)
	}
}

func TestQuarantineRootForSourceFallsBackWhenDifferentVolume(t *testing.T) {
	orig := sameVolumeCompare
	t.Cleanup(func() { sameVolumeCompare = orig })
	sameVolumeCompare = func(a, b string) (bool, error) { return false, nil }

	source := filepath.Join("F:", "usb", "photo.jpg")
	preferred := filepath.Join("E:", "data", ".encryption_private")
	got := QuarantineRootForSource(source, preferred)
	want := filepath.Join(filepath.Dir(source), ".quarantine", "encryption")
	if got != want {
		t.Fatalf("got %q want local quarantine %q", got, want)
	}
}

func TestQuarantineRootForSourceFallsBackWhenCompareErrors(t *testing.T) {
	orig := sameVolumeCompare
	t.Cleanup(func() { sameVolumeCompare = orig })
	sameVolumeCompare = func(a, b string) (bool, error) { return true, errors.New("volume compare failed") }

	source := filepath.Join("F:", "usb", "photo.jpg")
	preferred := filepath.Join("E:", "data", ".encryption_private")
	got := QuarantineRootForSource(source, preferred)
	want := filepath.Join(filepath.Dir(source), ".quarantine", "encryption")
	if got != want {
		t.Fatalf("got %q want local quarantine on compare error %q", got, want)
	}
}
