package handler

import "context"

func (h *Handler) unlinkedMusicCount(ctx context.Context, libraryID int64) (int64, error) {
	var n int64
	err := h.App.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM media m WHERE m.library_id=? AND m.status='active' AND m.file_type='audio' AND NOT EXISTS(SELECT 1 FROM music_track mt WHERE mt.media_id=m.id)`, libraryID).Scan(&n)
	return n, err
}
func (h *Handler) unlinkedTVCount(ctx context.Context, libraryID int64) (int64, error) {
	var n int64
	err := h.App.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM media m WHERE m.library_id=? AND m.status='active' AND m.file_type='video' AND NOT EXISTS(SELECT 1 FROM episode_media em WHERE em.media_id=m.id)`, libraryID).Scan(&n)
	return n, err
}
