package docparse

import (
	"path/filepath"
	"strings"
)

func matchExclude(relPath, pattern string) bool {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" {
		return false
	}
	relPath = filepath.ToSlash(relPath)
	if strings.Contains(pattern, "**") {
		needle := strings.Trim(pattern, "*")
		needle = strings.Trim(needle, "/")
		if needle != "" && strings.Contains(relPath, needle) {
			return true
		}
	}
	if ok, _ := filepath.Match(pattern, relPath); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, filepath.Base(relPath)); ok {
		return true
	}
	return false
}

// ParseExcludePatterns splits comma/newline separated exclude rules.
func ParseExcludePatterns(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == ','
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
