package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/store"
	"os"
	"path/filepath"
	"strings"
)

// DerivedAssetStore encrypts task-produced artifacts at rest for encrypted libraries.
type DerivedAssetStore struct {
	DB      *sql.DB
	Vault   *keystore.Vault
	BaseDir string
}

// NeedsDerivedEncryption reports whether media belongs to a library with static encryption on.
func NeedsDerivedEncryption(db *sql.DB, mediaID int64) bool {
	if db == nil || mediaID <= 0 {
		return false
	}
	var n int
	err := db.QueryRow(`
		SELECT COALESCE(l.encrypted_assets_enabled, 0)
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&n)
	return err == nil && n == 1
}

// FinalizePath encrypts plainPath when required and returns the path to store in DB.
func (s *DerivedAssetStore) FinalizePath(ctx context.Context, mediaID int64, kind, logicalName, plainPath string) (string, error) {
	if s == nil || !NeedsDerivedEncryption(s.DB, mediaID) {
		return plainPath, nil
	}
	return s.WriteFromTemp(ctx, mediaID, kind, logicalName, plainPath)
}

// FinalizeBytes writes data to an encrypted or plaintext artifact path.
func (s *DerivedAssetStore) FinalizeBytes(ctx context.Context, mediaID int64, kind, logicalName string, data []byte) (string, error) {
	if s == nil || !NeedsDerivedEncryption(s.DB, mediaID) {
		dir := filepath.Join(s.fallbackBase(mediaID), sanitizeDerivedKind(kind))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		plain := filepath.Join(dir, logicalName)
		if err := os.WriteFile(plain, data, 0o644); err != nil {
			return "", err
		}
		return plain, nil
	}
	return s.Write(ctx, mediaID, kind, logicalName, strings.NewReader(string(data)))
}

func (s *DerivedAssetStore) fallbackBase(mediaID int64) string {
	if s != nil && strings.TrimSpace(s.BaseDir) != "" {
		return filepath.Join(s.BaseDir, fmt.Sprintf("%d", mediaID))
	}
	return filepath.Join(".derived", fmt.Sprintf("%d", mediaID))
}

type StagedDerivedAsset struct {
	mediaID                                    int64
	kind, logicalName, encPath, wrappedDEK, iv string
}

func (a *StagedDerivedAsset) EncPath() string {
	if a == nil {
		return ""
	}
	return a.encPath
}

func (s *DerivedAssetStore) stageReader(ctx context.Context, mediaID int64, kind, logicalName string, r io.Reader) (*StagedDerivedAsset, error) {
	if s == nil || s.DB == nil || s.Vault == nil {
		return nil, fmt.Errorf("derived asset store not configured")
	}
	kind, logicalName = strings.TrimSpace(kind), strings.TrimSpace(logicalName)
	if mediaID <= 0 || kind == "" || logicalName == "" {
		return nil, fmt.Errorf("invalid derived asset args")
	}
	base := strings.TrimSpace(s.BaseDir)
	if base == "" {
		return nil, fmt.Errorf("derived base dir empty")
	}
	dir := filepath.Join(base, fmt.Sprintf("%d", mediaID), sanitizeDerivedKind(kind))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	unique := sanitizeDerivedFileName(logicalName) + "." + uuid.NewString() + ".enc"
	finalPath := filepath.Join(dir, unique)
	kek, err := s.Vault.GetKEK(ctx)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(kek)
	tmp, err := os.CreateTemp(dir, unique+".tmp-")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	result, encErr := kcrypto.EncryptFile(r, tmp, kek)
	closeErr := tmp.Close()
	if encErr != nil {
		return nil, encErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = os.Rename(tmpPath, finalPath); err != nil {
		return nil, err
	}
	return &StagedDerivedAsset{mediaID: mediaID, kind: kind, logicalName: logicalName, encPath: finalPath, wrappedDEK: hex.EncodeToString(result.WrappedDEK), iv: hex.EncodeToString(result.IV)}, nil
}
func (s *DerivedAssetStore) StageBytes(ctx context.Context, mediaID int64, kind, logicalName string, data []byte) (*StagedDerivedAsset, error) {
	return s.stageReader(ctx, mediaID, kind, logicalName, bytes.NewReader(data))
}

func (s *DerivedAssetStore) StagePath(ctx context.Context, mediaID int64, kind, logicalName, plainPath string) (*StagedDerivedAsset, error) {
	f, err := os.Open(strings.TrimSpace(plainPath))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return s.stageReader(ctx, mediaID, kind, logicalName, f)
}
func (s *DerivedAssetStore) CommitStagedTx(ctx context.Context, tx store.SQLExecutor, assets ...*StagedDerivedAsset) ([]string, error) {
	if tx == nil {
		return nil, fmt.Errorf("derived staged transaction is nil")
	}
	old := make([]string, 0, len(assets))
	for _, a := range assets {
		if a == nil {
			return nil, fmt.Errorf("nil staged asset")
		}
		var p string
		err := tx.QueryRowContext(ctx, `SELECT enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind=? AND logical_name=?`, a.mediaID, a.kind, a.logicalName).Scan(&p)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if p != "" && filepath.Clean(p) != filepath.Clean(a.encPath) {
			old = append(old, p)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv,updated_at) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(media_id,artifact_kind,logical_name) DO UPDATE SET enc_path=excluded.enc_path,wrapped_dek=excluded.wrapped_dek,iv=excluded.iv,updated_at=CURRENT_TIMESTAMP`, a.mediaID, a.kind, a.logicalName, a.encPath, a.wrappedDEK, a.iv); err != nil {
			return nil, err
		}
	}
	return old, nil
}
func (s *DerivedAssetStore) AbortStaged(assets ...*StagedDerivedAsset) {
	for _, a := range assets {
		if a != nil {
			_ = os.Remove(a.encPath)
		}
	}
}
func (s *DerivedAssetStore) CleanupReplaced(paths []string) {
	for _, p := range paths {
		if strings.TrimSpace(p) != "" {
			_ = os.Remove(p)
		}
	}
}

// Write encrypts r and atomically publishes one derived asset.
func (s *DerivedAssetStore) Write(ctx context.Context, mediaID int64, kind, logicalName string, r io.Reader) (string, error) {
	a, err := s.stageReader(ctx, mediaID, kind, logicalName, r)
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			s.AbortStaged(a)
		}
	}()
	var old []string
	err = store.WithBusyRetry(ctx, nil, func() error {
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = runDerivedCommitGuardTx(ctx, tx); e != nil {
			return e
		}
		old, e = s.CommitStagedTx(ctx, tx, a)
		if e != nil {
			return e
		}
		return tx.Commit()
	})
	if err != nil {
		return "", err
	}
	committed = true
	s.CleanupReplaced(old)
	return a.encPath, nil
}

// WriteFromTemp encrypts a temp/plain file and deletes it.
func (s *DerivedAssetStore) WriteFromTemp(ctx context.Context, mediaID int64, kind, logicalName, tempPath string) (string, error) {
	tempPath = strings.TrimSpace(tempPath)
	if tempPath == "" {
		return "", fmt.Errorf("empty temp path")
	}
	f, err := os.Open(tempPath)
	if err != nil {
		return "", err
	}
	encPath, err := s.Write(ctx, mediaID, kind, logicalName, f)
	_ = f.Close()
	if err != nil {
		return "", err
	}
	_ = os.Remove(tempPath)
	return encPath, nil
}

// LookupEncPath returns the encrypted path for a derived artifact if registered.
func LookupEncPath(db *sql.DB, mediaID int64, kind, logicalName string) (string, bool) {
	if db == nil || mediaID <= 0 {
		return "", false
	}
	var p string
	err := db.QueryRow(`
		SELECT enc_path FROM media_derived_assets
		WHERE media_id = ? AND artifact_kind = ? AND logical_name = ?
	`, mediaID, strings.TrimSpace(kind), strings.TrimSpace(logicalName)).Scan(&p)
	return p, err == nil && strings.TrimSpace(p) != ""
}

// ExpectedDerivedEncPath returns the on-disk path for a derived encrypted artifact.
func ExpectedDerivedEncPath(baseDir string, mediaID int64, kind, logicalName string) string {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" || mediaID <= 0 {
		return ""
	}
	name := sanitizeDerivedFileName(logicalName) + ".enc"
	return filepath.Join(baseDir, fmt.Sprintf("%d", mediaID), sanitizeDerivedKind(kind), name)
}

// ResolveDerivedEncPath returns a readable encrypted derived artifact path.
// It prefers the DB record and falls back to the expected layout under baseDir.
func ResolveDerivedEncPath(db *sql.DB, baseDir string, mediaID int64, kind, logicalName string) (string, bool) {
	if p, ok := LookupEncPath(db, mediaID, kind, logicalName); ok {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p, true
		}
	}
	if guess := ExpectedDerivedEncPath(baseDir, mediaID, kind, logicalName); guess != "" {
		if st, err := os.Stat(guess); err == nil && !st.IsDir() && st.Size() > 0 {
			return guess, true
		}
	}
	return "", false
}

func lookupDerivedWrappedDEK(db *sql.DB, mediaID int64, path, kind, logicalName string) (string, error) {
	if db == nil || mediaID <= 0 {
		return "", sql.ErrNoRows
	}
	kind = strings.TrimSpace(kind)
	logicalName = strings.TrimSpace(logicalName)
	if kind != "" && logicalName != "" {
		var wrappedHex string
		err := db.QueryRow(`
			SELECT wrapped_dek FROM media_derived_assets
			WHERE media_id = ? AND artifact_kind = ? AND logical_name = ?
		`, mediaID, kind, logicalName).Scan(&wrappedHex)
		if err == nil {
			return wrappedHex, nil
		}
		if err != sql.ErrNoRows {
			return "", err
		}
	}
	path = filepath.Clean(strings.TrimSpace(path))
	var wrappedHex string
	err := db.QueryRow(`
		SELECT wrapped_dek FROM media_derived_assets
		WHERE media_id = ? AND enc_path = ?
	`, mediaID, path).Scan(&wrappedHex)
	if err == nil {
		return wrappedHex, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	rows, err := db.Query(`
		SELECT enc_path, wrapped_dek FROM media_derived_assets WHERE media_id = ?
	`, mediaID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var stored, wh string
		if rows.Scan(&stored, &wh) != nil {
			continue
		}
		if derivedPathsEqual(stored, path) {
			return wh, nil
		}
	}
	return "", sql.ErrNoRows
}

func derivedPathsEqual(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == b {
		return true
	}
	return strings.EqualFold(filepath.ToSlash(a), filepath.ToSlash(b))
}

// OpenDerivedSeeker opens a derived artifact, decrypting Knox .enc when needed.
func OpenDerivedSeeker(db *sql.DB, vault *keystore.Vault, mediaID int64, path string) (*PlaintextSeeker, error) {
	return OpenDerivedArtifactSeeker(db, vault, mediaID, path, "", "")
}

// OpenDerivedArtifactSeeker opens a derived artifact by path, with optional kind/name fallbacks for DEK lookup.
func OpenDerivedArtifactSeeker(db *sql.DB, vault *keystore.Vault, mediaID int64, path, kind, logicalName string) (*PlaintextSeeker, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, os.ErrInvalid
	}
	if !kcrypto.IsEncFile(path) {
		f, err := os.Open(path)
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
		return nil, fmt.Errorf("derived asset requires keystore")
	}
	wrappedHex, err := lookupDerivedWrappedDEK(db, mediaID, path, kind, logicalName)
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
	defer zeroBytes(kek)
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	rsc, err := kcrypto.OpenDecryptSeeker(path, wrapped, kek)
	if err != nil {
		return nil, err
	}
	return &PlaintextSeeker{ReadSeekCloser: rsc, modTime: st.ModTime()}, nil
}

// DeleteForMedia removes derived asset rows and files for one media item.
func (s *DerivedAssetStore) DeleteForMedia(ctx context.Context, mediaID int64) error {
	if s == nil || s.DB == nil || mediaID <= 0 {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT enc_path FROM media_derived_assets WHERE media_id = ?`, mediaID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			_ = os.Remove(p)
		}
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM media_derived_assets WHERE media_id = ?`, mediaID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(s.BaseDir) != "" {
		_ = os.RemoveAll(filepath.Join(s.BaseDir, fmt.Sprintf("%d", mediaID)))
	}
	return nil
}

func (s *DerivedAssetStore) encPath(mediaID int64, kind, logicalName string) (string, error) {
	base := strings.TrimSpace(s.BaseDir)
	if base == "" {
		return "", fmt.Errorf("derived base dir empty")
	}
	name := sanitizeDerivedFileName(logicalName) + ".enc"
	return filepath.Join(base, fmt.Sprintf("%d", mediaID), sanitizeDerivedKind(kind), name), nil
}

func sanitizeDerivedKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "misc"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, kind)
}

func sanitizeDerivedFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	for _, invalid := range []string{":", "*", "?", "\"", "<", ">", "|"} {
		name = strings.ReplaceAll(name, invalid, "_")
	}
	if name == "" {
		return "asset"
	}
	return name
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
