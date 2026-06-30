package branding

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed default_favicon.svg
var DefaultFaviconSVG []byte

const DefaultAppName = "Vauldy"

// ResolveFaviconPath returns an on-disk favicon path when configured and present.
func ResolveFaviconPath(faviconPath, configPath string) string {
	p := strings.TrimSpace(faviconPath)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) && strings.TrimSpace(configPath) != "" {
		p = filepath.Join(filepath.Dir(configPath), p)
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func FaviconContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return "image/svg+xml"
	}
}
