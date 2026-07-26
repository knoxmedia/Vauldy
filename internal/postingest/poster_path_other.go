//go:build !windows

package postingest

import "os"

func posterPathPlatformLinkedDefault(_ string, _ os.FileInfo) bool { return false }
