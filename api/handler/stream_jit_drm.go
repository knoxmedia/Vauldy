package handler

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/jit/session"
	"knox-media/internal/medialibrary"
	"knox-media/internal/transcode"
)

const streamJITKeyMode = "stream_jit_aes128"

// ensureStreamJITEncryption prepares per-media AES-128 key material and an ffmpeg key info file
// for play-time encrypted JIT sessions (drm_enabled libraries).
func (h *Handler) ensureStreamJITEncryption(mediaID int64, pol medialibrary.StreamPolicy, sessionDir string) (*session.StreamEncryption, error) {
	if h == nil || h.App == nil || h.App.DB == nil || mediaID <= 0 || strings.TrimSpace(sessionDir) == "" {
		return nil, fmt.Errorf("invalid stream jit encryption request")
	}
	mode := pol.EncryptionMode
	kidHex, keyHex, err := h.loadOrCreateStreamJITKeys(mediaID, mode)
	if err != nil {
		return nil, err
	}
	keyBytes, err := hex.DecodeString(keyHex)
	if err != nil || len(keyBytes) != 16 {
		return nil, fmt.Errorf("invalid stream jit key")
	}
	keyPath := filepath.Join(sessionDir, "enc.key")
	if err := os.WriteFile(keyPath, keyBytes, 0o600); err != nil {
		return nil, err
	}
	iv := strings.TrimSpace(kidHex)
	if iv == "" {
		iv = strings.Repeat("0", 32)
	}
	keyURI := fmt.Sprintf("/api/v1/drm/hls/aes128/key?media_id=%d&kid=%s", mediaID, kidHex)
	keyInfoPath := filepath.Join(sessionDir, "enc.keyinfo")
	keyInfo := strings.Join([]string{keyURI, filepath.ToSlash(keyPath), iv}, "\n")
	if err := os.WriteFile(keyInfoPath, []byte(keyInfo), 0o600); err != nil {
		return nil, err
	}
	return &session.StreamEncryption{
		Mode:        mode,
		KidHex:      kidHex,
		KeyHex:      keyHex,
		KeyInfoPath: keyInfoPath,
	}, nil
}

func (h *Handler) loadOrCreateStreamJITKeys(mediaID int64, mode string) (kidHex, keyHex string, err error) {
	var dbKid, dbKey, dbMode sql.NullString
	_ = h.App.DB.QueryRow(
		`SELECT kid, key_hex, mode FROM drm_key_material WHERE media_id = ? LIMIT 1`,
		mediaID,
	).Scan(&dbKid, &dbKey, &dbMode)
	if strings.TrimSpace(dbKid.String) != "" && strings.TrimSpace(dbKey.String) != "" {
		return strings.TrimSpace(dbKid.String), strings.TrimSpace(dbKey.String), nil
	}
	kidHex, keyHex, err = transcode.GenerateDRMMaterial()
	if err != nil {
		return "", "", err
	}
	if _, err := h.App.DB.Exec(`
		INSERT INTO drm_key_material (media_id, mode, kid, key_hex, iv_hex, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
		  mode=excluded.mode,
		  kid=excluded.kid,
		  key_hex=excluded.key_hex,
		  iv_hex=excluded.iv_hex,
		  updated_at=CURRENT_TIMESTAMP
	`, mediaID, streamJITKeyMode, kidHex, keyHex, kidHex); err != nil {
		return "", "", err
	}
	switch mode {
	case "powerdrm", "drm":
		if h.App == nil || h.App.Config == nil || strings.TrimSpace(h.App.Config.Data.Dir) == "" {
			return "", "", fmt.Errorf("data dir not configured")
		}
		refDir := filepath.Join(h.App.Config.Data.Dir, "stream_drm", fmt.Sprintf("%d", mediaID))
		if err := os.MkdirAll(refDir, 0o755); err != nil {
			return "", "", err
		}
		refPath := filepath.Join(refDir, "drm_key_ref.json")
		payload := map[string]any{
			"media_id": mediaID,
			"kid":      kidHex,
			"key":      keyHex,
			"alg":      "aes-128",
		}
		raw, merr := json.Marshal(payload)
		if merr != nil {
			return "", "", merr
		}
		if err := os.WriteFile(refPath, raw, 0o600); err != nil {
			return "", "", err
		}
		_, _ = h.App.DB.Exec(`
			INSERT INTO drm_asset (media_id, kid, key_ref, manifest_path, license_policy_json, updated_at)
			VALUES (?, ?, ?, '', '{}', CURRENT_TIMESTAMP)
			ON CONFLICT(media_id) DO UPDATE SET
			  kid=excluded.kid,
			  key_ref=excluded.key_ref,
			  updated_at=CURRENT_TIMESTAMP
		`, mediaID, kidHex, refPath)
	}
	return kidHex, keyHex, nil
}

func (h *Handler) streamDRMPlaylistKeyLine(base string, s *session.Session, c *gin.Context) string {
	if s == nil || s.StreamEncryption == nil {
		return ""
	}
	enc := s.StreamEncryption
	switch enc.Mode {
	case "powerdrm", "drm":
		return fmt.Sprintf(`#EXT-X-KEY:METHOD=AES-128,URI="skd://%s",KEYFORMAT="powerdrm",IV=%s`, enc.KidHex, transcode.PowerDRMIV)
	default:
		keyURL := fmt.Sprintf("%s/api/v1/drm/hls/aes128/key?media_id=%d&kid=%s", base, s.MediaID, enc.KidHex)
		if tok := jitAccessToken(c); tok != "" {
			keyURL = appendQueryValue(keyURL, "access_token", tok)
		}
		return fmt.Sprintf(`#EXT-X-KEY:METHOD=AES-128,URI="%s",IV=0x%s`, keyURL, enc.KidHex)
	}
}
