package fileutil

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	pretranscodePresetDirRe = regexp.MustCompile(`(?i)^preset\d+$`)
	pretranscodeUUIDDirRe   = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// IsPretranscodeOutputPath reports whether path points at generated pretranscode
// output (for example movie.pretranscode/preset2/720p/720p.m3u8).
func IsPretranscodeOutputPath(path string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return false
	}
	parts := strings.Split(cleaned, string(filepath.Separator))
	for i, part := range parts {
		if part == "" {
			continue
		}
		if strings.HasSuffix(strings.ToLower(part), ".pretranscode") {
			return true
		}
		if pretranscodePresetDirRe.MatchString(part) {
			prev := ""
			if i > 0 {
				prev = parts[i-1]
			}
			if strings.HasSuffix(strings.ToLower(prev), ".pretranscode") || pretranscodeUUIDDirRe.MatchString(prev) {
				return true
			}
		}
	}
	return false
}

// IsPretranscodeOutputDir reports whether a directory should be skipped entirely
// during library scan because it contains pretranscode output.
func IsPretranscodeOutputDir(path string) bool {
	return IsPretranscodeOutputPath(path)
}
