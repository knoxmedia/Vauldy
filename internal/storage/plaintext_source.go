package storage

import (
	"database/sql"
	"os"

	kcrypto "knox-media/internal/crypto"
)

// PlaintextSourceAvailable reports whether ffmpeg can read a plaintext file for
// pretranscode. Encrypted catalog entries require an on-disk plain_path; Knox
// decrypt pipe alone is not sufficient for batch pretranscode workers.
func PlaintextSourceAvailable(db *sql.DB, mediaID, libraryID int64, catalogPath string) bool {
	abs := ResolveMediaAbsolutePath(db, libraryID, catalogPath)
	if abs == "" {
		return false
	}
	if !catalogUsesEncInput(db, mediaID, abs) {
		_, err := os.Stat(abs)
		return err == nil
	}
	probe := ResolveKeyframeProbePath(db, mediaID, abs)
	if probe == "" || kcrypto.IsEncFile(probe) {
		return false
	}
	_, err := os.Stat(probe)
	return err == nil
}
