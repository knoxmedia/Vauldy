package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	kcrypto "knox-media/internal/crypto"
)

type StagedMediaEncryption struct {
	MediaID           int64
	StageID           string
	OriginalPath      string
	SourceFingerprint string
	EncPath           string
	WrappedDEK        string
	IV                string
	SHA256            string
	Size              int64
	CleanupPlaintext  bool
}

func (s *AssetEncryptor) StageMediaEncryption(ctx context.Context, mediaID int64) (stage StagedMediaEncryption, err error) {
	if s == nil || s.DB == nil || s.Vault == nil {
		return stage, errors.New("encrypted assets not configured")
	}
	var libraryID int64
	var source, fileType, fileID string
	var cleanup int
	err = s.DB.QueryRowContext(ctx, `SELECT m.library_id,m.file_path,COALESCE(m.file_type,''),COALESCE(m.file_id,''),COALESCE(l.encrypted_assets_cleanup_plaintext,0) FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(&libraryID, &source, &fileType, &fileID, &cleanup)
	if err != nil {
		return stage, err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return stage, errors.New("empty file path")
	}
	fp, err := EncryptionSourceFingerprint(source)
	if err != nil {
		return stage, fmt.Errorf("plain file missing: %w", err)
	}
	kek, err := s.Vault.GetKEK(ctx)
	if err != nil {
		return stage, err
	}
	defer func() {
		for i := range kek {
			kek[i] = 0
		}
	}()
	if fileType == "" {
		fileType = "document"
	}
	if fileID == "" {
		fileID = fmt.Sprintf("media-%d", mediaID)
	}
	base, err := s.ResolveEncBase(ctx, libraryID, source)
	if err != nil {
		return stage, err
	}
	stageID := uuid.NewString()
	dir := filepath.Join(base, fileType, "stages", stageID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return stage, err
	}
	encPath := filepath.Join(dir, fileID+".enc")
	src, err := os.Open(source)
	if err != nil {
		return stage, err
	}
	defer src.Close()
	dst, err := os.OpenFile(encPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return stage, err
	}
	result, cryptErr := kcrypto.EncryptFileContext(ctx, src, dst, kek)
	closeErr := dst.Close()
	if cryptErr != nil {
		_ = os.Remove(encPath)
		return stage, cryptErr
	}
	if closeErr != nil {
		_ = os.Remove(encPath)
		return stage, closeErr
	}
	size, hash, err := EncryptionPathHash(encPath)
	if err != nil {
		_ = os.Remove(encPath)
		return stage, err
	}
	return StagedMediaEncryption{MediaID: mediaID, StageID: stageID, OriginalPath: source, SourceFingerprint: fp, EncPath: encPath, WrappedDEK: hex.EncodeToString(result.WrappedDEK), IV: hex.EncodeToString(result.IV), SHA256: hash, Size: size, CleanupPlaintext: cleanup == 1}, nil
}

func EncryptionSourceFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%d|%d|sha256:%s", filepath.Clean(abs), info.Size(), info.ModTime().UnixNano(), hex.EncodeToString(h.Sum(nil))), nil
}
func EncryptionPathHash(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

func (s *AssetEncryptor) EncryptionPrivateRoot() string {
	if s == nil {
		return ""
	}
	if strings.TrimSpace(s.DataDir) != "" {
		return filepath.Join(s.DataDir, ".quarantine", "encryption")
	}
	if strings.TrimSpace(s.BasePath) != "" {
		return filepath.Join(s.BasePath, ".quarantine", "encryption")
	}
	return ""
}

func (s *AssetEncryptor) EncryptMedia(ctx context.Context, mediaID int64) (err error) {
	leader, flight := acquireEncryptFlight(mediaID)
	if !leader {
		return waitEncryptFlight(ctx, flight)
	}
	defer func() { finishEncryptFlight(mediaID, flight, err) }()
	return s.commitStagedCompatibility(ctx, mediaID, false)
}
func (s *AssetEncryptor) EncryptMediaManual(ctx context.Context, mediaID int64) error {
	return s.commitStagedCompatibility(ctx, mediaID, true)
}
func (s *AssetEncryptor) commitStagedCompatibility(ctx context.Context, mediaID int64, manual bool) error {
	if s == nil || s.DB == nil || s.Vault == nil {
		if manual {
			return errors.New("encrypted assets not configured")
		}
		return nil
	}
	if !manual {
		var enabled int
		if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(l.encrypted_assets_enabled,0) FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(&enabled); err != nil {
			return err
		}
		if enabled != 1 {
			return nil
		}
	}
	var selected, existing string
	err := s.DB.QueryRowContext(ctx, `SELECT m.file_path,COALESCE(a.enc_path,'') FROM media m LEFT JOIN media_encrypted_assets a ON a.media_id=m.id AND a.status='encrypted' WHERE m.id=?`, mediaID).Scan(&selected, &existing)
	if err != nil {
		return err
	}
	if existing != "" && sameEncryptedPath(selected, existing) {
		ok, _ := ValidEncryptedFile(existing)
		if ok {
			return ErrAlreadyEncrypted
		}
	}
	stage, err := s.StageMediaEncryption(ctx, mediaID)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = runEncryptCommitGuard(ctx, tx); err != nil {
		_ = os.Remove(stage.EncPath)
		return err
	}
	if err = CommitStagedMediaEncryptionTx(ctx, tx, stage); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	persistPlainMD5AfterEncrypt(s.DB, mediaID, stage.OriginalPath)
	if stage.CleanupPlaintext {
		cleanupPlaintextAfterEncrypt(s.DB, mediaID, stage.OriginalPath)
	}
	return nil
}
func CommitStagedMediaEncryptionTx(ctx context.Context, tx *sql.Tx, stage StagedMediaEncryption) error {
	return commitStagedEncryptionSQL(ctx, tx, stage)
}

type encryptionSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func commitStagedEncryptionSQL(ctx context.Context, tx encryptionSQL, stage StagedMediaEncryption) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_encrypted_assets(media_id,enc_path,wrapped_dek,iv,plain_path,status,updated_at) VALUES(?,?,?,?,?,'encrypted',CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO UPDATE SET enc_path=excluded.enc_path,wrapped_dek=excluded.wrapped_dek,iv=excluded.iv,plain_path=excluded.plain_path,status='encrypted',updated_at=CURRENT_TIMESTAMP`, stage.MediaID, stage.EncPath, stage.WrappedDEK, stage.IV, stage.OriginalPath); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE media SET file_path=? WHERE id=? AND file_path=?`, stage.EncPath, stage.MediaID, stage.OriginalPath)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("encryption source selection changed")
	}
	return nil
}
func sameEncryptedPath(a, b string) bool {
	aa, ea := filepath.Abs(a)
	bb, eb := filepath.Abs(b)
	return ea == nil && eb == nil && strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}
