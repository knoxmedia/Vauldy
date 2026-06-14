package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureConfigFileCreatesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	got, err := EnsureConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(path)
	if got != want {
		t.Fatalf("got path %q want %q", got, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not created: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 8200 {
		t.Fatalf("unexpected port %d", cfg.Server.Port)
	}
	if !strings.Contains(cfg.FFmpeg.FFmpegPath, "tools/ffmpeg/bin/") {
		t.Fatalf("unexpected ffmpeg path %q", cfg.FFmpeg.FFmpegPath)
	}
	if !strings.Contains(cfg.FFmpeg.FFprobePath, "tools/ffmpeg/bin/") {
		t.Fatalf("unexpected ffprobe path %q", cfg.FFmpeg.FFprobePath)
	}
	// second call must not overwrite
	got2, err := EnsureConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got2 != want {
		t.Fatalf("second call path %q", got2)
	}
}

func TestResolveConfigPathEnv(t *testing.T) {
	t.Setenv("KNOX_MEDIA_CONFIG", "/tmp/custom.yml")
	if got := ResolveConfigPath(); got != "/tmp/custom.yml" {
		t.Fatalf("got %q", got)
	}
}
