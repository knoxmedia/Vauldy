//go:build !embedweb

package webembed

import "io/fs"

// FS returns nil unless built with -tags embedweb (see build.ps1).
func FS() fs.FS {
	return nil
}
