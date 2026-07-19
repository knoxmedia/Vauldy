//go:build !windows

package atomicfile

import "os"

func ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
