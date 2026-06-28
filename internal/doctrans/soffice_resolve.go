package doctrans

import (
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/config"
)

func mediaRoots(primary string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	add(primary)
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		add(wd)
	}
	return out
}

func resolveSofficePath(mediaRoot string, cfg config.DocTransConfig) string {
	for _, root := range mediaRoots(mediaRoot) {
		conv := &Converter{MediaRoot: root, Config: cfg}
		if p := absSofficePath(root, conv.resolveLibreOffice()); p != "" {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}
