package handler

import (
	"knox-media/internal/medialibrary"
)

func (h *Handler) loadStreamPolicy(mediaID int64) medialibrary.StreamPolicy {
	if h == nil || h.App == nil || h.App.DB == nil {
		return medialibrary.StreamPolicy{}
	}
	pol, err := medialibrary.LoadStreamPolicy(h.App.DB, mediaID)
	if err != nil {
		return medialibrary.StreamPolicy{}
	}
	return pol
}
