package subtitle

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCheckASRConfigNone(t *testing.T) {
	t.Parallel()
	r := CheckASRConfig(context.Background(), ASRConfig{Provider: "none"})
	if !r.OK {
		t.Fatalf("expected ok for none, got %+v", r)
	}
}

func TestCheckASRConfigShellMissingPlaceholders(t *testing.T) {
	t.Parallel()
	r := CheckASRConfig(context.Background(), ASRConfig{
		Provider: "shell",
		Shell:    `echo {input}`,
	})
	if r.OK {
		t.Fatalf("expected failure, got %+v", r)
	}
}

func TestCheckASRConfigShellValid(t *testing.T) {
	t.Parallel()
	r := CheckASRConfig(context.Background(), ASRConfig{
		Provider: "shell",
		Shell:    `tool --in "{input}" --out "{output_vtt}"`,
	})
	if !r.OK {
		t.Fatalf("expected ok, got %+v", r)
	}
}

func TestCheckOCRConfigDisabled(t *testing.T) {
	t.Parallel()
	r := CheckOCRConfig(context.Background(), "", OCRConfig{Enabled: false})
	if !r.OK {
		t.Fatalf("expected ok when disabled, got %+v", r)
	}
}

func TestResolveOCRPathDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := filepath.Join(dir, "tools", "subtitle_ocr", "bitmap_subtitle_ocr.py")
	got := resolveOCRPath(dir, "")
	if got != want {
		t.Fatalf("resolveOCRPath=%q want %q", got, want)
	}
}

func TestCheckOCRConfigMissingScript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := CheckOCRConfig(context.Background(), dir, OCRConfig{Enabled: true, ScriptPath: "tools/subtitle_ocr/missing.py"})
	if r.OK {
		t.Fatalf("expected failure, got %+v", r)
	}
}
