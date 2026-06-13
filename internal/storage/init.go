package storage

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strings"
	"time"

	"knox-media/internal/config"
	"knox-media/internal/keystore"
)

// NewAssetEncryptorFromConfig builds vault + encryptor when global encrypted assets are enabled.
func NewAssetEncryptorFromConfig(cfg *config.Config, db *sql.DB) (*keystore.Vault, *AssetEncryptor) {
	if cfg == nil || db == nil || !cfg.EncryptedAssetsEnabled() {
		return nil, nil
	}
	mainKey := strings.TrimSpace(os.Getenv("KNOX_MAIN_KEY"))
	if mainKey == "" {
		mainKey = strings.TrimSpace(cfg.Security.JWTSecret)
	}
	vault, err := keystore.NewVault(mainKey, cfg.EncryptedAssetsKEKSaltPath())
	if err != nil {
		log.Printf("encrypted assets: keystore init failed: %v", err)
		return nil, nil
	}
	return vault, &AssetEncryptor{
		DB:       db,
		Vault:    vault,
		BasePath: cfg.EncryptedAssetsStoragePath(),
		DataDir:  strings.TrimSpace(cfg.Data.Dir),
	}
}

// KickEncryptMedia runs background encryption for a media row when configured.
func KickEncryptMedia(enc *AssetEncryptor, cfg *config.Config, mediaID int64) {
	if enc == nil || cfg == nil || !cfg.EncryptedAssetsEnabled() || mediaID <= 0 {
		return
	}
	go func(mid int64) {
		WaitForPlaintextConsumers(enc.DB, mid, 45*time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if err := enc.EncryptMedia(ctx, mid); err != nil {
			log.Printf("asset encrypt failed media=%d: %v", mid, err)
		}
	}(mediaID)
}

// KickEncryptMediaManual runs on-demand encryption for one media item (menu action).
func KickEncryptMediaManual(enc *AssetEncryptor, cfg *config.Config, mediaID int64) {
	if enc == nil || cfg == nil || !cfg.EncryptedAssetsEnabled() || mediaID <= 0 {
		return
	}
	go func(mid int64) {
		WaitForPlaintextConsumers(enc.DB, mid, 45*time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if err := enc.EncryptMediaManual(ctx, mid); err != nil {
			log.Printf("asset encrypt manual failed media=%d: %v", mid, err)
		}
	}(mediaID)
}
