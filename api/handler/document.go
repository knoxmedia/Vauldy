package handler

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/doccover"
	"knox-media/internal/doctrans"
	"knox-media/internal/storage"
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
		FROM library_node
		WHERE library_id = ? AND COALESCE(parent_path, '') = ?
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
	items, err := h.queryDocuments(libID, qp, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) queryDocuments(libID int64, qp documentListQuery, limit int) ([]gin.H, error) {
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
		WHERE m.library_id = ? AND m.file_type = 'document' AND m.status = 'active'`
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
		q += ` AND EXISTS (SELECT 1 FROM document_tag dt WHERE dt.media_id = m.id AND dt.tag = ?)`
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

	rows, err := h.App.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var id int64
		var fileID, title, path, format, status, created, author, publisher, modified, desc, docFormat, lastRead sql.NullString
		var year, fileSize, pageCount sql.NullInt64
		if rows.Scan(&id, &fileID, &title, &path, &format, &status, &created, &author, &publisher, &year, &fileSize, &modified, &desc, &docFormat, &pageCount, &lastRead) != nil {
			continue
		}
		tags, _ := h.loadDocumentTags(id)
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
			"tags":         tags,
			"cover_url":    fmt.Sprintf("/api/v1/media/%d/document/cover.jpg", id),
		})
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
			SELECT COALESCE(NULLIF(json_extract(meta_json, '$.document.author'), ''), '未知作者') AS name, COUNT(1) AS cnt
			FROM media WHERE library_id = ? AND file_type = 'document' AND status = 'active'
			GROUP BY name ORDER BY cnt DESC, name COLLATE NOCASE LIMIT 200`
	case "format":
		q = `
			SELECT LOWER(COALESCE(NULLIF(json_extract(meta_json, '$.document.format'), ''), format, 'unknown')) AS name, COUNT(1) AS cnt
			FROM media WHERE library_id = ? AND file_type = 'document' AND status = 'active'
			GROUP BY name ORDER BY cnt DESC, name COLLATE NOCASE LIMIT 50`
	case "year":
		q = `
			SELECT CAST(COALESCE(json_extract(meta_json, '$.document.year'), 0) AS TEXT) AS name, COUNT(1) AS cnt
			FROM media WHERE library_id = ? AND file_type = 'document' AND status = 'active'
			GROUP BY name HAVING name != '0' ORDER BY name DESC LIMIT 100`
	case "tag":
		q = `
			SELECT dt.tag AS name, COUNT(1) AS cnt
			FROM document_tag dt
			JOIN media m ON m.id = dt.media_id
			WHERE m.library_id = ? AND m.file_type = 'document' AND m.status = 'active'
			GROUP BY dt.tag ORDER BY cnt DESC, dt.tag COLLATE NOCASE LIMIT 200`
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
		WHERE rp.user_id = ? AND m.file_type = 'document' AND m.status = 'active'`
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
			"format": firstNonEmpty(docFormat.String, format.String),
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
	if !middleware.IsAPIClient(c) {
		uid := middleware.UserID(c)
		if uid <= 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		profile, err := h.loadUserPermissionProfile(uid)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !profile.CanDownload {
			c.JSON(http.StatusForbidden, gin.H{"error": "download denied"})
			return
		}
	}
	var body batchDownloadBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
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
		if err := h.App.DB.QueryRow(`SELECT file_path, title, file_type FROM media WHERE id = ? AND status = 'active'`, id).Scan(&p, &title, &fileType); err != nil {
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
		  AND m.file_type = 'document' AND m.status = 'active'`,
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
	if newTitle != "" {
		_, _ = h.App.DB.Exec(`UPDATE media SET title = ?, meta_json = ? WHERE id = ?`, newTitle, string(out), id)
	} else {
		_, _ = h.App.DB.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, string(out), id)
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

func (h *Handler) ListScanLogs(c *gin.Context) {
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
	rows, err := h.App.DB.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit)
	for rows.Next() {
		var id, task, library sql.NullInt64
		var path, action, msg, created sql.NullString
		if rows.Scan(&id, &task, &library, &path, &action, &msg, &created) != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id.Int64, "scan_task_id": task.Int64, "library_id": library.Int64,
			"file_path": path.String, "action": action.String, "message": msg.String, "created_at": created.String,
		})
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
