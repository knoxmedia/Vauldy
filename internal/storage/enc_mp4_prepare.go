package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
)

// isISOBaseMedia reports paths that are typically MP4/MOV containers needing moov-at-start for pipe demux.
func isISOBaseMedia(path string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".mp4", ".m4v", ".mov", ".3gp", ".3g2":
		return true
	default:
		return false
	}
}

// prepareMP4ForEncPipe rewrites ISO-BMFF with -movflags faststart (moov before mdat) into a temp file
// so ffmpeg/ffprobe can demux Knox decrypt pipe:0 after the plaintext source is removed.
func prepareMP4ForEncPipe(ctx context.Context, ffmpegPath, dataDir string, mediaID int64, plainPath string) (string, func(), error) {
	plainPath = strings.TrimSpace(plainPath)
	if !isISOBaseMedia(plainPath) {
		return plainPath, func() {}, nil
	}
	ffmpegPath = strings.TrimSpace(ffmpegPath)
	if ffmpegPath == "" {
		return "", nil, fmt.Errorf("ffmpeg path required for mp4 encrypted pipe playback")
	}
	if strings.TrimSpace(dataDir) == "" {
		dataDir = os.TempDir()
	}
	prepDir := filepath.Join(dataDir, ".encrypt-prep")
	if err := os.MkdirAll(prepDir, 0o700); err != nil {
		return "", nil, err
	}
	tmp, err := os.CreateTemp(prepDir, fmt.Sprintf("%d-*.mp4", mediaID))
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-y",
		"-i", plainPath,
		"-c", "copy",
		"-movflags", "faststart",
		tmpPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmpPath)
		return "", nil, fmt.Errorf("mp4 faststart: %w: %s", err, strings.TrimSpace(string(out)))
	}
	cleanup := func() { _ = os.Remove(tmpPath) }
	return tmpPath, cleanup, nil
}

// resolveEncryptSource picks the byte stream to envelope-encrypt. When requirePipePlayback is true
// (library deletes plaintext), MP4/MOV inputs must be faststart-remuxed first.
func (s *AssetEncryptor) resolveEncryptSource(ctx context.Context, mediaID int64, plainPath string, requirePipePlayback bool) (string, func(), bool, error) {
	plainPath = strings.TrimSpace(plainPath)
	if plainPath == "" {
		return "", nil, false, fmt.Errorf("empty plain path")
	}
	if !isISOBaseMedia(plainPath) {
		return plainPath, func() {}, false, nil
	}
	prepared, cleanup, err := prepareMP4ForEncPipe(ctx, s.FFmpegPath, s.DataDir, mediaID, plainPath)
	if err == nil && prepared != plainPath {
		return prepared, cleanup, true, nil
	}
	if requirePipePlayback {
		if cleanup != nil {
			cleanup()
		}
		if err != nil {
			return "", nil, false, err
		}
		return "", nil, false, fmt.Errorf("mp4 faststart required for encrypted pipe playback")
	}
	if cleanup != nil {
		cleanup()
	}
	return plainPath, func() {}, false, nil
}

func markKeyframeReindex(db *sql.DB, mediaID int64) {
	if db == nil || mediaID <= 0 {
		return
	}
	_, _ = db.Exec(`
		INSERT INTO keyframe_task (media_id, status, updated_at)
		VALUES (?, 'waiting', CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
		  status = 'waiting',
		  error_message = NULL,
		  updated_at = CURRENT_TIMESTAMP
	`, mediaID)
}

func encryptedPipeDemuxOK(db *sql.DB, vault *keystore.Vault, ffprobePath string, mediaID int64, encPath string) bool {
	ffprobePath = strings.TrimSpace(ffprobePath)
	if db == nil || vault == nil || ffprobePath == "" || mediaID <= 0 || !kcrypto.IsEncFile(encPath) {
		return false
	}
	mp, err := ProbeMediaFile(db, vault, ffprobePath, mediaID, encPath, []string{"-t", "1"})
	if err != nil || mp == nil || mp.Summary == nil {
		return false
	}
	if mp.Cleanup != nil {
		mp.Cleanup()
	}
	return strings.TrimSpace(mp.Summary.VideoCodec) != ""
}

// RepackEncryptedMP4ForPipe decrypts an existing .enc MP4, faststart-remuxes, and re-envelopes in place
// so JIT/ffmpeg can demux via decrypt pipe after plaintext removal.
func (s *AssetEncryptor) RepackEncryptedMP4ForPipe(ctx context.Context, mediaID int64) error {
	if s == nil || s.DB == nil || s.Vault == nil {
		return fmt.Errorf("encryptor not configured")
	}
	var encPath string
	if err := s.DB.QueryRowContext(ctx, `
		SELECT enc_path FROM media_encrypted_assets WHERE media_id = ? AND status = 'encrypted'
	`, mediaID).Scan(&encPath); err != nil {
		return err
	}
	encPath = strings.TrimSpace(encPath)
	if !isISOBaseMedia(encPath) {
		return nil
	}
	if encryptedPipeDemuxOK(s.DB, s.Vault, s.FFprobePath, mediaID, encPath) {
		return nil
	}

	kek, err := s.Vault.GetKEK(ctx)
	if err != nil {
		return err
	}
	defer func() {
		for i := range kek {
			kek[i] = 0
		}
	}()

	rc, err := OpenPlaintext(s.DB, s.Vault, mediaID, encPath)
	if err != nil {
		return err
	}
	prepDir := filepath.Join(s.DataDir, ".encrypt-prep")
	if err := os.MkdirAll(prepDir, 0o700); err != nil {
		_ = rc.Close()
		return err
	}
	plainTmp, err := os.CreateTemp(prepDir, fmt.Sprintf("%d-decrypt-*.mp4", mediaID))
	if err != nil {
		_ = rc.Close()
		return err
	}
	plainTmpPath := plainTmp.Name()
	if _, err := io.Copy(plainTmp, rc); err != nil {
		_ = rc.Close()
		_ = plainTmp.Close()
		_ = os.Remove(plainTmpPath)
		return err
	}
	_ = rc.Close()
	_ = plainTmp.Close()
	defer os.Remove(plainTmpPath)

	fastPath, fastCleanup, err := prepareMP4ForEncPipe(ctx, s.FFmpegPath, s.DataDir, mediaID, plainTmpPath)
	if err != nil {
		return err
	}
	defer fastCleanup()

	src, err := os.Open(fastPath)
	if err != nil {
		return err
	}
	defer src.Close()

	newEnc := encPath + ".repack"
	dst, err := os.OpenFile(newEnc, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	result, err := kcrypto.EncryptFile(src, dst, kek)
	if closeErr := dst.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(newEnc)
		return err
	}
	if err := os.Remove(encPath); err != nil {
		_ = os.Remove(newEnc)
		return err
	}
	if err := os.Rename(newEnc, encPath); err != nil {
		_ = os.Remove(newEnc)
		return err
	}

	wrappedHex := hex.EncodeToString(result.WrappedDEK)
	ivHex := hex.EncodeToString(result.IV)
	_, err = s.DB.ExecContext(ctx, `
		UPDATE media_encrypted_assets
		SET wrapped_dek = ?, iv = ?, updated_at = CURRENT_TIMESTAMP
		WHERE media_id = ? AND status = 'encrypted'
	`, wrappedHex, ivHex, mediaID)
	return err
}

// KickEncryptedMP4PipeRepairs rewrites legacy moov-at-end encrypted MP4s in the background.
func KickEncryptedMP4PipeRepairs(enc *AssetEncryptor) {
	if enc == nil || enc.DB == nil || strings.TrimSpace(enc.FFmpegPath) == "" {
		return
	}
	go func() {
		rows, err := enc.DB.Query(`
			SELECT e.media_id, e.enc_path, COALESCE(e.plain_path,'')
			FROM media_encrypted_assets e
			JOIN media m ON m.id = e.media_id
			WHERE e.status = 'encrypted'
			  AND m.file_type = 'video'
			  AND TRIM(COALESCE(e.plain_path, '')) != ''
		`)
		if err != nil {
			log.Printf("enc pipe repair query: %v", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var mediaID int64
			var encPath, plainPath string
			if err := rows.Scan(&mediaID, &encPath, &plainPath); err != nil {
				continue
			}
			plainPath = strings.TrimSpace(plainPath)
			if plainPath != "" {
				if _, err := os.Stat(plainPath); err == nil {
					continue
				}
			}
			if !isISOBaseMedia(encPath) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
			err := enc.RepackEncryptedMP4ForPipe(ctx, mediaID)
			cancel()
			if err != nil {
				log.Printf("enc pipe repair media=%d: %v", mediaID, err)
				continue
			}
			markKeyframeReindex(enc.DB, mediaID)
			log.Printf("enc pipe repair media=%d: ok", mediaID)
		}
	}()
}
