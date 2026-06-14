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
	"path/filepath"
	"strings"
	"time"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
)

var ErrAlreadyEncrypted = errors.New("media already encrypted")

// AssetEncryptor encrypts library media to Knox 9527 .enc files at rest.
type AssetEncryptor struct {
	DB       *sql.DB
	Vault    *keystore.Vault
	BasePath string // legacy default; prefer ResolveEncBase per library
	DataDir  string
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

	src, err := os.Open(plainPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(encPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			encPath, err = uniqueEncPath(encDir, fileID)
			if err != nil {
				return err
			}
			dst, err = os.OpenFile(encPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		}
		if err != nil {
			return err
		}
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
	if cleanupPlain == 1 {
		if err := os.Remove(plainPath); err != nil && !os.IsNotExist(err) {
			log.Printf("asset encrypt: cleanup plain media=%d path=%s err=%v", mediaID, plainPath, err)
		}
	}
	return nil
}

func uniqueEncPath(dir, fileID string) (string, error) {
	for i := 0; i < 100; i++ {
		name := fileID
		if i > 0 {
			name = fmt.Sprintf("%s-%d", fileID, i)
		}
		p := filepath.Join(dir, name+".enc")
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p, nil
		}
	}
	return "", fmt.Errorf("could not allocate enc path for %s", fileID)
}

// WaitForPlaintextConsumers blocks until preview/package tasks finish or timeout.
func WaitForPlaintextConsumers(db *sql.DB, mediaID int64, timeout time.Duration) {
	if db == nil || mediaID <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var previewStatus, packageStatus sql.NullString
		_ = db.QueryRow(`
			SELECT (SELECT status FROM preview_task WHERE media_id = ? LIMIT 1),
			       (SELECT status FROM package_task WHERE media_id = ? ORDER BY id DESC LIMIT 1)
		`, mediaID, mediaID).Scan(&previewStatus, &packageStatus)
		busy := false
		if previewStatus.Valid {
			switch strings.ToLower(previewStatus.String) {
			case "running", "processing":
				busy = true
			}
		}
		if packageStatus.Valid {
			switch strings.ToLower(packageStatus.String) {
			case "running":
				busy = true
			}
		}
		if !busy {
			return
		}
		time.Sleep(2 * time.Second)
	}
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
