//go:build !windows

package recognition

import (
	"context"
	"fmt"
)

func installTesseractWindows(ctx context.Context, destDir string) (string, string, error) {
	_ = ctx
	_ = destDir
	return "", "", fmt.Errorf("installTesseractWindows called on non-windows")
}

func findWindowsSystemTesseract() (exe string, tessdata string) {
	return "", ""
}

func preferTessdataDir(systemDir, localDir string) string {
	if systemDir != "" {
		return systemDir
	}
	return localDir
}
