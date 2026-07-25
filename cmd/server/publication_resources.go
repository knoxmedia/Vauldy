package main

import (
	"context"
	"errors"
	"fmt"
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
	ProbeOps                  artifactProbeOps
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
	if err = r.probeRoot(root); err != nil {
		return fmt.Errorf("encryption root: %w", err)
	}
	quarantineRoot := r.Encryptor.EncryptionPrivateRoot()
	if strings.TrimSpace(quarantineRoot) == "" {
		return errors.New("quarantine root unresolved")
	}
	if err = r.probeRoot(quarantineRoot); err != nil {
		return fmt.Errorf("quarantine root: %w", err)
	}
	stageRoot, err := r.Encryptor.ResolveLibraryEncryptionStageRootTx(ctx, q, lib.ID, lib.Path)
	if err != nil {
		return fmt.Errorf("stage root: %w", err)
	}
	if err = r.probeRoot(stageRoot); err != nil {
		return fmt.Errorf("stage root: %w", err)
	}
	if err = r.probeRoot(r.Derived.BaseDir); err != nil {
		return fmt.Errorf("derived root: %w", err)
	}
	return nil
}
func (r serverPublicationResources) ProbePosterResolver(context.Context) error {
	return r.probeRoot(r.PosterRoot)
}
func (r serverPublicationResources) ProbeThumbnailResolver(context.Context) error {
	return r.probeRoot(r.ThumbnailRoot)
}
func (r serverPublicationResources) probeRoot(root string) error {
	ops := r.ProbeOps
	if ops == nil {
		ops = osArtifactProbeOps{}
	}
	return probeArtifactRoot(root, ops)
}
