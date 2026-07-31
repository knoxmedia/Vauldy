package handler

import "knox-media/internal/storage"

// KickEncryptMediaAsset encrypts media at rest when the library has encrypted_assets_enabled.
func (h *Handler) KickEncryptMediaAsset(mediaID int64) {
	if h == nil || h.App == nil {
		return
	}
	storage.KickEncryptMedia(h.AssetEncryptor, h.App.Config, mediaID)
}
