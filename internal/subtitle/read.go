package subtitle

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
)

// ReadVTTContent loads subtitle file bytes (plain or Knox .enc derived asset).
func (s *Service) ReadVTTContent(mediaID, subtitleID int64, vault *keystore.Vault) (string, error) {
	if s == nil {
		return "", os.ErrInvalid
	}
	path, err := s.VTTPath(mediaID, subtitleID)
	if err != nil {
		return "", err
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return "", os.ErrNotExist
	}
	if !kcrypto.IsEncFile(path) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	seeker, err := storage.OpenDerivedSeeker(s.DB, vault, mediaID, path)
	if err != nil {
		return "", err
	}
	defer seeker.Close()
	b, err := io.ReadAll(seeker)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ParseMediaSubtitleVTTURL extracts media/subtitle ids from Knox VTT URLs.
func ParseMediaSubtitleVTTURL(raw string) (mediaID, subtitleID int64, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(strings.ToLower(raw), "blob:") {
		return 0, 0, false
	}
	if idx := strings.Index(raw, "://"); idx >= 0 {
		if slash := strings.Index(raw[idx+3:], "/"); slash >= 0 {
			raw = raw[idx+3+slash:]
		}
	}
	raw = strings.TrimPrefix(raw, "/")
	const prefix = "api/v1/media/"
	if !strings.HasPrefix(strings.ToLower(raw), prefix) {
		return 0, 0, false
	}
	raw = raw[len(prefix):]
	parts := strings.Split(raw, "/")
	if len(parts) < 4 || parts[1] != "subtitles" || !strings.HasPrefix(strings.ToLower(parts[3]), "vtt") {
		return 0, 0, false
	}
	var mid, sid int64
	var err error
	if mid, err = strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64); err != nil || mid <= 0 {
		return 0, 0, false
	}
	if sid, err = strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64); err != nil || sid <= 0 {
		return 0, 0, false
	}
	return mid, sid, true
}
