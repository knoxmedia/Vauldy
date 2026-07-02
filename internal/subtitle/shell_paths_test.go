package subtitle

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveShellMediaPaths(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(`E:\Projects\Knox\media`)
	sh := `"tools/recognition/.venv/Scripts/python.exe" "tools/asr/asr_to_vtt.py" --input "{input}"`
	got := resolveShellMediaPaths(sh, root)
	if !strings.Contains(got, filepath.ToSlash(root)+`/tools/recognition`) {
		t.Fatalf("venv path not resolved: %q", got)
	}
	if !strings.Contains(got, `/tools/asr/asr_to_vtt.py`) {
		t.Fatalf("script path not resolved: %q", got)
	}
}
