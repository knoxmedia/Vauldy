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
	// ResumeCheckpointBytes overrides EncryptResumeCheckpointBytes when > 0 (tests).
	ResumeCheckpointBytes int64
	onFlightJoined        func(mediaID int64)
	syncStagedFile        func(*os.File) error
	// onEncryptCheckpoint is invoked after each durable resume checkpoint (tests).
	onEncryptCheckpoint func(ctx context.Context, plainOffset int64)
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
func (s *AssetEncryptor) encryptMediaLegacyPublic(ctx context.Context, mediaID int64) error {
	return s.encryptMedia(ctx, mediaID, false)
}

// EncryptMediaManual encrypts a single media item on demand (ignores library encrypted_assets_enabled).
func (s *AssetEncryptor) encryptMediaManualLegacy(ctx context.Context, mediaID int64) error {
	return s.encryptMedia(ctx, mediaID, true)
}

func (s *AssetEncryptor) encryptMedia(ctx context.Context, mediaID int64, manual bool) (err error) {
	if s == nil || s.DB == nil || s.Vault == nil {
		if manual {
			return errors.New("encrypted assets not configured")
		}
		return nil
	}
	leader, flight := acquireEncryptFlightFor(mediaID, "media")
	if !leader {
		if s.onFlightJoined != nil {
			s.onFlightJoined(mediaID)
		}
		if waitErr := waitEncryptFlight(ctx, flight); waitErr != nil {
			return waitErr
		}
		if flight.operation == "stage" {
			return s.encryptMedia(ctx, mediaID, manual)
		}
		return nil
	}
	defer func() { finishEncryptFlight(mediaID, flight, err) }()

	if err := EnsureEncryptResumeSchema(s.DB); err != nil {
		return err
	}

	var libraryID sql.NullInt64
	var generation int64
	var filePath, fileType, fileID string
	if err := s.DB.QueryRowContext(ctx, `
		SELECT library_id, file_path, COALESCE(file_type,''), COALESCE(file_id,''), COALESCE(ingest_generation,0)
		FROM media WHERE id = ?
	`, mediaID).Scan(&libraryID, &filePath, &fileType, &fileID, &generation); err != nil {
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
	if cleanupPlain == 1 {
		if _, err := lookupEncryptionRetirementIdentity(ctx, s.DB, mediaID, generation); err != nil {
			return err
		}
	}
	plainPath := strings.TrimSpace(filePath)
	if plainPath == "" {
		if manual {
			return errors.New("empty file path")
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
	valid, err := encryptedCanonicalRecordValid(ctx, s.DB, mediaID, encPath)
	if err != nil {
		return err
	}
	if valid {
		return ErrAlreadyEncrypted
	}

	identity, err := QuickSourceIdentity(plainPath)
	if err != nil {
		return err
	}
	requireFaststart := cleanupPlain == 1 || encryptRequiresISOFaststart(ft, plainPath)
	encryptSource, prepCleanup, remuxed, err := s.resolveEncryptSource(ctx, mediaID, plainPath, requireFaststart)
	if err != nil {
		return err
	}
	defer prepCleanup()
	sourceInfo, err := os.Stat(encryptSource)
	if err != nil {
		return err
	}
	backupPath := fmt.Sprintf("%s.orphan-resume-%d", encPath, generation)
	output, err := s.encryptToPathResumable(
		ctx, mediaID, generation, plainPath, encryptSource, identity, sourceInfo.Size(), kek, encPath, backupPath,
		func() (resumableEncryptTarget, error) {
			if _, statErr := os.Stat(encPath); statErr == nil {
				if _, backupErr := os.Stat(backupPath); backupErr == nil {
					if removeErr := os.Remove(encPath); removeErr != nil {
						return resumableEncryptTarget{}, removeErr
					}
				} else if os.IsNotExist(backupErr) {
					if renameErr := os.Rename(encPath, backupPath); renameErr != nil {
						return resumableEncryptTarget{}, renameErr
					}
				} else {
					return resumableEncryptTarget{}, backupErr
				}
			} else if !os.IsNotExist(statErr) {
				return resumableEncryptTarget{}, statErr
			}
			return resumableEncryptTarget{
				StageID:    "manual-" + fmt.Sprint(generation),
				EncPath:    encPath,
				BackupPath: backupPath,
			}, nil
		},
	)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			err = restoreEncryptedOutput(err, output.EncPath, output.BackupPath, os.Remove, os.Rename)
		}
	}()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := runEncryptCommitGuard(ctx, tx); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO media_encrypted_assets (media_id, enc_path, wrapped_dek, iv, plain_path, status, updated_at)
		VALUES (?, ?, ?, ?, ?, 'encrypted', CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
		  enc_path = excluded.enc_path,
		  wrapped_dek = excluded.wrapped_dek,
		  iv = excluded.iv,
		  plain_path = excluded.plain_path,
		  status = 'encrypted',
		  updated_at = CURRENT_TIMESTAMP
	`, mediaID, output.EncPath, output.WrappedDEK, output.IV, plainPath)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media SET file_path = ? WHERE id = ?`, output.EncPath, mediaID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE media_encrypt_resume
		SET state = 'abandoned', updated_at = CURRENT_TIMESTAMP
		WHERE media_id = ? AND generation = ?
	`, mediaID, generation); err != nil {
		return err
	}
	if err := upsertManualEncryptRetirementTx(ctx, tx, mediaID, generation, plainPath, output, cleanupPlain == 1); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	published = true
	if output.BackupPath != "" {
		_ = os.Remove(output.BackupPath)
	}
	persistPlainMD5AfterEncrypt(s.DB, mediaID, plainPath)
	// Authoritative plaintext deletion is owned by retirement; never os.Remove the source here.
	if remuxed && ft == "video" {
		markKeyframeReindex(s.DB, mediaID)
	}
	return nil
}

// ValidEncryptedFile validates a Knox encrypted file header and non-empty payload.
func ValidEncryptedFile(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, nil
	}
	plainSize, err := kcrypto.PlaintextSize(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, nil
	}
	return plainSize > 0, nil
}

// IsEncryptedAssetRecordValid validates the encrypted DB record and its file.
func IsEncryptedAssetRecordValid(ctx context.Context, db *sql.DB, mediaID int64) (bool, error) {
	if db == nil {
		return false, errors.New("encrypted asset validator: database is not configured")
	}
	if mediaID <= 0 {
		return false, nil
	}
	var path string
	err := db.QueryRowContext(ctx, `SELECT enc_path FROM media_encrypted_assets WHERE media_id=? AND status='encrypted'`, mediaID).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ValidEncryptedFile(path)
}

func encryptedCanonicalRecordValid(ctx context.Context, db *sql.DB, mediaID int64, canonicalPath string) (bool, error) {
	var path string
	err := db.QueryRowContext(ctx, `SELECT enc_path FROM media_encrypted_assets WHERE media_id=? AND status='encrypted'`, mediaID).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(path) != strings.TrimSpace(canonicalPath) {
		return false, nil
	}
	return ValidEncryptedFile(path)
}

func restoreEncryptedOutput(original error, newPath, backupPath string, remove func(string) error, rename func(string, string) error) error {
	var restoreErr error
	if err := remove(newPath); err != nil && !os.IsNotExist(err) {
		restoreErr = errors.Join(restoreErr, err)
	}
	if backupPath != "" {
		if err := rename(backupPath, newPath); err != nil {
			restoreErr = errors.Join(restoreErr, err)
		}
	}
	return errors.Join(original, restoreErr)
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

// EncryptionDB exposes the encryptor database to the post-ingest adapter for
// idempotency checks and lease fencing.
func (s *AssetEncryptor) EncryptionDB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.DB
}
