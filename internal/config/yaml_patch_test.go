package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSaveSubtitleRecognitionPatchesYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	initial := `server:
  port: 8200
subtitle:
  auto_on_scan: true
  asr:
    provider: none
  graphical_ocr:
    enabled: false
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	asr := ASRConfig{
		Provider:    "shell",
		WhisperPath: "whisper-bin",
		ExtraArgs:   []string{"--model", "small"},
		Shell:       `python script.py --input "{input}" --output-vtt "{output_vtt}"`,
	}
	ocr := GraphicalOCRConfig{
		Enabled:       true,
		TesseractPath: "tesseract",
		Languages:     "chi_sim",
		ScriptPath:    "tools/subtitle_ocr/bitmap_subtitle_ocr.py",
	}
	if err := SaveSubtitleRecognition(path, false, asr, ocr, true); err != nil {
		t.Fatalf("SaveSubtitleRecognition: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read patched: %v", err)
	}
	text := string(b)
	if !strings.Contains(text, "port: 8200") {
		t.Fatalf("expected server.port preserved, got:\n%s", text)
	}
	if !strings.Contains(text, "auto_on_scan: false") {
		t.Fatalf("expected auto_on_scan patched to false, got:\n%s", text)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.SubtitleAutoOnScan() {
		t.Fatal("auto_on_scan should be false after patch")
	}
	if cfg.Subtitle.ASR.Provider != "shell" {
		t.Fatalf("asr provider=%q want shell", cfg.Subtitle.ASR.Provider)
	}
	if len(cfg.Subtitle.ASR.ExtraArgs) != 2 || cfg.Subtitle.ASR.ExtraArgs[0] != "--model" {
		t.Fatalf("extra_args=%v", cfg.Subtitle.ASR.ExtraArgs)
	}
	if !cfg.Subtitle.GraphicalOCR.Enabled {
		t.Fatal("ocr should be enabled")
	}
	if cfg.Subtitle.GraphicalOCR.ScriptPath != ocr.ScriptPath {
		t.Fatalf("script_path=%q", cfg.Subtitle.GraphicalOCR.ScriptPath)
	}
	if !cfg.SubtitleAIProofreadEnabled() {
		t.Fatal("ai_proofread should be true after patch")
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse patched yaml: %v", err)
	}
}
