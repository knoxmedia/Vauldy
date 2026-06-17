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

// MaterializeCLIFile returns a filesystem path readable by external CLIs (Python face detect, etc.).
// Knox .enc files are decrypted to a temp file; plaintext paths are returned as-is.
func MaterializeCLIFile(db *sql.DB, vault *keystore.Vault, mediaID int64, path string) (workPath string, cleanup func(), err error) {
	cleanup = func() {}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", cleanup, fmt.Errorf("empty path")
	}
	if !kcrypto.IsEncFile(path) {
		if _, err := os.Stat(path); err != nil {
			return "", cleanup, fmt.Errorf("source missing: %w", err)
		}
		return path, cleanup, nil
	}
	if db == nil || vault == nil {
		return "", cleanup, fmt.Errorf("encrypted file requires keystore")
	}
	seeker, err := OpenDerivedSeeker(db, vault, mediaID, path)
	if err != nil {
		return "", cleanup, err
	}
	defer func() { _ = seeker.Close() }()

	ext := cliTempExt(path)
	tmp, err := os.CreateTemp("", "knox-cli-*"+ext)
	if err != nil {
		return "", cleanup, err
	}
	tmpPath := tmp.Name()
	if _, err = io.Copy(tmp, seeker); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", cleanup, err
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", cleanup, err
	}
	return tmpPath, func() { _ = os.Remove(tmpPath) }, nil
}

func cliTempExt(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".enc")
	if ext := filepath.Ext(base); ext != "" {
		return ext
	}
	return ".bin"
}
