package subtitle

import (
	"strings"
	"testing"
)

func TestAppendASRShellFlagsInjectsMissing(t *testing.T) {
	got := appendASRShellFlags(`python asr_to_vtt.py --input "{input}" --output-vtt "{output_vtt}"`, ASRConfig{
		Engine: "faster-whisper", Model: "base", Language: "zh", Device: "cpu",
	})
	for _, want := range []string{"--engine faster-whisper", "--whisper-model base", "--whisper-language zh", "--whisper-device cpu"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestAppendASRShellFlagsPreservesExisting(t *testing.T) {
	in := `python asr.py --engine whisper --whisper-model small --input x --output-vtt y`
	got := appendASRShellFlags(in, ASRConfig{Engine: "faster-whisper", Model: "base", Language: "zh"})
	if strings.Contains(got, "--engine faster-whisper") {
		t.Fatalf("overwrote engine: %q", got)
	}
	if !strings.Contains(got, "--engine whisper") || !strings.Contains(got, "--whisper-model small") {
		t.Fatalf("lost original flags: %q", got)
	}
	if !strings.Contains(got, "--whisper-language zh") {
		t.Fatalf("should append language: %q", got)
	}
}

func TestAppendASRShellFlagsDefaultEngine(t *testing.T) {
	got := appendASRShellFlags(`python asr.py --input a --output-vtt b`, ASRConfig{})
	if !strings.Contains(got, "--engine whisper") {
		t.Fatalf("got %q", got)
	}
}
