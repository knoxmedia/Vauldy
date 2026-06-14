package config

import (
	"runtime"
	"strings"
	"testing"
)

func TestLocalizeDefaultConfigWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only")
	}
	out := string(localizeDefaultConfig(defaultConfigYAML))
	if !strings.Contains(out, `ffmpeg_path: "tools/ffmpeg/bin/ffmpeg.exe"`) {
		t.Fatalf("missing windows ffmpeg path: %s", out)
	}
	if !strings.Contains(out, `ffprobe_path: "tools/ffmpeg/bin/ffprobe.exe"`) {
		t.Fatalf("missing windows ffprobe path")
	}
}

func TestLocalizeDefaultConfigLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only")
	}
	out := string(localizeDefaultConfig(defaultConfigYAML))
	if strings.Contains(out, ".exe") {
		t.Fatalf("linux default should not contain .exe: %s", out)
	}
}
