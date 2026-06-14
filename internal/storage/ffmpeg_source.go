package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
)

// FFmpegInput is a decrypted view of media suitable for ffmpeg -i.
type FFmpegInput struct {
	Path    string
	Stdin   io.Reader
	Cleanup func()
	FromEnc bool
}

// OpenFFmpegInput resolves media for ffmpeg. Knox .enc uses pipe:0 (Stdin); plaintext uses Path.
// startSec seeks into encrypted plaintext when durationSec > 0.
func OpenFFmpegInput(db *sql.DB, vault *keystore.Vault, mediaID int64, path string, startSec, durationSec float64) (*FFmpegInput, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty media path")
	}
	if !kcrypto.IsEncFile(path) {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("source missing: %w", err)
		}
		return &FFmpegInput{Path: path}, nil
	}
	if db == nil || vault == nil {
		return nil, fmt.Errorf("encrypted source requires keystore")
	}
	var wrappedHex string
	if err := db.QueryRow(`
		SELECT wrapped_dek FROM media_encrypted_assets WHERE media_id = ? AND status = 'encrypted'
	`, mediaID).Scan(&wrappedHex); err != nil {
		return nil, fmt.Errorf("encrypted asset metadata: %w", err)
	}
	wrapped, err := hex.DecodeString(wrappedHex)
	if err != nil {
		return nil, err
	}
	kek, err := vault.GetKEK(context.Background())
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range kek {
			kek[i] = 0
		}
	}()

	seeker, err := kcrypto.OpenDecryptSeeker(path, wrapped, kek)
	if err != nil {
		return nil, err
	}
	if startSec > 0.01 {
		off := int64(0)
		if durationSec > 0.01 {
			if plainSize, perr := kcrypto.PlaintextSize(path); perr == nil && plainSize > 0 {
				off = int64(float64(plainSize) * (startSec / durationSec))
			}
		}
		if _, err := seeker.Seek(off, io.SeekStart); err != nil {
			_ = seeker.Close()
			return nil, fmt.Errorf("encrypted seek: %w", err)
		}
	}

	pr, pw := io.Pipe()
	go func() {
		_, copyErr := io.Copy(pw, seeker)
		_ = seeker.Close()
		_ = pw.CloseWithError(copyErr)
	}()
	return &FFmpegInput{
		Stdin:   pr,
		Cleanup: func() { _ = pr.Close() },
		FromEnc: true,
	}, nil
}

// ApplyFFmpegInput appends -i args to ffmpeg argv and wires Stdin on cmd when using pipe.
func ApplyFFmpegInput(args []string, in *FFmpegInput) ([]string, io.Reader) {
	if in == nil {
		return args, nil
	}
	if in.Path != "" {
		return append(args, "-i", in.Path), nil
	}
	return append(args, "-i", "pipe:0"), in.Stdin
}
