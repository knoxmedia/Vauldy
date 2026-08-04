package storage

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
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
		DB:          db,
		Vault:       vault,
		BasePath:    cfg.EncryptedAssetsStoragePath(),
		DataDir:     strings.TrimSpace(cfg.Data.Dir),
		FFmpegPath:  strings.TrimSpace(cfg.FFmpeg.FFmpegPath),
		FFprobePath: strings.TrimSpace(cfg.FFmpeg.FFprobePath),
	}
}

// KickEncryptMedia runs background encryption for a media row when configured.
func KickEncryptMedia(enc *AssetEncryptor, cfg *config.Config, mediaID int64) {
	if enc == nil || cfg == nil || !cfg.EncryptedAssetsEnabled() || mediaID <= 0 {
		return
	}
	go func(mid int64) {
		if !LibraryEncryptEnabled(enc.DB, mid) {
			return
		}
		WaitForPlaintextConsumers(enc.DB, mid, 45*time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if err := enc.EncryptMedia(ctx, mid); err != nil {
			log.Printf("asset encrypt failed media=%d: %v", mid, err)
		}
	}(mediaID)
}

// LibraryEncryptEnabled reports whether the media row's library has encrypted_assets_enabled.
func LibraryEncryptEnabled(db *sql.DB, mediaID int64) bool {
	if db == nil || mediaID <= 0 {
		return false
	}
	var enabled int
	err := db.QueryRow(`
		SELECT COALESCE(l.encrypted_assets_enabled, 0)
		FROM media m
		JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&enabled)
	return err == nil && enabled == 1
}

// KickPendingMediaEncryption periodically encrypts plaintext catalog rows in libraries
// that have encrypted_assets_enabled (covers missed ingest hooks).
func KickPendingMediaEncryption(enc *AssetEncryptor, cfg *config.Config) {
	if enc == nil || cfg == nil || !cfg.EncryptedAssetsEnabled() {
		return
	}
	sweep := func() {
		rows, err := enc.DB.Query(`
			SELECT m.id
			FROM media m
			JOIN library l ON l.id = m.library_id
			WHERE COALESCE(l.encrypted_assets_enabled, 0) = 1
			  AND COALESCE(m.status, 'active') = 'active'
			  AND NOT EXISTS (
			    SELECT 1 FROM media_encrypted_assets e
			    WHERE e.media_id = m.id AND e.status IN ('encrypted', 'plain_missing')
			  )
			LIMIT 100
		`)
		if err != nil {
			log.Printf("asset encrypt: pending sweep query: %v", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var mediaID int64
			if err := rows.Scan(&mediaID); err != nil || mediaID <= 0 {
				continue
			}
			KickEncryptMedia(enc, cfg, mediaID)
		}
	}
	go sweep()
	go func() {
		tk := time.NewTicker(3 * time.Minute)
		defer tk.Stop()
		for range tk.C {
			sweep()
		}
	}()
}

// NewDerivedAssetStoreFromConfig builds derived artifact encryption store when global encrypted assets are enabled.
func NewDerivedAssetStoreFromConfig(cfg *config.Config, db *sql.DB, vault *keystore.Vault) *DerivedAssetStore {
	if cfg == nil || db == nil || vault == nil || !cfg.EncryptedAssetsEnabled() {
		return nil
	}
	dir := strings.TrimSpace(cfg.Data.Dir)
	if dir == "" {
		dir = "./data"
	}
	return &DerivedAssetStore{
		DB:      db,
		Vault:   vault,
		BaseDir: filepath.Join(dir, ".derived"),
	}
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
