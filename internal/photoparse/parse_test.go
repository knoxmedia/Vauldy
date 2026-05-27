package photoparse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsPhotoLibraryType(t *testing.T) {
	if !IsPhotoLibraryType("photo") || !IsPhotoLibraryType("Photo") {
		t.Fatal("expected photo")
	}
	if IsPhotoLibraryType("music") {
		t.Fatal("music is not photo")
	}
}

func TestShouldScanFile(t *testing.T) {
	cases := []struct {
		lib, ft string
		want    bool
	}{
		{"photo", "image", true},
		{"photo", "video", false},
		{"music", "audio", true},
		{"music", "image", false},
		{"movie", "video", true},
		{"movie", "image", false},
	}
	for _, c := range cases {
		if got := ShouldScanFile(c.lib, c.ft); got != c.want {
			t.Fatalf("%s/%s got=%v want=%v", c.lib, c.ft, got, c.want)
		}
	}
}

func TestParseFromFilePNG(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.png")
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := ParseFromFile(path)
	if meta.Width != 1 || meta.Height != 1 {
		t.Fatalf("dimensions=%dx%d", meta.Width, meta.Height)
	}
	if meta.TakenAt == "" {
		t.Fatal("expected fallback taken_at")
	}
}
