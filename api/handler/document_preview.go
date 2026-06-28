package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"

	"knox-media/internal/doctrans"
	"knox-media/internal/storage"
)

func (h *Handler) DocumentPreviewInfo(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	path, format, _, err := h.loadDocumentSource(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	office := doctrans.IsOfficeDocument(path, format)
	resp := gin.H{
		"id":              id,
		"format":          format,
		"needs_preview":   office,
		"preview_ready":   false,
		"conversion_enabled": h.App.Config.DocTransEnabled(),
	}
	if !office {
		c.JSON(http.StatusOK, resp)
		return
	}
	conv, err := h.docConverter()
	if err != nil {
		resp["error"] = err.Error()
		c.JSON(http.StatusOK, resp)
		return
	}
	pdf := conv.PreviewPDFPath(id)
	if enc, ok := storage.LookupEncPath(h.App.DB, id, "doc_preview", "preview.pdf"); ok {
		pdf = enc
	}
	if st, err := os.Stat(pdf); err == nil && !st.IsDir() && st.Size() > 0 {
		resp["preview_ready"] = true
	}
	resp["preview_url"] = fmt.Sprintf("/api/v1/media/%d/document/preview.pdf", id)
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) ServeDocumentPreview(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	if h.App.Config == nil || !h.App.Config.DocTransEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "document conversion disabled"})
		return
	}
	path, format, mtime, err := h.loadDocumentSource(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if !doctrans.IsOfficeDocument(path, format) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "preview conversion not supported for this format"})
		return
	}
	workPath, cleanup, err := storage.MaterializePlaintextTemp(h.App.DB, h.KeyVault, id, path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer cleanup()
	conv, err := h.docConverter()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	pdfPath, err := conv.EnsurePreviewPDF(c.Request.Context(), id, workPath, mtime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if h.DerivedStore != nil {
		if encPath, encErr := h.DerivedStore.FinalizePath(c.Request.Context(), id, "doc_preview", "preview.pdf", pdfPath); encErr == nil {
			pdfPath = encPath
		}
	}
	h.serveDerivedAsset(c, id, pdfPath, "application/pdf")
}

func (h *Handler) loadDocumentSource(id int64) (filePath, format string, mtime int64, err error) {
	var path, ftype, fmtVal sql.NullString
	var fileMtime sql.NullInt64
	var libraryID int64
	e := h.App.DB.QueryRow(`
		SELECT file_path, file_type, format, file_mtime, library_id FROM media WHERE id = ? AND status = 'active'`, id).
		Scan(&path, &ftype, &fmtVal, &fileMtime, &libraryID)
	if e != nil {
		if e == sql.ErrNoRows {
			return "", "", 0, fmt.Errorf("not found")
		}
		return "", "", 0, e
	}
	if ftype.String != "document" {
		return "", "", 0, fmt.Errorf("not a document")
	}
	if !path.Valid || path.String == "" {
		return "", "", 0, fmt.Errorf("file path missing")
	}
	mtime = fileMtime.Int64
	catalog := path.String
	if pref := storage.PreferredFFmpegPath(h.App.DB, id, libraryID, catalog); pref != "" {
		catalog = pref
	} else if abs := storage.ResolveMediaAbsolutePath(h.App.DB, libraryID, catalog); abs != "" {
		catalog = abs
	}
	return catalog, fmtVal.String, mtime, nil
}
