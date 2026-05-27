package handler

// GenerateDocumentCover queues cover.jpg generation for one document.
func (h *Handler) GenerateDocumentCover(mediaID int64) {
	if h == nil || h.DocCoverWorker == nil || mediaID <= 0 {
		return
	}
	h.DocCoverWorker.Enqueue(mediaID)
}

// scheduleDocumentCoverBackfill enqueues missing covers for all documents in a library.
func (h *Handler) scheduleDocumentCoverBackfill(libraryID int64) {
	if h == nil || h.DocCoverWorker == nil || libraryID <= 0 {
		return
	}
	if h.loadLibraryType(libraryID) != "document" {
		return
	}
	go h.DocCoverWorker.BackfillLibrary(libraryID)
}
