package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"time"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
)

// PlaintextSeeker is a seekable decrypted view of an encrypted asset.
type PlaintextSeeker struct {
	io.ReadSeekCloser
	cleanup func()
	modTime time.Time
}

// ModTime returns the source .enc file modification time for caching headers.
func (p *PlaintextSeeker) ModTime() time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.modTime
}

// Close releases decrypted plaintext from memory.
func (p *PlaintextSeeker) Close() error {
	if p == nil {
		return nil
	}
	err := p.ReadSeekCloser.Close()
	if p.cleanup != nil {
		p.cleanup()
	}
	return err
}

// OpenPlaintextSeeker decrypts a Knox .enc file into memory and returns a seekable reader
// suitable for http.ServeContent (Range / resume). Plaintext is wiped on Close.
func OpenPlaintextSeeker(db *sql.DB, vault *keystore.Vault, mediaID int64, encPath string) (*PlaintextSeeker, error) {
	encPath = strings.TrimSpace(encPath)
	if encPath == "" {
		return nil, os.ErrInvalid
	}
	if !kcrypto.IsEncFile(encPath) {
		f, err := os.Open(encPath)
		if err != nil {
			return nil, err
		}
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		return &PlaintextSeeker{ReadSeekCloser: f, modTime: st.ModTime()}, nil
	}
	if db == nil || vault == nil {
		return nil, os.ErrInvalid
	}
	var wrappedHex, ivHex string
	if err := db.QueryRow(`
		SELECT wrapped_dek, iv FROM media_encrypted_assets WHERE media_id = ? AND status = 'encrypted'
	`, mediaID).Scan(&wrappedHex, &ivHex); err != nil {
		return nil, err
	}
	wrapped, err := hex.DecodeString(wrappedHex)
	if err != nil {
		return nil, err
	}
	_ = ivHex

	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range kek {
			kek[i] = 0
		}
	}()

	st, err := os.Stat(encPath)
	if err != nil {
		return nil, err
	}
	rsc, err := kcrypto.OpenDecryptSeeker(encPath, wrapped, kek)
	if err != nil {
		return nil, err
	}
	return &PlaintextSeeker{ReadSeekCloser: rsc, modTime: st.ModTime()}, nil
}
