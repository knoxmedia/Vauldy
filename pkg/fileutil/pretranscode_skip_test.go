package fileutil

import (
	"path/filepath"
	"testing"
)

func TestIsPretranscodeOutputPathSourceMode(t *testing.T) {
	t.Parallel()

	root := filepath.Join(`F:\videos`, "movie.pretranscode", "preset2", "720p", "720p.m3u8")
	if !IsPretranscodeOutputPath(root) {
		t.Fatalf("expected source-mode pretranscode path to match")
	}
	if !IsPretranscodeOutputDir(filepath.Join(`F:\videos`, "movie.pretranscode")) {
		t.Fatalf("expected .pretranscode directory to be skipped")
	}
}

func TestIsPretranscodeOutputPathCustomMode(t *testing.T) {
	t.Parallel()

	root := filepath.Join(`D:\out`, "550e8400-e29b-41d4-a716-446655440000", "preset3", "1080p.mp4")
	if !IsPretranscodeOutputPath(root) {
		t.Fatalf("expected custom-mode pretranscode path to match")
	}
}

func TestIsPretranscodeOutputPathNormalMedia(t *testing.T) {
	t.Parallel()

	cases := []string{
		filepath.Join(`F:\videos`, "movie.mp4"),
		filepath.Join(`F:\videos`, "preset releases", "movie.mp4"),
		filepath.Join(`F:\videos`, "preset1", "movie.mp4"),
	}
	for _, path := range cases {
		if IsPretranscodeOutputPath(path) {
			t.Fatalf("expected normal media path %q to be scannable", path)
		}
	}
}
