package pretranscode

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// osRemoveAll wraps os.RemoveAll for testability.
func osRemoveAll(path string) error { return os.RemoveAll(path) }

// dirSize walks a directory and returns the total byte count of all regular
// files. Returns 0 when the path does not exist.
func dirSize(path string) (int64, error) {
	var total int64
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// validRenditionArtifact reports whether a completed rendition still has the
// minimum on-disk artifacts needed to be presented as optimized.
func validRenditionArtifact(outputPath, outputFormat string) bool {
	outputPath = filepath.Clean(outputPath)
	if outputPath == "." || !nonEmptyRegularFile(outputPath) {
		return false
	}

	format := strings.ToLower(strings.TrimSpace(outputFormat))
	if format == "mp4" {
		return true
	}
	if format != "hls" && format != "dash" {
		return false
	}

	entries, err := os.ReadDir(filepath.Dir(outputPath))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		appropriate := ext == ".m4s" || format == "hls" && ext == ".ts" || format == "dash" && ext == ".mp4"
		if appropriate && nonEmptyRegularFile(filepath.Join(filepath.Dir(outputPath), entry.Name())) {
			return true
		}
	}
	return false
}

func nonEmptyRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}
