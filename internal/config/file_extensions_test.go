package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFileExtensionsYAMLDecode(t *testing.T) {
	const snippet = `
scan:
  file_extensions:
    video: [.ts]
`
	var c Config
	if err := yaml.Unmarshal([]byte(snippet), &c); err != nil {
		t.Fatal(err)
	}
	if c.Scan.FileExtensions == nil {
		t.Fatal("FileExtensions should be non-nil")
	}
	if c.Scan.FileExtensions.Video == nil {
		t.Fatal("Video should be non-nil")
	}
	if got := len(*c.Scan.FileExtensions.Video); got != 1 {
		t.Fatalf("Video len=%d want 1", got)
	}
	if (*c.Scan.FileExtensions.Video)[0] != ".ts" {
		t.Fatalf("Video[0]=%q want .ts", (*c.Scan.FileExtensions.Video)[0])
	}
}

func TestLoadFileExtensionsFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	yamlText := "server: {}\nscan:\n  file_extensions:\n    video: [.ts, .mp4]\n"
	if err := os.WriteFile(path, []byte(yamlText), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scan.FileExtensions == nil || c.Scan.FileExtensions.Video == nil {
		t.Fatal("expected file_extensions.video")
	}
	if len(*c.Scan.FileExtensions.Video) != 2 {
		t.Fatalf("video len=%d want 2", len(*c.Scan.FileExtensions.Video))
	}
}
