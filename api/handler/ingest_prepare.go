package handler

import "knox-media/internal/jit/ingestprepare"

// KickIngestJITPrepare pushes Redis video meta + virtual slice index after scanner/upload ingest when enabled on the library.
func (h *Handler) KickIngestJITPrepare(mediaID int64) {
	if h == nil || h.App == nil || h.Instant == nil {
		return
	}
	ingestprepare.Kick(h.App.DB, h.Instant, mediaID)
}
