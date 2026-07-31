package storage

import (
	"path/filepath"
	"strings"
)

// sameVolumeCompare is the injectable seam used by QuarantineRootForSource.
// Tests may override it; production defaults to SameVolume.
var sameVolumeCompare = SameVolume

// QuarantineRootForSource returns preferredRoot when it shares a volume with
// sourcePath; otherwise a local quarantine directory beside the source.
func QuarantineRootForSource(sourcePath, preferredRoot string) string {
	local := filepath.Join(filepath.Dir(sourcePath), ".quarantine", "encryption")
	if strings.TrimSpace(preferredRoot) == "" {
		return local
	}
	same, err := sameVolumeCompare(sourcePath, preferredRoot)
	if err != nil || !same {
		return local
	}
	return preferredRoot
}
