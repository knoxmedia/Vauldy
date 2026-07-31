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
	"knox-media/internal/publication"
)

type StagedMediaEncryption struct {
	MediaID           int64
	StageID           string
	OriginalPath      string
	SourceIdentity    string
	SourceFingerprint string
	EncPath           string
	WrappedDEK        string
	IV                string
	SHA256            string
	Size              int64
	CleanupPlaintext  bool
}

func (s *AssetEncryptor) resumeCheckpointBytes() int64 {
	if s != nil && s.ResumeCheckpointBytes > 0 {
		return s.ResumeCheckpointBytes
	}
	return EncryptResumeCheckpointBytes
}

type resumableEncryptOutput struct {
	StageID    string
	EncPath    string
	WrappedDEK string
	IV         string
	BackupPath string
	SHA256     string
	Size       int64
}

type resumableEncryptTarget struct {
	StageID    string
	EncPath    string
	BackupPath string
}

func (s *AssetEncryptor) encryptToPathResumable(
	ctx context.Context,
	mediaID, generation int64,
	source, encryptSource, identity string,
	plainSize int64,
	kek []byte,
	intendedEncPath string,
	backupPath string,
	freshTarget func() (resumableEncryptTarget, error),
) (out resumableEncryptOutput, err error) {
	out.BackupPath = backupPath
	var (
		plainOffset   int64
		session       *kcrypto.EncryptResumeSession
		resuming      bool
		hadCheckpoint bool
	)

	prev, loadErr := LoadEncryptResume(ctx, s.DB, mediaID, generation)
	if loadErr == nil &&
		(prev.State == "encrypting" || prev.State == "staged") &&
		prev.SourceIdentity == identity &&
		prev.EncPath != "" &&
		sameEncryptedPath(prev.EncPath, intendedEncPath) {
		resumeOffset := prev.PlainOffset
		if resumeOffset < 0 || resumeOffset > plainSize {
			resumeOffset = 0
		}
		wantEnc := int64(kcrypto.EncHeaderSize) + resumeOffset
		if st, stErr := os.Stat(prev.EncPath); stErr == nil && st.Size() >= wantEnc {
			wrappedRaw, wErr := hex.DecodeString(prev.WrappedDEK)
			ivRaw, iErr := hex.DecodeString(prev.IV)
			if wErr == nil && iErr == nil {
				session, err = kcrypto.RestoreEncryptResume(kek, wrappedRaw, ivRaw)
				if err == nil {
					out.StageID = prev.StageID
					out.EncPath = prev.EncPath
					out.WrappedDEK = prev.WrappedDEK
					out.IV = prev.IV
					plainOffset = resumeOffset
					resuming = true
					hadCheckpoint = prev.PlainOffset > 0 || prev.EncBytesWritten > 0 || prev.State == "staged"
				}
			}
		}
	}
	if loadErr == nil && !resuming && prev.State != "abandoned" {
		_ = AbandonEncryptResume(ctx, s.DB, mediaID, generation)
	}

	if !resuming {
		session, err = kcrypto.BeginEncryptResume(kek)
		if err != nil {
			return out, err
		}
		target, targetErr := freshTarget()
		if targetErr != nil {
			return out, targetErr
		}
		out.StageID = target.StageID
		out.EncPath = target.EncPath
		if target.BackupPath != "" {
			out.BackupPath = target.BackupPath
		}
		result := session.Result()
		out.WrappedDEK = hex.EncodeToString(result.WrappedDEK)
		out.IV = hex.EncodeToString(result.IV)
	}

	src, err := os.Open(encryptSource)
	if err != nil {
		return out, err
	}
	defer src.Close()

	var dst *os.File
	if resuming {
		dst, err = os.OpenFile(out.EncPath, os.O_RDWR, 0o600)
		if err != nil {
			return out, err
		}
		wantEnc := int64(kcrypto.EncHeaderSize) + plainOffset
		st, stErr := dst.Stat()
		if stErr != nil {
			_ = dst.Close()
			return out, stErr
		}
		if st.Size() > wantEnc {
			if err = dst.Truncate(wantEnc); err != nil {
				_ = dst.Close()
				return out, err
			}
		}
		if _, err = dst.Seek(0, io.SeekStart); err != nil {
			_ = dst.Close()
			return out, err
		}
		if err = session.HashPrefix(dst, wantEnc); err != nil {
			_ = dst.Close()
			return out, err
		}
		if _, err = dst.Seek(wantEnc, io.SeekStart); err != nil {
			_ = dst.Close()
			return out, err
		}
	} else {
		dst, err = os.OpenFile(out.EncPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			if out.BackupPath != "" {
				_ = os.Rename(out.BackupPath, out.EncPath)
			}
			return out, err
		}
		if err = session.WriteHeader(dst); err != nil {
			_ = dst.Close()
			_ = os.Remove(out.EncPath)
			if out.BackupPath != "" {
				_ = os.Rename(out.BackupPath, out.EncPath)
			}
			return out, err
		}
	}

	discardUndurable := func() {
		if hadCheckpoint {
			return
		}
		_ = os.Remove(out.EncPath)
		if out.BackupPath != "" {
			_ = os.Rename(out.BackupPath, out.EncPath)
		}
	}
	syncDst := func() error {
		if s.syncStagedFile != nil {
			return s.syncStagedFile(dst)
		}
		return dst.Sync()
	}
	upsertProgress := func(offset int64, state string) error {
		return UpsertEncryptResume(context.WithoutCancel(ctx), s.DB, EncryptResumeRow{
			MediaID: mediaID, Generation: generation, StageID: out.StageID,
			EncPath: out.EncPath, SourcePath: source, SourceIdentity: identity,
			WrappedDEK: out.WrappedDEK, IV: out.IV, PlainOffset: offset,
			EncBytesWritten: offset, State: state,
		})
	}
	checkpointAndUpsert := func(offset int64, state string) error {
		if err := syncDst(); err != nil {
			return err
		}
		return upsertProgress(offset, state)
	}

	if _, err = src.Seek(plainOffset, io.SeekStart); err != nil {
		_ = dst.Close()
		discardUndurable()
		return out, err
	}

	dirty := false
	checkpoint := s.resumeCheckpointBytes()
	for plainOffset < plainSize {
		if err := ctx.Err(); err != nil {
			if dirty {
				_ = syncDst()
			}
			_ = upsertProgress(plainOffset, "encrypting")
			_ = dst.Close()
			discardUndurable()
			return out, err
		}
		chunk := checkpoint
		if remaining := plainSize - plainOffset; chunk > remaining {
			chunk = remaining
		}
		if err = session.EncryptRange(ctx, src, dst, plainOffset, chunk); err != nil {
			_ = syncDst()
			_ = upsertProgress(plainOffset, "encrypting")
			_ = dst.Close()
			discardUndurable()
			return out, err
		}
		dirty = true
		plainOffset += chunk
		if plainOffset == plainSize {
			break
		}
		if err = checkpointAndUpsert(plainOffset, "encrypting"); err != nil {
			_ = dst.Close()
			discardUndurable()
			return out, err
		}
		dirty = false
		hadCheckpoint = true
		if s.onEncryptCheckpoint != nil {
			s.onEncryptCheckpoint(ctx, plainOffset)
		}
	}

	syncErr := syncDst()
	closeErr := dst.Close()
	if syncErr != nil || closeErr != nil {
		_ = os.Remove(out.EncPath)
		if out.BackupPath != "" {
			_ = os.Rename(out.BackupPath, out.EncPath)
		}
		return out, errors.Join(syncErr, closeErr)
	}
	if err := upsertProgress(plainOffset, "staged"); err != nil {
		_ = os.Remove(out.EncPath)
		if out.BackupPath != "" {
			_ = os.Rename(out.BackupPath, out.EncPath)
		}
		return out, err
	}
	if out.BackupPath != "" {
		if _, statErr := os.Stat(out.BackupPath); os.IsNotExist(statErr) {
			out.BackupPath = ""
		}
	}
	out.Size = int64(kcrypto.EncHeaderSize) + plainSize
	out.SHA256 = hex.EncodeToString(session.Sum())
	return out, nil
}

func (s *AssetEncryptor) StageMediaEncryption(ctx context.Context, mediaID int64) (stage StagedMediaEncryption, err error) {
	if s == nil || s.DB == nil || s.Vault == nil {
		return stage, errors.New("encrypted assets not configured")
	}
	leader, flight := acquireEncryptFlightFor(mediaID, "stage")
	if !leader {
		if s.onFlightJoined != nil {
			s.onFlightJoined(mediaID)
		}
		if waitErr := waitEncryptFlight(ctx, flight); waitErr != nil {
			return stage, waitErr
		}
		if flight.operation == "stage" {
			return flight.stage, nil
		}
		if IsMediaEncrypted(s.DB, mediaID, "") {
			return stage, ErrAlreadyEncrypted
		}
		return s.StageMediaEncryption(ctx, mediaID)
	}
	defer func() {
		flight.stage = stage
		finishEncryptFlight(mediaID, flight, err)
	}()

	if err := EnsureEncryptResumeSchema(s.DB); err != nil {
		return stage, err
	}

	var libraryID, generation int64
	var source, fileType, fileID string
	var cleanup int
	err = s.DB.QueryRowContext(ctx, `SELECT m.library_id,m.file_path,COALESCE(m.file_type,''),COALESCE(m.file_id,''),COALESCE(l.encrypted_assets_cleanup_plaintext,0),COALESCE(m.ingest_generation,0) FROM media m JOIN library l ON l.id=m.library_id WHERE m.id=?`, mediaID).Scan(&libraryID, &source, &fileType, &fileID, &cleanup, &generation)
	if err != nil {
		return stage, err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return stage, errors.New("empty file path")
	}
	fp, err := publication.SourceFingerprintContext(ctx, source)
	if err != nil {
		return stage, fmt.Errorf("plain file missing: %w", err)
	}
	identity, err := QuickSourceIdentity(source)
	if err != nil {
		return stage, fmt.Errorf("plain file missing: %w", err)
	}

	encryptSource := source
	prepCleanup := func() {}
	if encryptRequiresISOFaststart(fileType, source) {
		encryptSource, prepCleanup, _, err = s.resolveEncryptSource(ctx, mediaID, source, true)
		if err != nil {
			return stage, err
		}
	}
	defer prepCleanup()

	srcInfo, err := os.Stat(encryptSource)
	if err != nil {
		return stage, fmt.Errorf("plain file missing: %w", err)
	}
	plainSize := srcInfo.Size()

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
	stageDir := filepath.Join(base, fileType, "stages", stageID)
	intendedEncPath := filepath.Join(stageDir, fileID+".enc")
	reusingResumeTarget := false
	if prev, loadErr := LoadEncryptResume(ctx, s.DB, mediaID, generation); loadErr == nil {
		if _, parseErr := uuid.Parse(prev.StageID); parseErr == nil {
			prevDir := filepath.Join(base, fileType, "stages", prev.StageID)
			prevIntendedPath := filepath.Join(prevDir, fileID+".enc")
			if sameEncryptedPath(prev.EncPath, prevIntendedPath) {
				stageID = prev.StageID
				stageDir = prevDir
				intendedEncPath = prevIntendedPath
				reusingResumeTarget = true
			}
		}
	}

	output, err := s.encryptToPathResumable(ctx, mediaID, generation, source, encryptSource, identity, plainSize, kek, intendedEncPath, "", func() (resumableEncryptTarget, error) {
		if reusingResumeTarget {
			stageID = uuid.NewString()
			stageDir = filepath.Join(base, fileType, "stages", stageID)
			intendedEncPath = filepath.Join(stageDir, fileID+".enc")
		}
		if err := os.MkdirAll(stageDir, 0o700); err != nil {
			return resumableEncryptTarget{}, err
		}
		return resumableEncryptTarget{StageID: stageID, EncPath: intendedEncPath}, nil
	})
	if err != nil {
		return stage, err
	}
	return StagedMediaEncryption{
		MediaID:           mediaID,
		StageID:           output.StageID,
		OriginalPath:      source,
		SourceIdentity:    identity,
		SourceFingerprint: fp,
		EncPath:           output.EncPath,
		WrappedDEK:        output.WrappedDEK,
		IV:                output.IV,
		SHA256:            output.SHA256,
		Size:              output.Size,
		CleanupPlaintext:  cleanup == 1,
	}, nil
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

func (s *AssetEncryptor) EncryptMedia(ctx context.Context, mediaID int64) error {
	return s.encryptMediaLegacyPublic(ctx, mediaID)
}
func (s *AssetEncryptor) EncryptMediaManual(ctx context.Context, mediaID int64) error {
	return s.encryptMediaManualLegacy(ctx, mediaID)
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

// ResolveEncryptionStageRoot derives the current authoritative stage container for a media source.
func (s *AssetEncryptor) ResolveEncryptionStageRoot(ctx context.Context, mediaID int64, source string) (string, error) {
	var libraryID int64
	var fileType string
	if err := s.DB.QueryRowContext(ctx, `SELECT library_id,COALESCE(file_type,'document') FROM media WHERE id=?`, mediaID).Scan(&libraryID, &fileType); err != nil {
		return "", err
	}
	base, err := s.ResolveEncBase(ctx, libraryID, source)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, fileType, "stages"), nil
}
