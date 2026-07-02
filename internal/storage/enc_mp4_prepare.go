package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
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

func isoFormatToken(format string) bool {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), ".")) {
	case "mp4", "m4v", "mov", "3gp", "3g2":
		return true
	default:
		return false
	}
}

func isKnoxEncCatalogPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if kcrypto.IsEncFile(path) {
		return true
	}
	return strings.EqualFold(filepath.Ext(path), ".enc")
}

// isISOBaseMediaCatalog resolves ISO-BMFF for Knox catalog paths (.enc) via plain_path or media.format.
func isISOBaseMediaCatalog(db *sql.DB, mediaID int64, catalogPath string) bool {
	if isISOBaseMedia(catalogPath) {
		return true
	}
	if !isKnoxEncCatalogPath(catalogPath) || db == nil || mediaID <= 0 {
		return false
	}
	var plainPath, format sql.NullString
	_ = db.QueryRow(`
		SELECT COALESCE(e.plain_path,''), COALESCE(m.format,'')
		FROM media_encrypted_assets e
		JOIN media m ON m.id = e.media_id
		WHERE e.media_id = ? AND e.status = 'encrypted'
	`, mediaID).Scan(&plainPath, &format)
	if isISOBaseMedia(plainPath.String) {
		return true
	}
	return isoFormatToken(format.String)
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

// encryptRequiresISOFaststart reports whether encrypted video ISO-BMFF must be faststart-remuxed
// before envelope encryption so JIT/ffmpeg can demux via decrypt pipe (no plaintext on disk).
func encryptRequiresISOFaststart(fileType, plainPath string) bool {
	return strings.EqualFold(strings.TrimSpace(fileType), "video") && isISOBaseMedia(plainPath)
}

// resolveEncryptSource picks the byte stream to envelope-encrypt. When requireFaststart is true,
// MP4/MOV inputs must be faststart-remuxed first (video encrypted assets always require this).
func (s *AssetEncryptor) resolveEncryptSource(ctx context.Context, mediaID int64, plainPath string, requireFaststart bool) (string, func(), bool, error) {
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
	if requireFaststart {
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
		  status = CASE WHEN keyframe_task.status = 'failed' THEN keyframe_task.status ELSE 'waiting' END,
		  error_message = CASE WHEN keyframe_task.status = 'failed' THEN keyframe_task.error_message ELSE NULL END,
		  updated_at = CURRENT_TIMESTAMP
	`, mediaID)
}

func encryptedPipeDemuxOK(db *sql.DB, vault *keystore.Vault, ffprobePath string, mediaID int64, encPath string) bool {
	if db == nil || vault == nil || mediaID <= 0 || !kcrypto.IsEncFile(encPath) {
		return false
	}
	if plain := ResolveKeyframeProbePath(db, mediaID, encPath); plain != encPath {
		if _, err := os.Stat(plain); err == nil {
			return true
		}
	}
	if encryptedISOMoovBeforeMDAT(db, vault, mediaID, encPath) {
		return true
	}
	ffprobePath = strings.TrimSpace(ffprobePath)
	if ffprobePath == "" {
		return false
	}
	mp, err := ProbeMediaFile(db, vault, ffprobePath, mediaID, encPath, []string{
		"-analyzeduration", "5000000",
		"-probesize", "33554432",
	})
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
		if errors.Is(err, sql.ErrNoRows) {
			// No at-rest encrypted asset for this media — nothing to repack.
			return nil
		}
		return err
	}
	encPath = strings.TrimSpace(encPath)
	if !isISOBaseMediaCatalog(s.DB, mediaID, encPath) {
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

	if ok, layoutErr := isoBMFFMoovBeforeMDATFile(fastPath); layoutErr != nil {
		return layoutErr
	} else if !ok {
		return fmt.Errorf("mp4 faststart remux did not place moov before mdat")
	}

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

// EnsureEncryptedISOPipePlayback verifies Knox .enc ISO-BMFF can be demuxed from decrypt pipe.
// Legacy moov-at-end assets are faststart-repacked in place (ciphertext only on disk afterward).
func (s *AssetEncryptor) EnsureEncryptedISOPipePlayback(ctx context.Context, mediaID int64, catalogPath string) error {
	if s == nil || s.DB == nil || s.Vault == nil || mediaID <= 0 {
		return nil
	}
	catalogPath = strings.TrimSpace(catalogPath)
	// Only applies to media that has a Knox .enc asset at rest. Plaintext media (including
	// stream-DRM JIT paths where encryption happens at playback time) has nothing to repack;
	// without this guard, a plaintext .mp4 path would trip isISOBaseMediaCatalog via the
	// extension alone and RepackEncryptedMP4ForPipe would fail with sql.ErrNoRows.
	if !IsMediaEncrypted(s.DB, mediaID, catalogPath) {
		return nil
	}
	if !isISOBaseMediaCatalog(s.DB, mediaID, catalogPath) {
		return nil
	}
	if encryptedPipeDemuxOK(s.DB, s.Vault, s.FFprobePath, mediaID, catalogPath) {
		return nil
	}
	if !tryAcquireRepack(mediaID) {
		return waitEncryptedPipeReady(ctx, s, mediaID, catalogPath)
	}
	defer releaseRepack(mediaID)

	if err := s.RepackEncryptedMP4ForPipe(ctx, mediaID); err != nil {
		return err
	}
	markKeyframeReindex(s.DB, mediaID)
	if !encryptedPipeDemuxOK(s.DB, s.Vault, s.FFprobePath, mediaID, catalogPath) {
		return fmt.Errorf("encrypted mp4 still not pipe-ready after repack")
	}
	return nil
}

func waitEncryptedPipeReady(ctx context.Context, s *AssetEncryptor, mediaID int64, catalogPath string) error {
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	for {
		if encryptedPipeDemuxOK(s.DB, s.Vault, s.FFprobePath, mediaID, catalogPath) {
			return nil
		}
		if !repackInFlight(mediaID) {
			return fmt.Errorf("encrypted mp4 pipe playback not ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tk.C:
		}
	}
}

// KickEncryptedMP4PipeRepairs rewrites legacy moov-at-end encrypted MP4s in the background.
func KickEncryptedMP4PipeRepairs(enc *AssetEncryptor) {
	if enc == nil || enc.DB == nil || strings.TrimSpace(enc.FFmpegPath) == "" {
		return
	}
	go func() {
		rows, err := enc.DB.Query(`
			SELECT e.media_id, e.enc_path
			FROM media_encrypted_assets e
			JOIN media m ON m.id = e.media_id
			WHERE e.status = 'encrypted'
			  AND m.file_type = 'video'
		`)
		if err != nil {
			log.Printf("enc pipe repair query: %v", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var mediaID int64
			var encPath string
			if err := rows.Scan(&mediaID, &encPath); err != nil {
				continue
			}
			if !isISOBaseMediaCatalog(enc.DB, mediaID, encPath) {
				continue
			}
			if encryptedPipeDemuxOK(enc.DB, enc.Vault, enc.FFprobePath, mediaID, encPath) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
			err := enc.EnsureEncryptedISOPipePlayback(ctx, mediaID, encPath)
			cancel()
			if err != nil {
				log.Printf("enc pipe repair media=%d: %v", mediaID, err)
				continue
			}
			log.Printf("enc pipe repair media=%d: ok", mediaID)
		}
	}()
}
