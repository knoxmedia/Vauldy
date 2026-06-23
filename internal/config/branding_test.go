package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveBrandingPatchesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8200\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveBranding(path, BrandingConfig{AppName: "Home Cinema", FaviconPath: "icons/app.svg"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BrandingAppName() != "Home Cinema" {
		t.Fatalf("app_name=%q", cfg.Branding.AppName)
	}
	if cfg.Branding.FaviconPath != "icons/app.svg" {
		t.Fatalf("favicon_path=%q", cfg.Branding.FaviconPath)
	}
}
