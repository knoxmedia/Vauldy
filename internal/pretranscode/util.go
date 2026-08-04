package pretranscode

import (
	"io/fs"
	"os"
	"path/filepath"
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
