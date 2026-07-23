package imagethumb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCommitGuardPreventsArtifactPublication(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jpg")
	if err := os.WriteFile(source, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(dir, "fake-ffmpeg.bat")
	script := "@echo off\r\nset last=\r\n:loop\r\nif \"%~1\"==\"\" goto done\r\nset last=%~1\r\nshift\r\ngoto loop\r\n:done\r\necho jpeg>\"%last%\"\r\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := errors.New("stale thumbnail lease")
	ctx := WithCommitGuard(context.Background(), func(context.Context) error { return stale })
	paths, err := Ensure(ctx, nil, nil, nil, ffmpeg, source, filepath.Join(dir, "cache"), 7)
	if !errors.Is(err, stale) {
		t.Fatalf("err=%v want stale", err)
	}
	if _, statErr := os.Stat(paths.Thumb); !os.IsNotExist(statErr) {
		t.Fatalf("thumb published despite stale guard: %v", statErr)
	}
	if _, statErr := os.Stat(paths.Medium); !os.IsNotExist(statErr) {
		t.Fatalf("medium published despite stale guard: %v", statErr)
	}
}
