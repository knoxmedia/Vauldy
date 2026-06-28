package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/pkg/hashutil"
)

var ErrAlreadyEncrypted = errors.New("media already encrypted")

// AssetEncryptor encrypts library media to Knox 9527 .enc files at rest.
type AssetEncryptor struct {
	DB          *sql.DB
	Vault       *keystore.Vault
	BasePath    string // legacy default; prefer ResolveEncBase per library
	DataDir     string
	FFmpegPath  string
	FFprobePath string
}

// IsMediaEncrypted reports whether the media item is already stored as an encrypted asset.
func IsMediaEncrypted(db *sql.DB, mediaID int64, filePath string) bool {
	if db == nil || mediaID <= 0 {
		return false
	}
	if kcrypto.IsEncFile(strings.TrimSpace(filePath)) {
		return true
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(1) FROM media_encrypted_assets WHERE media_id = ? AND status = 'encrypted'`, mediaID).Scan(&n)
	return n > 0
}

// EncryptMedia encrypts the file at plainPath for mediaID when the library has encrypted_assets_enabled.
func (s *AssetEncryptor) EncryptMedia(ctx context.Context, mediaID int64) error {
	return s.encryptMedia(ctx, mediaID, false)
}

// EncryptMediaManual encrypts a single media item on demand (ignores library encrypted_assets_enabled).
func (s *AssetEncryptor) EncryptMediaManual(ctx context.Context, mediaID int64) error {
	return s.encryptMedia(ctx, mediaID, true)
}

func (s *AssetEncryptor) encryptMedia(ctx context.Context, mediaID int64, manual bool) error {
	if s == nil || s.DB == nil || s.Vault == nil {
		if manual {
			return errors.New("encrypted assets not configured")
		}
		return nil
	}
	if !tryAcquireEncrypt(mediaID) {
		return nil
	}
	defer releaseEncrypt(mediaID)

	var libraryID sql.NullInt64
	var filePath, fileType, fileID string
	if err := s.DB.QueryRowContext(ctx, `
		SELECT library_id, file_path, COALESCE(file_type,''), COALESCE(file_id,'')
		FROM media WHERE id = ?
	`, mediaID).Scan(&libraryID, &filePath, &fileType, &fileID); err != nil {
		return err
	}
	if !libraryID.Valid || libraryID.Int64 <= 0 {
		if manual {
			return errors.New("media has no library")
		}
		return nil
	}
	var encLib int
	var cleanupPlain int
	if err := s.DB.QueryRowContext(ctx, `
		SELECT COALESCE(encrypted_assets_enabled,0), COALESCE(encrypted_assets_cleanup_plaintext,0)
		FROM library WHERE id = ?
	`, libraryID.Int64).Scan(&encLib, &cleanupPlain); err != nil {
		return err
	}
	if !manual && encLib != 1 {
		return nil
	}
	plainPath := strings.TrimSpace(filePath)
	if plainPath == "" {
		if manual {
			return errors.New("empty file path")
		}
		return nil
	}
	if IsMediaEncrypted(s.DB, mediaID, plainPath) {
		if manual {
			return ErrAlreadyEncrypted
		}
		return nil
	}
	if _, err := os.Stat(plainPath); err != nil {
		if os.IsNotExist(err) {
			markEncryptPlainMissing(s.DB, mediaID, plainPath)
		}
		return fmt.Errorf("plain file missing: %w", err)
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

	ft := fileType
	if ft == "" {
		ft = "document"
	}
	if fileID == "" {
		fileID = fmt.Sprintf("media-%d", mediaID)
	}
	encBase, err := s.ResolveEncBase(ctx, libraryID.Int64, plainPath)
	if err != nil {
		return err
	}
	encDir := filepath.Join(encBase, ft)
	if err := os.MkdirAll(encDir, 0o700); err != nil {
		return err
	}
	encPath := filepath.Join(encDir, fileID+".enc")

	requireFaststart := cleanupPlain == 1 || encryptRequiresISOFaststart(ft, plainPath)
	encryptSource, prepCleanup, remuxed, err := s.resolveEncryptSource(ctx, mediaID, plainPath, requireFaststart)
	if err != nil {
		return err
	}
	defer prepCleanup()

	src, err := os.Open(encryptSource)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(encPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Another encrypt goroutine is writing the canonical path; do not allocate -1/-2 copies.
			return nil
		}
		return err
	}
	result, err := kcrypto.EncryptFile(src, dst, kek)
	closeErr := dst.Close()
	if err != nil {
		_ = os.Remove(encPath)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(encPath)
		return closeErr
	}

	wrappedHex := hex.EncodeToString(result.WrappedDEK)
	ivHex := hex.EncodeToString(result.IV)
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status, updated_at)
		VALUES (?, ?, ?, ?, ?, 'encrypted', CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
		  enc_path = excluded.enc_path,
		  wrapped_dek = excluded.wrapped_dek,
		  iv = excluded.iv,
		  plain_path = excluded.plain_path,
		  status = 'encrypted',
		  updated_at = CURRENT_TIMESTAMP
	`, mediaID, encPath, wrappedHex, ivHex, plainPath)
	if err != nil {
		_ = os.Remove(encPath)
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE media SET file_path = ? WHERE id = ?`, encPath, mediaID); err != nil {
		return err
	}
	persistPlainMD5AfterEncrypt(s.DB, mediaID, plainPath)
	if cleanupPlain == 1 {
		cleanupPlaintextAfterEncrypt(s.DB, mediaID, plainPath)
	}
	if remuxed && ft == "video" {
		markKeyframeReindex(s.DB, mediaID)
	}
	return nil
}

// OpenPlaintext returns a reader for media content, decrypting .enc when needed.
func OpenPlaintext(db *sql.DB, vault *keystore.Vault, mediaID int64, path string) (io.ReadCloser, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}
	if !kcrypto.IsEncFile(path) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	if db == nil || vault == nil {
		return nil, fmt.Errorf("encrypted asset requires keystore")
	}
	var wrappedHex, ivHex string
	err := db.QueryRow(`
		SELECT wrapped_dek, iv FROM media_encrypted_assets WHERE media_id = ? AND status = 'encrypted'
	`, mediaID).Scan(&wrappedHex, &ivHex)
	if err != nil {
		return nil, err
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
	return kcrypto.OpenDecryptSeeker(path, wrapped, kek)
}

func persistPlainMD5AfterEncrypt(db *sql.DB, mediaID int64, plainPath string) {
	plainPath = strings.TrimSpace(plainPath)
	if db == nil || mediaID <= 0 || plainPath == "" {
		return
	}
	var existing sql.NullString
	if err := db.QueryRow(`SELECT md5 FROM media WHERE id = ?`, mediaID).Scan(&existing); err != nil {
		return
	}
	if existing.Valid && strings.TrimSpace(existing.String) != "" {
		return
	}
	h, err := hashutil.MD5File(plainPath)
	if err != nil || h == "" {
		return
	}
	_, _ = db.Exec(`UPDATE media SET md5 = ? WHERE id = ? AND (md5 IS NULL OR trim(md5) = '')`, h, mediaID)
}

func markEncryptPlainMissing(db *sql.DB, mediaID int64, plainPath string) {
	if db == nil || mediaID <= 0 || strings.TrimSpace(plainPath) == "" {
		return
	}
	_, _ = db.Exec(`
		INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status, updated_at)
		VALUES (?, ?, '00', '00', ?, 'plain_missing', CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
		  enc_path = excluded.enc_path,
		  plain_path = excluded.plain_path,
		  status = 'plain_missing',
		  updated_at = CURRENT_TIMESTAMP
	`, mediaID, plainPath, plainPath)
}
