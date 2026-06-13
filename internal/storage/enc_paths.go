package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	EncDirModeLibrary = "library"
	EncDirModeData    = "data"
	EncDirModeCustom  = "custom"
)

// NormalizeEncDirMode returns a supported encrypted directory mode.
func NormalizeEncDirMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case EncDirModeData:
		return EncDirModeData
	case EncDirModeCustom:
		return EncDirModeCustom
	default:
		return EncDirModeLibrary
	}
}

// ValidateCustomEncDir checks that a custom directory can be created or accessed.
func ValidateCustomEncDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("custom encrypted directory is required")
	}
	clean := filepath.Clean(dir)
	if clean == "." || clean == string(filepath.Separator) {
		return fmt.Errorf("invalid encrypted directory: %q", dir)
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return fmt.Errorf("encrypted directory not usable: %w", err)
	}
	st, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("encrypted directory not accessible: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("encrypted directory is not a folder: %s", clean)
	}
	return nil
}

// ResolveEncBase returns the base directory for encrypted assets (without file_type suffix).
func (s *AssetEncryptor) ResolveEncBase(ctx context.Context, libraryID int64, plainPath string) (string, error) {
	if s == nil || s.DB == nil {
		return "", errors.New("encryptor not configured")
	}
	var mode, customDir, libPath string
	if err := s.DB.QueryRowContext(ctx, `
		SELECT COALESCE(encrypted_assets_dir_mode,'library'),
		       COALESCE(encrypted_assets_custom_dir,''),
		       COALESCE(path,'')
		FROM library WHERE id = ?
	`, libraryID).Scan(&mode, &customDir, &libPath); err != nil {
		return "", err
	}
	mode = NormalizeEncDirMode(mode)
	switch mode {
	case EncDirModeData:
		if strings.TrimSpace(s.DataDir) == "" {
			return "", errors.New("data directory not configured")
		}
		return filepath.Join(s.DataDir, ".encrypted"), nil
	case EncDirModeCustom:
		customDir = strings.TrimSpace(customDir)
		if err := ValidateCustomEncDir(customDir); err != nil {
			return "", err
		}
		return filepath.Clean(customDir), nil
	default:
		root := libraryRootForPlain(s.DB, libraryID, libPath, plainPath)
		if root == "" {
			return "", errors.New("library root path not configured")
		}
		return filepath.Join(root, ".encrypted"), nil
	}
}

func libraryRootForPlain(db *sql.DB, libraryID int64, libPath, plainPath string) string {
	folders := listLibraryFolderPaths(db, libraryID, libPath)
	if len(folders) == 0 {
		return strings.TrimSpace(libPath)
	}
	plainClean := filepath.Clean(strings.TrimSpace(plainPath))
	for _, f := range folders {
		f = filepath.Clean(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		if plainClean != "" && pathHasPrefix(plainClean, f) {
			return f
		}
	}
	return folders[0]
}

func listLibraryFolderPaths(db *sql.DB, libraryID int64, fallback string) []string {
	if db == nil || libraryID <= 0 {
		return nil
	}
	rows, err := db.Query(`SELECT path FROM library_folder WHERE library_id = ? ORDER BY sort_order, id`, libraryID)
	if err != nil {
		return []string{strings.TrimSpace(fallback)}
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil && strings.TrimSpace(p) != "" {
			out = append(out, filepath.Clean(strings.TrimSpace(p)))
		}
	}
	if len(out) == 0 && strings.TrimSpace(fallback) != "" {
		out = []string{filepath.Clean(strings.TrimSpace(fallback))}
	}
	return out
}

func pathHasPrefix(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}
