package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/keystore"
	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

type serverPublicationResources struct {
	Vault                     *keystore.Vault
	Encryptor                 *storage.AssetEncryptor
	Derived                   *storage.DerivedAssetStore
	PosterRoot, ThumbnailRoot string
}

func (r serverPublicationResources) ValidateEncryptedLibrary(ctx context.Context, q store.SQLExecutor, lib publication.EncryptedLibrary) error {
	if r.Vault == nil || r.Encryptor == nil || r.Derived == nil {
		return errors.New("vault, encryptor, and derived store are required")
	}
	key, err := r.Vault.GetKEK(ctx)
	if err != nil || len(key) == 0 {
		return errors.New("vault key unavailable")
	}
	root, err := r.Encryptor.ResolveLibraryEncBaseTx(ctx, q, lib.ID, lib.Path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(root) == "" {
		return errors.New("encryption root unresolved")
	}
	if strings.TrimSpace(r.Encryptor.EncryptionPrivateRoot()) == "" {
		return errors.New("quarantine root unresolved")
	}
	if _, err = r.Encryptor.ResolveLibraryEncryptionStageRootTx(ctx, q, lib.ID, lib.Path); err != nil {
		return fmt.Errorf("stage root: %w", err)
	}
	return probeRoot(r.Derived.BaseDir)
}
func (r serverPublicationResources) ProbePosterResolver(context.Context) error {
	return probeRoot(r.PosterRoot)
}
func (r serverPublicationResources) ProbeThumbnailResolver(context.Context) error {
	return probeRoot(r.ThumbnailRoot)
}
func probeRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("resolver root unavailable")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	_, err := filepath.Abs(root)
	return err
}
