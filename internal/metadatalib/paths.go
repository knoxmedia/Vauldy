package metadatalib

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// PublicURLPrefix is the HTTP path root for locally stored scrape artwork.
	PublicURLPrefix = "/metadata/library"
)

// RelShardDir returns a sharded relative path for a media id, e.g. "3a/f9/12345".
// Two hex levels (256×256 buckets) spread directories evenly and avoid single-folder limits.
func RelShardDir(mediaID int64) string {
	if mediaID <= 0 {
		return "00/00/0"
	}
	id := uint64(mediaID)
	l1 := (id >> 8) & 0xff
	l2 := id & 0xff
	return fmt.Sprintf("%02x/%02x/%d", l1, l2, mediaID)
}

// MediaDir returns the absolute directory for one media's artwork files.
func MediaDir(root string, mediaID int64) string {
	return filepath.Join(root, filepath.FromSlash(RelShardDir(mediaID)))
}

// PublicURL returns the browser URL for a file under the sharded media directory.
func PublicURL(mediaID int64, filename string) string {
	filename = strings.TrimPrefix(strings.TrimSpace(filename), "/")
	return path.Join(PublicURLPrefix, RelShardDir(mediaID), filename)
}

// IsLocalMetadataURL reports whether the URL already points at our metadata library static route.
func IsLocalMetadataURL(u string) bool {
	u = strings.TrimSpace(u)
	return strings.HasPrefix(u, PublicURLPrefix+"/")
}

// ParseMediaIDFromPublicURL extracts media id from /metadata/library/aa/bb/{id}/file (best effort).
func ParseMediaIDFromPublicURL(u string) (int64, bool) {
	u = strings.TrimSpace(u)
	if !IsLocalMetadataURL(u) {
		return 0, false
	}
	trim := strings.TrimPrefix(u, PublicURLPrefix+"/")
	parts := strings.Split(trim, "/")
	if len(parts) < 4 {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
