package storage

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
)

// MaterializePlaintextTemp decrypts Knox .enc to a temp file for external CLIs that need a filesystem path
// (e.g. Whisper, custom ASR shell). Returns the original path when not encrypted.
func MaterializePlaintextTemp(db *sql.DB, vault *keystore.Vault, mediaID int64, path string) (workPath string, cleanup func(), err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil, fmt.Errorf("empty media path")
	}
	if !InputNeedsPipe(db, mediaID, path) {
		return path, func() {}, nil
	}
	if db == nil || vault == nil {
		return "", nil, fmt.Errorf("encrypted source requires keystore")
	}
	seeker, err := OpenPlaintextSeeker(db, vault, mediaID, path)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = seeker.Close() }()

	ext := tempPlaintextExt(db, mediaID, path)
	tmp, err := os.CreateTemp("", "knox-plain-*"+ext)
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmp.Name()
	if _, err = io.Copy(tmp, seeker); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", nil, err
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", nil, err
	}
	return tmpPath, func() { _ = os.Remove(tmpPath) }, nil
}

func tempPlaintextExt(db *sql.DB, mediaID int64, encPath string) string {
	ext := filepath.Ext(encPath)
	if ext != "" && !kcrypto.IsEncFile(encPath) {
		return ext
	}
	if db != nil && mediaID > 0 {
		var plainPath, format sql.NullString
		_ = db.QueryRow(`
			SELECT COALESCE(e.plain_path,''), COALESCE(m.format,'')
			FROM media_encrypted_assets e
			JOIN media m ON m.id = e.media_id
			WHERE e.media_id = ? AND e.status = 'encrypted'
		`, mediaID).Scan(&plainPath, &format)
		if p := strings.TrimSpace(plainPath.String); p != "" {
			if e := filepath.Ext(p); e != "" {
				return e
			}
		}
		if f := strings.TrimSpace(format.String); f != "" {
			f = strings.TrimPrefix(f, ".")
			if f != "" {
				return "." + f
			}
		}
	}
	return ".bin"
}
