package storage

import (
	"database/sql"
	"os"
	"strings"

	kcrypto "knox-media/internal/crypto"
)

// ResolveKeyframeProbePath picks a filesystem path for ffprobe and ffmpeg read-only access.
// Encrypted catalog paths (.enc) fall back to media_encrypted_assets.plain_path when the
// plaintext file is still present. ffprobe -show_packets and ffmpeg demux do not work on
// decrypt pipe:0 for typical moov-at-end MP4 inputs, but work on the original plaintext file.
func ResolveKeyframeProbePath(db *sql.DB, mediaID int64, catalogPath string) string {
	catalogPath = strings.TrimSpace(catalogPath)
	if catalogPath == "" || !kcrypto.IsEncFile(catalogPath) || db == nil || mediaID <= 0 {
		return catalogPath
	}
	var plain sql.NullString
	_ = db.QueryRow(`
		SELECT plain_path FROM media_encrypted_assets
		WHERE media_id = ? AND status = 'encrypted'
	`, mediaID).Scan(&plain)
	p := strings.TrimSpace(plain.String)
	if p == "" {
		return catalogPath
	}
	if _, err := os.Stat(p); err != nil {
		return catalogPath
	}
	return p
}
