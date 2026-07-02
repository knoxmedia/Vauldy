package doctrans

import (
	"os"
	"path/filepath"
	"strings"
)

func relPathUnder(base, abs string) string {
	abs = filepath.Clean(abs)
	base = filepath.Clean(base)
	if rel, err := filepath.Rel(base, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return filepath.ToSlash(rel)
	}
	return ""
}
