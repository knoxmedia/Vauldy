package subtitle

import (
	"context"
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
	r := CheckOCRConfig(context.Background(), OCRConfig{Enabled: false})
	if !r.OK {
		t.Fatalf("expected ok when disabled, got %+v", r)
	}
}

func TestCheckOCRConfigMissingScript(t *testing.T) {
	t.Parallel()
	r := CheckOCRConfig(context.Background(), OCRConfig{Enabled: true, ScriptPath: ""})
	if r.OK {
		t.Fatalf("expected failure, got %+v", r)
	}
}
