package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"knox-media/internal/store"
)

// ResolveLibraryEncBase resolves an enabled library's canonical encryption root without requiring a media row.
func (s *AssetEncryptor) ResolveLibraryEncBase(ctx context.Context, libraryID int64, libraryPath string) (string, error) {
	return s.ResolveLibraryEncBaseTx(ctx, s.DB, libraryID, libraryPath)
}
func (s *AssetEncryptor) ResolveLibraryEncBaseTx(ctx context.Context, q store.SQLExecutor, libraryID int64, libraryPath string) (string, error) {
	if s == nil || q == nil {
		return "", errors.New("encryptor not configured")
	}
	var mode, custom, dbPath string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(encrypted_assets_dir_mode,'library'),COALESCE(encrypted_assets_custom_dir,''),COALESCE(path,'') FROM library WHERE id=?`, libraryID).Scan(&mode, &custom, &dbPath); err != nil {
		return "", err
	}
	if strings.TrimSpace(libraryPath) != "" {
		dbPath = libraryPath
	}
	var root string
	switch NormalizeEncDirMode(mode) {
	case EncDirModeData:
		root = filepath.Join(s.DataDir, ".encrypted")
	case EncDirModeCustom:
		root = custom
	default:
		root = filepath.Join(dbPath, ".encrypted")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("encrypted root not configured")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if abs == filepath.VolumeName(abs)+string(filepath.Separator) {
		return "", errors.New("encrypted root cannot be volume root")
	}
	return abs, nil
}
