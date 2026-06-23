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
	Path          string
	Stdin         io.Reader
	Cleanup       func()
	FromEnc       bool
	PlainFallback bool // read-only plaintext path for .enc catalog entries (no decrypt pipe)
}

// OpenFFmpegInput resolves media for ffmpeg. Knox .enc streams decrypted bytes on Stdin (pipe:0)
// when no plaintext is available. pipeByteOffset may seek the decrypt reader for non-container
// payloads; MP4/MOV JIT playback must pass 0 and use ffmpeg -ss because container headers live at
// the start of the stream. When media_encrypted_assets still references an on-disk plain_path,
// that file is used read-only instead of pipe:0.
func OpenFFmpegInput(db *sql.DB, vault *keystore.Vault, mediaID int64, path string, pipeByteOffset int64) (*FFmpegInput, error) {
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
	if plain := ResolveKeyframeProbePath(db, mediaID, path); plain != path {
		if _, err := os.Stat(plain); err != nil {
			return nil, fmt.Errorf("plaintext fallback missing: %w", err)
		}
		return &FFmpegInput{Path: plain, PlainFallback: true}, nil
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
	if pipeByteOffset > 0 {
		if _, err := seeker.Seek(pipeByteOffset, io.SeekStart); err != nil {
			_ = seeker.Close()
			return nil, fmt.Errorf("encrypted pipe seek: %w", err)
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
