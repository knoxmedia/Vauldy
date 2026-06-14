package config

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

//go:embed default/config.yml
var defaultConfigYAML []byte

// ResolveConfigPath picks the config file location. When none exists yet, returns
// the preferred path where EnsureConfigFile should create a default config
// (executable directory first, then current working directory).
func ResolveConfigPath() string {
	if p := strings.TrimSpace(os.Getenv("KNOX_MEDIA_CONFIG")); p != "" {
		return p
	}
	for _, c := range []string{"config.yml", filepath.Join("media", "config.yml")} {
		if _, err := os.Stat(c); err == nil {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		exeConfig := filepath.Join(exeDir, "config.yml")
		if _, err := os.Stat(exeConfig); err == nil {
			return exeConfig
		}
		return exeConfig
	}
	return "config.yml"
}

// EnsureConfigFile creates a default config.yml when path is missing.
func EnsureConfigFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty config path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		return abs, nil
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	dir := filepath.Dir(abs)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create config dir: %w", err)
		}
	}
	content := localizeDefaultConfig(defaultConfigYAML)
	if len(content) == 0 {
		return "", fmt.Errorf("embedded default config is empty")
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		return "", fmt.Errorf("write default config: %w", err)
	}
	log.Printf("created default config at %s", abs)
	return abs, nil
}
