//go:build embedweb

package webembed

import (
	"embed"
	"io/fs"
)

// Dist holds the Vite production build synced to internal/webembed/dist before go build.
//
//go:embed all:dist
var dist embed.FS

// FS returns the embedded web/dist root (index.html at top level), or nil when unavailable.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}
