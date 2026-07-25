package storage

import (
	"context"
	"errors"
	"knox-media/internal/store"
	"path/filepath"
	"strings"
)

// ResolveLibraryEncryptionStageRoot resolves a safe stage root without requiring a media row.
func (s *AssetEncryptor) ResolveLibraryEncryptionStageRoot(ctx context.Context, libraryID int64, libraryPath string) (string, error) {
	return s.ResolveLibraryEncryptionStageRootTx(ctx, s.DB, libraryID, libraryPath)
}
func (s *AssetEncryptor) ResolveLibraryEncryptionStageRootTx(ctx context.Context, q store.SQLExecutor, libraryID int64, libraryPath string) (string, error) {
	base, err := s.ResolveLibraryEncBaseTx(ctx, q, libraryID, libraryPath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(base) == "" {
		return "", errors.New("stage base unresolved")
	}
	return filepath.Join(base, "stages"), nil
}
