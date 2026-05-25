package recognition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaRoot(t *testing.T) {
	t.Parallel()
	got := MediaRoot(`E:\Projects\Knox\media\config.yml`)
	want := filepath.Clean(`E:\Projects\Knox\media`)
	if got != want {
		t.Fatalf("MediaRoot=%q want %q", got, want)
	}
}

func TestRelIfUnder(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	inner := filepath.Join(base, "tools", "a.py")
	if err := os.MkdirAll(filepath.Dir(inner), 0o755); err != nil {
		t.Fatal(err)
	}
	rel := relIfUnder(base, inner)
	if rel != "tools/a.py" && rel != `tools\a.py` {
		t.Fatalf("rel=%q", rel)
	}
}

func TestDefaultASRShellHasPlaceholders(t *testing.T) {
	t.Parallel()
	sh := DefaultASRShell("tools/recognition/.venv/Scripts/python.exe", "tools/asr/asr_to_vtt.py")
	for _, ph := range []string{"{input}", "{output_vtt}"} {
		if !strings.Contains(sh, ph) {
			t.Fatalf("missing %s in %q", ph, sh)
		}
	}
}

func TestInstallASRRequiresScript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := InstallASR(context.Background(), dir)
	if err == nil {
		t.Fatal("expected error without repo scripts")
	}
}

func TestEnsureVenvRequiresPython(t *testing.T) {
	t.Parallel()
	if _, _, err := findSystemPython(context.Background()); err != nil {
		t.Skip("python not available:", err)
	}
	dir := t.TempDir()
	py, err := EnsureVenv(context.Background(), dir)
	if err != nil {
		t.Fatalf("EnsureVenv: %v", err)
	}
	if !fileExists(py) {
		t.Fatalf("venv python missing: %s", py)
	}
}
