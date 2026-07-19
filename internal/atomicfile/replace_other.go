//go:build !windows

package atomicfile

import "os"

func ReplaceFile(source, target string) error { return os.Rename(source, target) }
