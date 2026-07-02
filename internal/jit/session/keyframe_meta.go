package session

import (
	"strings"

	"knox-media/internal/jit/keyframes"
)

func (m *Manager) loadKeyframeMeta(s *Session) *keyframes.Meta {
	if m == nil || s == nil || strings.TrimSpace(m.keyframesDir) == "" {
		return nil
	}
	cache, err := keyframes.NewCache(m.keyframesDir, m.ffprobePath)
	if err != nil {
		return nil
	}
	meta, err := cache.LoadForMedia(m.DB, m.Vault, s.MediaID, s.FileID, s.SourcePath)
	if err != nil || meta == nil || len(meta.PTS) == 0 {
		return nil
	}
	return meta
}
