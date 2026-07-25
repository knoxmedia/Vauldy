package handler

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/doccover"
	"knox-media/internal/doctrans"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

type documentListQuery struct {
	Q        string
	Author   string
	Format   string
	Tag      string
	Year     string
	Parent   string
	Sort     string
	Order    string
	FullText bool
}

func (h *Handler) ListDocumentNodes(c *gin.Context) {
	libID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	parent := strings.TrimSpace(c.Query("parent"))
	q := `
		SELECT node_path, node_name, node_type, media_id
		FROM library_node ln
		LEFT JOIN media m ON m.id=ln.media_id
		WHERE ln.library_id = ? AND COALESCE(ln.parent_path, '') = ?
		  AND (ln.media_id IS NULL OR m.publication_state IN ('published','degraded'))
		ORDER BY node_type DESC, node_name COLLATE NOCASE`
	rows, err := h.App.DB.Query(q, libID, parent)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, 64)
	for rows.Next() {
		var nodePath, nodeName, nodeType sql.NullString
		var mediaID sql.NullInt64
		if rows.Scan(&nodePath, &nodeName, &nodeType, &mediaID) != nil {
			continue
		}
		item := gin.H{
			"path":      nodePath.String,
			"name":      nodeName.String,
			"node_type": nodeType.String,
		}
		if mediaID.Valid && mediaID.Int64 > 0 {
			item["media_id"] = mediaID.Int64
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "parent": parent})
}

func (h *Handler) ListDocuments(c *gin.Context) {
	libID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	qp := documentListQuery{
		Q:        strings.TrimSpace(c.Query("q")),
		Author:   strings.TrimSpace(c.Query("author")),
		Format:   strings.TrimSpace(c.Query("format")),
		Tag:      strings.TrimSpace(c.Query("tag")),
		Year:     strings.TrimSpace(c.Query("year")),
		Parent:   strings.TrimSpace(c.Query("parent")),
		Sort:     c.DefaultQuery("sort", "title"),
		Order:    strings.ToLower(c.DefaultQuery("order", "asc")),
		FullText: strings.TrimSpace(c.Query("fulltext")) == "1",
	}
	limit := 500
	if ls := c.Query("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 && n <= 5000 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	items, err := h.queryDocumentsContext(ctx, libID, qp, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) queryDocuments(libID int64, qp documentListQuery, limit int) ([]gin.H, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return h.queryDocumentsContext(ctx, libID, qp, limit)
}

func (h *Handler) queryDocumentsContext(ctx context.Context, libID int64, qp documentListQuery, limit int) ([]gin.H, error) {
	q := `
		SELECT m.id, m.file_id, m.title, m.file_path, m.format, m.status, m.created_at,
			COALESCE(json_extract(m.meta_json, '$.document.author'), '') AS author,
			COALESCE(json_extract(m.meta_json, '$.document.publisher'), '') AS publisher,
			COALESCE(CAST(json_extract(m.meta_json, '$.document.year') AS INTEGER), 0) AS doc_year,
			COALESCE(CAST(json_extract(m.meta_json, '$.document.file_size') AS INTEGER), 0) AS file_size,
			COALESCE(json_extract(m.meta_json, '$.document.modified_at'), '') AS modified_at,
			COALESCE(json_extract(m.meta_json, '$.document.description'), '') AS description,
			COALESCE(json_extract(m.meta_json, '$.document.format'), m.format, '') AS doc_format,
			COALESCE(CAST(json_extract(m.meta_json, '$.document.page_count') AS INTEGER), 0) AS page_count,
			(SELECT MAX(rp.update_at) FROM read_progress rp WHERE rp.media_id = m.id) AS last_read_at
		FROM media m
		WHERE m.library_id = ? AND m.file_type = 'document' AND m.status = 'active' AND m.publication_state IN ('published','degraded')`
	args := []any{libID}
	if qp.Author != "" {
		q += ` AND json_extract(m.meta_json, '$.document.author') = ?`
		args = append(args, qp.Author)
	}
	if qp.Format != "" {
		q += ` AND LOWER(COALESCE(json_extract(m.meta_json, '$.document.format'), m.format, '')) = ?`
		args = append(args, strings.ToLower(qp.Format))
	}
	if qp.Year != "" {
		q += ` AND CAST(json_extract(m.meta_json, '$.document.year') AS TEXT) = ?`
		args = append(args, qp.Year)
	}
	if qp.Tag != "" {
		q += ` AND EXISTS (SELECT 1 FROM document_tag dt WHERE dt.media_id = m.id AND dt.tag = ? COLLATE NOCASE)`
		args = append(args, qp.Tag)
	}
	if qp.Parent != "" {
		q += ` AND EXISTS (
			SELECT 1 FROM library_node ln
			WHERE ln.media_id = m.id AND ln.library_id = ? AND ln.node_path LIKE ? ESCAPE '\'
		)`
		args = append(args, libID, escapeLike(qp.Parent)+"/%")
	}
	if qp.Q != "" {
		like := "%" + escapeLike(qp.Q) + "%"
		if qp.FullText {
			q += ` AND (
				m.title LIKE ? ESCAPE '\'
				OR json_extract(m.meta_json, '$.document.author') LIKE ? ESCAPE '\'
				OR json_extract(m.meta_json, '$.document.text_preview') LIKE ? ESCAPE '\'
			)`
			args = append(args, like, like, like)
		} else {
			q += ` AND (
				m.title LIKE ? ESCAPE '\'
				OR json_extract(m.meta_json, '$.document.author') LIKE ? ESCAPE '\'
			)`
			args = append(args, like, like)
		}
	}
	orderCol := "m.title COLLATE NOCASE"
	switch qp.Sort {
	case "size":
		orderCol = "file_size"
	case "modified":
		orderCol = "modified_at"
	case "added":
		orderCol = "datetime(m.created_at)"
	case "author":
		orderCol = "author COLLATE NOCASE"
	}
	orderDir := "ASC"
	if qp.Order == "desc" {
		orderDir = "DESC"
	}
	q += fmt.Sprintf(` ORDER BY %s %s LIMIT ?`, orderCol, orderDir)
	args = append(args, limit)

	rows, err := h.App.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var id int64
		var fileID, title, path, format, status, created, author, publisher, modified, desc, docFormat, lastRead sql.NullString
		var year, fileSize, pageCount sql.NullInt64
		if err := rows.Scan(&id, &fileID, &title, &path, &format, &status, &created, &author, &publisher, &year, &fileSize, &modified, &desc, &docFormat, &pageCount, &lastRead); err != nil {
			return nil, err
		}
		items = append(items, gin.H{
			"id":           id,
			"file_id":      fileID.String,
			"title":        title.String,
			"file_path":    path.String,
			"format":       firstNonEmpty(docFormat.String, format.String),
			"author":       author.String,
			"publisher":    publisher.String,
			"year":         year.Int64,
			"file_size":    fileSize.Int64,
			"modified_at":  modified.String,
			"description":  desc.String,
			"page_count":   pageCount.Int64,
			"created_at":   created.String,
			"last_read_at": lastRead.String,
			"cover_url":    fmt.Sprintf("/api/v1/media/%d/document/cover.jpg", id),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]any, len(items))
	byID := make(map[int64]gin.H, len(items))
	for i, item := range items {
		id := item["id"].(int64)
		ids[i] = id
		item["tags"] = []string{}
		byID[id] = item
	}
	tagRows, err := h.App.DB.QueryContext(ctx, `SELECT media_id, tag FROM document_tag WHERE media_id IN (`+documentTagPlaceholders(len(ids))+`) ORDER BY media_id, tag COLLATE NOCASE, tag`, ids...)
	if err != nil {
		return nil, err
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var id int64
		var tag string
		if err := tagRows.Scan(&id, &tag); err != nil {
			return nil, err
		}
		if item, ok := byID[id]; ok && tag != "" {
			item["tags"] = append(item["tags"].([]string), tag)
		}
	}
	if err := tagRows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (h *Handler) ListDocumentFacets(c *gin.Context) {
	libID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	kind := strings.TrimSpace(c.Query("kind"))
	switch kind {
	case "author", "format", "tag", "year":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be author|format|tag|year"})
		return
	}
	items, err := h.queryDocumentFacets(libID, kind)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "kind": kind})
}

func (h *Handler) queryDocumentFacets(libID int64, kind string) ([]gin.H, error) {
	var q string
	switch kind {
	case "author":
		q = `
			SELECT COALESCE(NULLIF(json_extract(meta_json, '$.document.author'), ''), 'unknown') AS name, COUNT(1) AS cnt
			FROM media WHERE library_id = ? AND file_type = 'document' AND status = 'active' AND publication_state IN ('published','degraded')
			GROUP BY name ORDER BY cnt DESC, name COLLATE NOCASE LIMIT 200`
	case "format":
		q = `
			SELECT LOWER(COALESCE(NULLIF(json_extract(meta_json, '$.document.format'), ''), format, 'unknown')) AS name, COUNT(1) AS cnt
			FROM media WHERE library_id = ? AND file_type = 'document' AND status = 'active' AND publication_state IN ('published','degraded')
			GROUP BY name ORDER BY cnt DESC, name COLLATE NOCASE LIMIT 50`
	case "year":
		q = `
			SELECT CAST(COALESCE(json_extract(meta_json, '$.document.year'), 0) AS TEXT) AS name, COUNT(1) AS cnt
			FROM media WHERE library_id = ? AND file_type = 'document' AND status = 'active' AND publication_state IN ('published','degraded')
			GROUP BY name HAVING name != '0' ORDER BY name DESC LIMIT 100`
	case "tag":
		q = `
			SELECT MIN(dt.tag) AS name, COUNT(1) AS cnt
			FROM document_tag dt
			JOIN media m ON m.id = dt.media_id
			WHERE m.library_id = ? AND m.file_type = 'document' AND m.status = 'active' AND m.publication_state IN ('published','degraded')
			GROUP BY dt.tag COLLATE NOCASE ORDER BY cnt DESC, name COLLATE NOCASE LIMIT 200`
	}
	rows, err := h.App.DB.Query(q, libID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]gin.H, 0, 64)
	for rows.Next() {
		var name sql.NullString
		var cnt sql.NullInt64
		if rows.Scan(&name, &cnt) != nil {
			continue
		}
		items = append(items, gin.H{"name": name.String, "count": cnt.Int64})
	}
	return items, nil
}

func (h *Handler) GetDocumentDetail(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	var title, path, format, metaJSON, fileType sql.NullString
	if err := h.App.DB.QueryRow(`
		SELECT title, file_path, format, meta_json, file_type FROM media WHERE id = ?`, id).Scan(&title, &path, &format, &metaJSON, &fileType); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if fileType.String != "document" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not a document"})
		return
	}
	docMeta := parseDocumentMetaJSON(metaJSON.String)
	tags, _ := h.loadDocumentTags(id)
	docFormat := firstNonEmpty(docMeta.Format, format.String)
	item := gin.H{
		"id":           id,
		"title":        title.String,
		"file_path":    path.String,
		"format":       docFormat,
		"author":       docMeta.Author,
		"publisher":    docMeta.Publisher,
		"year":         docMeta.Year,
		"description":  docMeta.Description,
		"page_count":   docMeta.PageCount,
		"file_size":    docMeta.FileSize,
		"modified_at":  docMeta.ModifiedAt,
		"language":     docMeta.Language,
		"tags":         tags,
		"cover_url":    fmt.Sprintf("/api/v1/media/%d/document/cover.jpg", id),
		"stream_url":   fmt.Sprintf("/api/v1/media/%d/play", id),
		"download_url": fmt.Sprintf("/api/v1/media/%d/play?download=1", id),
	}
	if doctrans.IsOfficeDocument(path.String, docFormat) {
		item["needs_preview"] = true
		item["preview_url"] = fmt.Sprintf("/api/v1/media/%d/document/preview.pdf", id)
	}
	c.JSON(http.StatusOK, item)
}

func (h *Handler) derivedBaseDir() string {
	if h != nil && h.DerivedStore != nil && strings.TrimSpace(h.DerivedStore.BaseDir) != "" {
		return h.DerivedStore.BaseDir
	}
	if h != nil && h.App != nil && h.App.Config != nil {
		return filepath.Join(strings.TrimSpace(h.App.Config.Data.Dir), ".derived")
	}
	return ""
}

func (h *Handler) ServeDocumentCover(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	if enc, ok := storage.ResolveDerivedEncPath(h.App.DB, h.derivedBaseDir(), id, "doc_cover", "cover.jpg"); ok {
		h.serveDerivedAssetKind(c, id, enc, "image/jpeg", "doc_cover", "cover.jpg")
		return
	}
	cache := h.documentCoverPath(id)
	if st, err := os.Stat(cache); err == nil && !st.IsDir() && st.Size() > 0 {
		h.serveDerivedAsset(c, id, cache, "image/jpeg")
		return
	}
	// LibreOffice may write preview.jpg before copy/rename completes.
	previewJPEG := filepath.Join(filepath.Dir(h.documentCoverPath(id)), "preview.jpg")
	if st, err := os.Stat(previewJPEG); err == nil && !st.IsDir() && st.Size() > 0 {
		h.serveDerivedAsset(c, id, previewJPEG, "image/jpeg")
		return
	}
	var filePath, format sql.NullString
	if err := h.App.DB.QueryRow(`SELECT file_path, format FROM media WHERE id = ? AND file_type = 'document'`, id).Scan(&filePath, &format); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if strings.EqualFold(format.String, "epub") {
		epubPath := filePath.String
		if work, cleanup, err := storage.MaterializePlaintextTemp(h.App.DB, h.KeyVault, id, epubPath); err == nil {
			epubPath = work
			defer cleanup()
		}
		if cover := extractEPUBCover(epubPath, h.documentCoverPath(id)); cover != "" {
			h.serveDerivedAsset(c, id, cover, "image/jpeg")
			return
		}
	}
	previewDir := ""
	if h.App != nil && h.App.Config != nil {
		previewDir = h.App.Config.Data.Preview
	}
	if doccover.NeedsCoverWork(h.App.DB, previewDir, h.derivedBaseDir(), id, 0) {
		h.GenerateDocumentCover(id)
	}
	h.serveDocumentPlaceholder(c, format.String)
}

func (h *Handler) documentCoverPath(id int64) string {
	if h == nil || h.App == nil || h.App.Config == nil {
		return ""
	}
	return doccover.Path(h.App.Config.Data.Preview, id)
}

func (h *Handler) serveDocumentPlaceholder(c *gin.Context, format string) {
	c.Header("Content-Type", "image/svg+xml")
	c.Header("Cache-Control", "public, max-age=3600")
	label := strings.ToUpper(firstNonEmpty(format, "DOC"))
	if len(label) > 8 {
		label = label[:8]
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="240" height="320" viewBox="0 0 240 320">
		<rect width="240" height="320" rx="8" fill="#2a3142"/>
		<text x="120" y="170" text-anchor="middle" fill="#8ea0c8" font-size="36" font-family="sans-serif">%s</text>
	</svg>`, label)
	c.String(http.StatusOK, svg)
}

type readProgressBody struct {
	Position string   `json:"position"`
	Percent  *float64 `json:"percent"`
}

func (h *Handler) SaveReadProgress(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, true); !ok {
		return
	}
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "API client cannot sync read progress"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "login required"})
		return
	}
	var body readProgressBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pct := 0.0
	if body.Percent != nil {
		pct = *body.Percent
	}
	_, err = h.App.DB.Exec(`
		INSERT INTO read_progress (user_id, media_id, position, percent, update_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(user_id, media_id) DO UPDATE SET
			position = excluded.position,
			percent = excluded.percent,
			update_at = CURRENT_TIMESTAMP`,
		uid, id, strings.TrimSpace(body.Position), pct)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) GetReadProgress(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusOK, gin.H{"position": "", "percent": 0})
		return
	}
	var position sql.NullString
	var percent sql.NullFloat64
	_ = h.App.DB.QueryRow(`
		SELECT position, percent FROM read_progress WHERE user_id = ? AND media_id = ?`, uid, id).Scan(&position, &percent)
	c.JSON(http.StatusOK, gin.H{
		"position": position.String,
		"percent":  percent.Float64,
	})
}

func (h *Handler) ListRecentDocuments(c *gin.Context) {
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusOK, gin.H{"items": []any{}})
		return
	}
	libParam := strings.TrimSpace(c.Param("id"))
	if libParam == "0" {
		libParam = strings.TrimSpace(c.Query("library_id"))
	}
	limit := 30
	q := `
		SELECT m.id, m.title, m.format,
			COALESCE(json_extract(m.meta_json, '$.document.author'), '') AS author,
			COALESCE(json_extract(m.meta_json, '$.document.format'), m.format, '') AS doc_format,
			rp.position, rp.percent, rp.update_at
		FROM read_progress rp
		JOIN media m ON m.id = rp.media_id
		WHERE rp.user_id = ? AND m.file_type = 'document' AND m.status = 'active' AND m.publication_state IN ('published','degraded')`
	args := []any{uid}
	if libParam != "" {
		q += ` AND m.library_id = ?`
		args = append(args, libParam)
	}
	q += ` ORDER BY rp.update_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := h.App.DB.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var id int64
		var title, format, author, docFormat, position, updated sql.NullString
		var percent sql.NullFloat64
		if rows.Scan(&id, &title, &format, &author, &docFormat, &position, &percent, &updated) != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id, "title": title.String, "author": author.String,
			"format":   firstNonEmpty(docFormat.String, format.String),
			"position": position.String, "percent": percent.Float64, "update_at": updated.String,
			"cover_url": fmt.Sprintf("/api/v1/media/%d/document/cover.jpg", id),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type batchDownloadBody struct {
	MediaIDs []int64 `json:"media_ids"`
	DirPath  string  `json:"dir_path"`
}

func (h *Handler) BatchDownloadDocuments(c *gin.Context) {
	var profile *userPermissionProfile
	if !middleware.IsAPIClient(c) {
		uid := middleware.UserID(c)
		if uid <= 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		loaded, err := h.loadUserPermissionProfile(uid)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !loaded.CanDownload {
			c.JSON(http.StatusForbidden, gin.H{"error": "download denied"})
			return
		}
		profile = &loaded
	}
	var body batchDownloadBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if profile != nil {
		allowed, err := h.documentDownloadRequestAllowed(*profile, body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !allowed {
			c.JSON(http.StatusForbidden, gin.H{"error": "document download access denied"})
			return
		}
	}
	paths, titles, err := h.resolveDocumentDownloadPaths(body.MediaIDs, body.DirPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(paths) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files to download"})
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="documents-%s.zip"`, time.Now().Format("20060102-150405")))
	zw := zip.NewWriter(c.Writer)
	defer zw.Close()
	for i, p := range paths {
		name := titles[i]
		if name == "" {
			name = filepath.Base(p)
		}
		w, err := zw.Create(name)
		if err != nil {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		_, _ = io.Copy(w, f)
		_ = f.Close()
	}
}

func documentDownloadTargetAllowed(profile userPermissionProfile, libraryID int64, filePath string) bool {
	if !strings.EqualFold(profile.LibraryScope, "selected") {
		return true
	}
	if _, ok := profile.AllowedLibraryIDs[libraryID]; !ok {
		return false
	}
	folders := profile.AllowedLibraryFolders[libraryID]
	return len(folders) == 0 || pathMatchesAnyFolder(filePath, folders)
}

func (h *Handler) documentDownloadRequestAllowed(profile userPermissionProfile, body batchDownloadBody) (bool, error) {
	if dir := strings.TrimSpace(body.DirPath); dir != "" {
		var libraryID int64
		if err := h.App.DB.QueryRow(`SELECT library_id FROM library_node WHERE node_path=? AND node_type='dir'`, dir).Scan(&libraryID); err != nil {
			return false, fmt.Errorf("directory not found")
		}
		return documentDownloadTargetAllowed(profile, libraryID, dir), nil
	}
	for _, id := range body.MediaIDs {
		if id <= 0 {
			return false, nil
		}
		var libraryID int64
		var filePath string
		err := h.App.DB.QueryRow(`SELECT library_id, COALESCE(file_path,'') FROM media WHERE id=? AND file_type='document' AND status='active' AND publication_state IN ('published','degraded')`, id).Scan(&libraryID, &filePath)
		if err != nil || !documentDownloadTargetAllowed(profile, libraryID, filePath) {
			return false, nil
		}
	}
	return len(body.MediaIDs) > 0, nil
}

func (h *Handler) resolveDocumentDownloadPaths(ids []int64, dirPath string) ([]string, []string, error) {
	if strings.TrimSpace(dirPath) != "" {
		return h.resolveDirDownloadPaths(dirPath)
	}
	paths := make([]string, 0, len(ids))
	titles := make([]string, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		var p, title, fileType sql.NullString
		if err := h.App.DB.QueryRow(`SELECT file_path, title, file_type FROM media WHERE id = ? AND status = 'active' AND publication_state IN ('published','degraded')`, id).Scan(&p, &title, &fileType); err != nil {
			continue
		}
		if fileType.String != "document" {
			continue
		}
		paths = append(paths, p.String)
		name := title.String
		if name == "" {
			name = filepath.Base(p.String)
		} else if !strings.HasSuffix(strings.ToLower(name), strings.ToLower(filepath.Ext(p.String))) {
			name += filepath.Ext(p.String)
		}
		titles = append(titles, name)
	}
	return paths, titles, nil
}

func (h *Handler) resolveDirDownloadPaths(dirPath string) ([]string, []string, error) {
	var libID int64
	var nodePath sql.NullString
	if err := h.App.DB.QueryRow(`SELECT library_id, node_path FROM library_node WHERE node_path = ? AND node_type = 'dir' LIMIT 1`, dirPath).Scan(&libID, &nodePath); err != nil {
		return nil, nil, fmt.Errorf("directory not found")
	}
	rows, err := h.App.DB.Query(`
		SELECT m.file_path, m.title
		FROM library_node ln
		JOIN media m ON m.id = ln.media_id
		WHERE ln.library_id = ? AND ln.node_type = 'file'
		  AND (ln.node_path = ? OR ln.node_path LIKE ? ESCAPE '\')
		  AND m.file_type = 'document' AND m.status = 'active' AND m.publication_state IN ('published','degraded')`,
		libID, dirPath, escapeLike(dirPath)+"/%")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	paths := make([]string, 0, 32)
	titles := make([]string, 0, 32)
	for rows.Next() {
		var p, title sql.NullString
		if rows.Scan(&p, &title) != nil {
			continue
		}
		rel := strings.TrimPrefix(p.String, filepath.Dir(p.String))
		paths = append(paths, p.String)
		titles = append(titles, filepath.Base(p.String))
		_ = rel
	}
	return paths, titles, nil
}

type batchUpdateDocumentTagsBody struct {
	MediaIDs []int64  `json:"media_ids"`
	Mode     string   `json:"mode"`
	Tags     []string `json:"tags"`
}

type documentTagTarget struct {
	MediaID   int64
	LibraryID int64
	FilePath  string
}

type documentTagBatchItem struct {
	MediaID int64    `json:"media_id"`
	Tags    []string `json:"tags"`
}

type documentTagFacetDelta struct {
	Tag   string `json:"tag"`
	Delta int    `json:"delta"`
}

var (
	errDocumentTagTargetNotFound = errors.New("document tag target not found")
	errDocumentTagAccessDenied   = errors.New("document tag access denied")
	errDocumentTagLimit          = errors.New("document tag limit exceeded")
)

func normalizeDocumentTags(tags []string) ([]string, error) {
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag == "" || !utf8.ValidString(tag) || len([]byte(tag)) > 64 || strings.IndexFunc(tag, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("invalid tag")
		}
		duplicate := false
		for _, existing := range out {
			if strings.EqualFold(existing, tag) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, tag)
		}
	}
	return out, nil
}

func normalizeDocumentTagRequest(body batchUpdateDocumentTagsBody) ([]int64, []string, error) {
	if body.Mode != "add" && body.Mode != "remove" && body.Mode != "replace" {
		return nil, nil, fmt.Errorf("mode must be add, remove, or replace")
	}
	ids := make([]int64, 0, len(body.MediaIDs))
	seen := make(map[int64]struct{}, len(body.MediaIDs))
	for _, id := range body.MediaIDs {
		if id <= 0 {
			return nil, nil, fmt.Errorf("media_ids must be positive")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 || len(ids) > 200 {
		return nil, nil, fmt.Errorf("media_ids must contain 1 to 200 unique ids")
	}
	tags, err := normalizeDocumentTags(body.Tags)
	if err != nil || len(tags) == 0 {
		return nil, nil, fmt.Errorf("tags must contain at least one valid tag")
	}
	if len(tags) > 50 {
		return nil, nil, fmt.Errorf("tags must contain at most 50 unique tags")
	}
	return ids, tags, nil
}

func documentTagPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func documentTagAccessAllowed(profile userPermissionProfile, target documentTagTarget, now time.Time) bool {
	if strings.EqualFold(profile.LibraryScope, "selected") {
		if _, ok := profile.AllowedLibraryIDs[target.LibraryID]; !ok {
			return false
		}
		if folders := profile.AllowedLibraryFolders[target.LibraryID]; len(folders) > 0 && !pathMatchesAnyFolder(target.FilePath, folders) {
			return false
		}
	}
	return !profile.ParentalEnabled || withinAllowedTimeWindow(profile.AllowedTimeStart, profile.AllowedTimeEnd, profile.ParentalPlans, now)
}

func applyDocumentTagMode(current, requested []string, mode string) []string {
	result := make([]string, 0, len(current)+len(requested))
	contains := func(values []string, value string) bool {
		for _, candidate := range values {
			if strings.EqualFold(candidate, value) {
				return true
			}
		}
		return false
	}
	switch mode {
	case "replace":
		result = append(result, requested...)
	case "remove":
		for _, tag := range current {
			if !contains(requested, tag) {
				result = append(result, tag)
			}
		}
	case "add":
		result = append(result, current...)
		for _, tag := range requested {
			if !contains(result, tag) {
				result = append(result, tag)
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := strings.ToLower(result[i]), strings.ToLower(result[j])
		if left == right {
			return result[i] < result[j]
		}
		return left < right
	})
	return result
}

func documentTagTargetsFrom(ctx context.Context, q permissionQueryer, ids []int64) (map[int64]documentTagTarget, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT id, library_id, COALESCE(file_path,'') FROM media WHERE id IN (`+documentTagPlaceholders(len(ids))+`) AND file_type='document' AND status='active' AND publication_state IN ('published','degraded')`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make(map[int64]documentTagTarget, len(ids))
	for rows.Next() {
		var target documentTagTarget
		if err = rows.Scan(&target.MediaID, &target.LibraryID, &target.FilePath); err != nil {
			return nil, err
		}
		targets[target.MediaID] = target
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(targets) != len(ids) {
		return nil, errDocumentTagTargetNotFound
	}
	return targets, nil
}

func authorizeDocumentTagTargets(profile userPermissionProfile, targets map[int64]documentTagTarget, ids []int64, now time.Time) error {
	for _, id := range ids {
		if !documentTagAccessAllowed(profile, targets[id], now) {
			return errDocumentTagAccessDenied
		}
	}
	return nil
}

func (h *Handler) BatchUpdateDocumentTags(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	var body batchUpdateDocumentTagsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"code": "document_tags_body_too_large"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}
	ids, tags, err := normalizeDocumentTagRequest(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	apiClient := middleware.IsAPIClient(c)
	uid := middleware.UserID(c)
	if !apiClient && uid <= 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	preflightTargets, err := documentTagTargetsFrom(ctx, h.App.DB, ids)
	if err == nil && !apiClient {
		var profile userPermissionProfile
		profile, err = loadUserPermissionProfileFrom(ctx, h.App.DB, uid)
		if err == nil {
			err = authorizeDocumentTagTargets(profile, preflightTargets, ids, time.Now())
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			c.JSON(http.StatusRequestTimeout, gin.H{"code": "document_tags_canceled"})
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			c.JSON(http.StatusGatewayTimeout, gin.H{"code": "document_tags_timeout"})
		case errors.Is(err, errDocumentTagTargetNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		case errors.Is(err, errDocumentTagAccessDenied):
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	var result []documentTagBatchItem
	var facetDeltas []documentTagFacetDelta
	err = store.WithBusyRetry(ctx, nil, func() error {
		result = nil
		facetDeltas = nil
		conn, txErr := h.App.DB.Conn(ctx)
		if txErr != nil {
			return txErr
		}
		var originalBusyTimeout int64
		if txErr = conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&originalBusyTimeout); txErr != nil {
			conn.Close()
			return txErr
		}
		remaining := int64(2900)
		if deadline, ok := ctx.Deadline(); ok {
			remaining = time.Until(deadline).Milliseconds() - 100
			if remaining < 1 {
				remaining = 1
			}
		}
		if _, txErr = conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, remaining)); txErr != nil {
			conn.Close()
			return txErr
		}
		defer func() {
			restoreCtx, cancelRestore := context.WithTimeout(context.Background(), time.Second)
			defer cancelRestore()
			_, _ = conn.ExecContext(restoreCtx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, originalBusyTimeout))
			_ = conn.Close()
		}()
		tx, txErr := conn.BeginTx(ctx, nil)
		if txErr != nil {
			return txErr
		}
		defer tx.Rollback()
		if _, txErr = tx.ExecContext(ctx, `UPDATE document_tag SET tag=tag WHERE 0`); txErr != nil {
			return txErr
		}

		var profile userPermissionProfile
		if !apiClient {
			profile, txErr = loadUserPermissionProfileFrom(ctx, tx, uid)
			if txErr != nil {
				return txErr
			}
		}
		targets, txErr := documentTagTargetsFrom(ctx, tx, ids)
		if txErr != nil {
			return txErr
		}
		if !apiClient {
			if txErr = authorizeDocumentTagTargets(profile, targets, ids, time.Now()); txErr != nil {
				return txErr
			}
		}
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}

		current := make(map[int64][]string, len(ids))
		rows, txErr := tx.QueryContext(ctx, `SELECT media_id, tag FROM document_tag WHERE media_id IN (`+documentTagPlaceholders(len(ids))+`) ORDER BY media_id, tag COLLATE NOCASE, tag`, args...)
		if txErr != nil {
			return txErr
		}
		for rows.Next() {
			var id int64
			var tag string
			if txErr = rows.Scan(&id, &tag); txErr != nil {
				rows.Close()
				return txErr
			}
			if strings.TrimSpace(tag) != "" {
				current[id] = append(current[id], tag)
			}
		}
		if txErr = rows.Err(); txErr != nil {
			rows.Close()
			return txErr
		}
		if txErr = rows.Close(); txErr != nil {
			return txErr
		}

		result = make([]documentTagBatchItem, 0, len(ids))
		facetCounts := make(map[string]int)
		rowsToInsert := make([]documentTagBatchItem, 0, len(ids))
		for _, id := range ids {
			finalTags := applyDocumentTagMode(current[id], tags, body.Mode)
			if len(finalTags) > 50 {
				return errDocumentTagLimit
			}
			for _, tag := range current[id] {
				facetCounts[strings.ToLower(tag)]--
			}
			for _, tag := range finalTags {
				facetCounts[strings.ToLower(tag)]++
			}
			item := documentTagBatchItem{MediaID: id, Tags: finalTags}
			result = append(result, item)
			rowsToInsert = append(rowsToInsert, item)
		}
		facetDeltas = make([]documentTagFacetDelta, 0, len(facetCounts))
		for tag, delta := range facetCounts {
			if delta != 0 {
				facetDeltas = append(facetDeltas, documentTagFacetDelta{Tag: tag, Delta: delta})
			}
		}
		sort.Slice(facetDeltas, func(i, j int) bool { return facetDeltas[i].Tag < facetDeltas[j].Tag })
		idsJSON, txErr := json.Marshal(ids)
		if txErr != nil {
			return txErr
		}
		if _, txErr = tx.ExecContext(ctx, `DELETE FROM document_tag WHERE media_id IN (SELECT CAST(value AS INTEGER) FROM json_each(?))`, string(idsJSON)); txErr != nil {
			return txErr
		}
		rowsJSON, txErr := json.Marshal(rowsToInsert)
		if txErr != nil {
			return txErr
		}
		if _, txErr = tx.ExecContext(ctx, `
			INSERT INTO document_tag(media_id, tag)
			SELECT CAST(json_extract(item.value, '$.media_id') AS INTEGER), CAST(tag.value AS TEXT)
			FROM json_each(?) AS item
			JOIN json_each(json_extract(item.value, '$.tags')) AS tag`, string(rowsJSON)); txErr != nil {
			return txErr
		}
		return tx.Commit()
	})
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			c.JSON(http.StatusRequestTimeout, gin.H{"code": "document_tags_canceled"})
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			c.JSON(http.StatusGatewayTimeout, gin.H{"code": "document_tags_timeout"})
		case errors.Is(err, errDocumentTagTargetNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		case errors.Is(err, errDocumentTagAccessDenied):
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		case errors.Is(err, errDocumentTagLimit):
			c.JSON(http.StatusBadRequest, gin.H{"error": "a document cannot have more than 50 tags"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(result), "items": result, "facet_deltas": facetDeltas})
}

type updateDocumentMetaBody struct {
	Title       *string  `json:"title"`
	Author      *string  `json:"author"`
	Publisher   *string  `json:"publisher"`
	Year        *int     `json:"year"`
	Description *string  `json:"description"`
	Tags        []string `json:"tags"`
}

func (h *Handler) UpdateDocumentMeta(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var metaJSON, fileType sql.NullString
	if err := h.App.DB.QueryRow(`SELECT meta_json, file_type FROM media WHERE id = ?`, id).Scan(&metaJSON, &fileType); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if fileType.String != "document" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not a document"})
		return
	}
	var body updateDocumentMetaBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	root := map[string]any{}
	if strings.TrimSpace(metaJSON.String) != "" {
		_ = json.Unmarshal([]byte(metaJSON.String), &root)
	}
	doc, _ := root["document"].(map[string]any)
	if doc == nil {
		doc = map[string]any{}
	}
	if body.Author != nil {
		doc["author"] = strings.TrimSpace(*body.Author)
	}
	if body.Publisher != nil {
		doc["publisher"] = strings.TrimSpace(*body.Publisher)
	}
	if body.Year != nil {
		doc["year"] = *body.Year
	}
	if body.Description != nil {
		doc["description"] = strings.TrimSpace(*body.Description)
	}
	root["document"] = doc
	newTitle := ""
	if body.Title != nil {
		newTitle = strings.TrimSpace(*body.Title)
		root["title"] = newTitle
	}
	out, _ := json.Marshal(root)
	tx, err := h.App.DB.BeginTx(c.Request.Context(), nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	if newTitle != "" {
		if _, err = tx.ExecContext(c.Request.Context(), `UPDATE media SET title=? WHERE id=?`, newTitle, id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err = store.UpdateMediaMetaAndPhotoTime(c.Request.Context(), tx, id, string(out)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err = tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if body.Tags != nil {
		_, _ = h.App.DB.Exec(`DELETE FROM document_tag WHERE media_id = ?`, id)
		for _, tag := range body.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			_, _ = h.App.DB.Exec(`INSERT OR IGNORE INTO document_tag (media_id, tag) VALUES (?, ?)`, id, tag)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func respondDocumentQueryError(c *gin.Context, ctx context.Context, err error, prefix string) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		c.JSON(http.StatusGatewayTimeout, gin.H{"code": prefix + "_timeout"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"code": prefix + "_internal"})
}
func (h *Handler) ListScanLogs(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	taskID := strings.TrimSpace(c.Query("task_id"))
	libID := strings.TrimSpace(c.Query("library_id"))
	limit := 200
	q := `SELECT id, scan_task_id, library_id, file_path, action, message, created_at FROM scan_log WHERE 1=1`
	args := []any{}
	if taskID != "" {
		q += ` AND scan_task_id = ?`
		args = append(args, taskID)
	}
	if libID != "" {
		q += ` AND library_id = ?`
		args = append(args, libID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := h.App.DB.QueryContext(ctx, q, args...)
	if err != nil {
		respondDocumentQueryError(c, ctx, err, "scan_logs")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var id, task, library sql.NullInt64
		var path, action, msg, created sql.NullString
		if err := rows.Scan(&id, &task, &library, &path, &action, &msg, &created); err != nil {
			rows.Close()
			respondDocumentQueryError(c, ctx, err, "scan_logs")
			return
		}
		items = append(items, gin.H{
			"id": id.Int64, "scan_task_id": task.Int64, "library_id": library.Int64,
			"file_path": path.String, "action": action.String, "message": msg.String, "created_at": created.String,
		})
	}
	if err := rows.Err(); err != nil {
		respondDocumentQueryError(c, ctx, err, "scan_logs")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) loadDocumentTags(mediaID int64) ([]string, error) {
	rows, err := h.App.DB.Query(`SELECT tag FROM document_tag WHERE media_id = ? ORDER BY tag COLLATE NOCASE`, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make([]string, 0, 8)
	for rows.Next() {
		var tag sql.NullString
		if rows.Scan(&tag) == nil && tag.String != "" {
			tags = append(tags, tag.String)
		}
	}
	return tags, nil
}

type documentMetaView struct {
	Author      string
	Publisher   string
	Year        int
	Description string
	Format      string
	PageCount   int
	FileSize    int64
	ModifiedAt  string
	Language    string
}

func parseDocumentMetaJSON(raw string) documentMetaView {
	var root map[string]any
	if strings.TrimSpace(raw) == "" {
		return documentMetaView{}
	}
	_ = json.Unmarshal([]byte(raw), &root)
	doc, _ := root["document"].(map[string]any)
	if doc == nil {
		return documentMetaView{}
	}
	out := documentMetaView{}
	if v, ok := doc["author"].(string); ok {
		out.Author = v
	}
	if v, ok := doc["publisher"].(string); ok {
		out.Publisher = v
	}
	if v, ok := doc["description"].(string); ok {
		out.Description = v
	}
	if v, ok := doc["format"].(string); ok {
		out.Format = v
	}
	if v, ok := doc["modified_at"].(string); ok {
		out.ModifiedAt = v
	}
	if v, ok := doc["language"].(string); ok {
		out.Language = v
	}
	switch v := doc["year"].(type) {
	case float64:
		out.Year = int(v)
	case int:
		out.Year = v
	}
	switch v := doc["page_count"].(type) {
	case float64:
		out.PageCount = int(v)
	case int:
		out.PageCount = v
	}
	switch v := doc["file_size"].(type) {
	case float64:
		out.FileSize = int64(v)
	case int64:
		out.FileSize = v
	}
	return out
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
