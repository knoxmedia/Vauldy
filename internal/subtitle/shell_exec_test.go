package subtitle

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStripShellCDPrefix(t *testing.T) {
	t.Parallel()
	in := `cd /d "{output_dir}" && "tools/python.exe" "tools/asr.py" --input "{input}"`
	want := `"tools/python.exe" "tools/asr.py" --input "{input}"`
	if got := stripShellCDPrefix(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSplitShellCommandLine(t *testing.T) {
	t.Parallel()
	parts := splitShellCommandLine(`"a b.exe" "c d.py" --input "f:\迅雷\video.mp4" --flag val`)
	if len(parts) != 6 {
		t.Fatalf("parts=%v want 6", parts)
	}
	if parts[0] != "a b.exe" || parts[1] != "c d.py" || parts[2] != "--input" {
		t.Fatalf("unexpected parts: %v", parts)
	}
	if parts[3] != `f:\迅雷\video.mp4` {
		t.Fatalf("input path=%q", parts[3])
	}
}

func TestRunShellCommandEcho(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh -c on unix only")
	}
	t.Parallel()
	s := &Service{MediaRoot: t.TempDir()}
	out, err := s.runShellCommand(context.Background(), `echo hello`)
	if err != nil {
		t.Fatalf("runShellCommand: %v out=%q", err, string(out))
	}
	if !strings.Contains(string(out), "hello") {
		t.Fatalf("output=%q", string(out))
	}
}

func TestRunShellCommandKnoxASRHelp(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only")
	}
	root := filepath.Clean(`E:\Projects\PowerCOM\Knox\media`)
	py := filepath.Join(root, `tools\recognition\.venv\Scripts\python.exe`)
	script := filepath.Join(root, `tools\asr\asr_to_vtt.py`)
	if _, err := os.Stat(py); err != nil {
		t.Skip("venv python missing")
	}
	if _, err := os.Stat(script); err != nil {
		t.Skip("asr script missing")
	}
	outDir := filepath.Join(root, `data\subtitles\396`)
	sh := `cd /d "` + outDir + `" && "` + filepath.ToSlash(py) + `" "` + filepath.ToSlash(script) + `" --help`
	s := &Service{MediaRoot: root}
	out, err := s.runShellCommand(context.Background(), sh)
	if err != nil {
		t.Fatalf("runShellCommand: %v out=%q", err, string(out))
	}
	if !strings.Contains(string(out), "usage:") {
		t.Fatalf("expected help output, got %q", string(out))
	}
}

func TestBuildShellCommandUsesDirectExecOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only")
	}
	py := filepath.Join(t.TempDir(), "python.exe")
	if err := os.WriteFile(py, []byte("@echo off\r\necho ok\r\n"), 0o644); err != nil {
		t.Fatalf("write py stub: %v", err)
	}
	sh := `"` + py + `" --help`
	cmd, ok := buildShellCommand(context.Background(), sh)
	if !ok {
		t.Fatal("buildShellCommand returned false")
	}
	if len(cmd.Args) < 2 || cmd.Args[0] != py {
		t.Fatalf("args=%v want direct exec of %q", cmd.Args, py)
	}
}
