package docparse

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"knox-media/internal/keystore"
	"knox-media/internal/storage"
	"knox-media/pkg/fileutil"
)

// DocumentMeta holds normalized document/e-book metadata extracted during scan.
type DocumentMeta struct {
	Title       string   `json:"title,omitempty"`
	Author      string   `json:"author,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
	Year        int      `json:"year,omitempty"`
	Language    string   `json:"language,omitempty"`
	Description string   `json:"description,omitempty"`
	MimeType    string   `json:"mime_type,omitempty"`
	Format      string   `json:"format,omitempty"`
	FileSize    int64    `json:"file_size,omitempty"`
	ModifiedAt  string   `json:"modified_at,omitempty"`
	PageCount   int      `json:"page_count,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	TextPreview string   `json:"text_preview,omitempty"`
	HasCover    bool     `json:"has_cover,omitempty"`
}

// IsDocumentLibraryType reports whether the library type should use document scanning.
func IsDocumentLibraryType(libraryType string) bool {
	return strings.EqualFold(strings.TrimSpace(libraryType), "document")
}

// ShouldScanFile reports whether a discovered file should be ingested for document libraries.
func ShouldScanFile(libraryType, fileType string) bool {
	if IsDocumentLibraryType(libraryType) {
		return fileType == "document"
	}
	return false
}

// ShouldSkipPath reports whether a path should be skipped during document scan.
func ShouldSkipPath(name string, size int64, excludePatterns []string) bool {
	base := filepath.Base(name)
	if strings.HasPrefix(base, ".") {
		return true
	}
	if size > 0 && size < 1024 {
		return true
	}
	rel := filepath.ToSlash(name)
	for _, p := range excludePatterns {
		if matchExclude(rel, p) {
			return true
		}
	}
	return false
}

// ParseFromFile extracts metadata from a local document file.
func ParseFromFile(filePath string) DocumentMeta {
	meta := DocumentMeta{
		Title:  strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)),
		Format: fileutil.GuessDocumentFormat(filePath),
	}
	meta.MimeType = guessMime(filePath, meta.Format)
	if st, err := os.Stat(filePath); err == nil {
		meta.FileSize = st.Size()
		meta.ModifiedAt = st.ModTime().UTC().Format(time.RFC3339)
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".epub":
		if em := parseEPUBMeta(filePath); em != nil {
			mergeEPUB(&meta, em)
		}
	case ".pdf":
		if pm := parsePDFMeta(filePath); pm != nil {
			mergePDF(&meta, pm)
		}
	case ".txt", ".md", ".mdx", ".csv":
		meta.TextPreview = readTextPreview(filePath, 12000)
	case ".html", ".htm":
		meta.TextPreview = stripHTMLPreview(readTextPreview(filePath, 12000))
	}
	meta.Title = PickDocumentTitle(filePath, meta.Title)
	if meta.Author != "" {
		meta.Author = SanitizeMetadataText(meta.Author)
	}
	return meta
}

// ParseForMedia extracts document metadata, materializing Knox .enc to a temp file when needed.
func ParseForMedia(db *sql.DB, vault *keystore.Vault, mediaID int64, filePath string) DocumentMeta {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return DocumentMeta{}
	}
	work := filePath
	if storage.InputNeedsPipe(db, mediaID, filePath) {
		tmp, cleanup, err := storage.MaterializePlaintextTemp(db, vault, mediaID, filePath)
		if err != nil {
			return DocumentMeta{
				Title:  strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)),
				Format: fileutil.GuessDocumentFormat(filePath),
			}
		}
		defer cleanup()
		work = tmp
	}
	return ParseFromFile(work)
}

// MergeDocumentMetaJSON merges document metadata into existing meta_json.
func MergeDocumentMetaJSON(raw string, doc DocumentMeta) string {
	var root map[string]any
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	b, _ := json.Marshal(doc)
	var docMap map[string]any
	_ = json.Unmarshal(b, &docMap)
	root["document"] = docMap
	if strings.TrimSpace(doc.Title) != "" {
		root["title"] = doc.Title
	}
	out, _ := json.Marshal(root)
	return string(out)
}

func guessMime(path, format string) string {
	if m := fileutil.GuessDocumentMIME(path); m != "" {
		return m
	}
	switch strings.ToLower(format) {
	case "pdf":
		return "application/pdf"
	case "epub":
		return "application/epub+zip"
	case "txt":
		return "text/plain"
	case "md", "mdx":
		return "text/markdown"
	case "html", "htm":
		return "text/html"
	case "csv":
		return "text/csv"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	default:
		return "application/octet-stream"
	}
}

func mergeEPUB(dst *DocumentMeta, em *epubMeta) {
	if em == nil {
		return
	}
	if em.Title != "" {
		dst.Title = em.Title
	}
	if em.Author != "" {
		dst.Author = em.Author
	}
	if em.Publisher != "" {
		dst.Publisher = em.Publisher
	}
	if em.Language != "" {
		dst.Language = em.Language
	}
	if em.Description != "" {
		dst.Description = em.Description
	}
	if em.Year > 0 {
		dst.Year = em.Year
	}
	dst.HasCover = em.HasCover
}

func mergePDF(dst *DocumentMeta, pm *pdfMeta) {
	if pm == nil {
		return
	}
	if pm.Title != "" {
		dst.Title = pm.Title
	}
	if pm.Author != "" {
		dst.Author = pm.Author
	}
	if pm.Subject != "" {
		dst.Description = pm.Subject
	}
	if pm.PageCount > 0 {
		dst.PageCount = pm.PageCount
	}
}
