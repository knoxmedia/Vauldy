package fileutil

import (
	"path/filepath"
	"strings"
)

var videoExts = map[string]struct{}{
	".mp4": {}, ".mkv": {}, ".avi": {}, ".mov": {}, ".wmv": {}, ".flv": {}, ".webm": {}, ".m4v": {}, ".mpeg": {}, ".mpg": {},
}

var audioExts = map[string]struct{}{
	".mp3": {}, ".flac": {}, ".wav": {}, ".aac": {}, ".ogg": {}, ".m4a": {}, ".wma": {},
	".aiff": {}, ".aif": {}, ".ape": {},
}

var imageExts = map[string]struct{}{
	".jpg": {}, ".jpeg": {}, ".png": {}, ".gif": {}, ".webp": {}, ".bmp": {}, ".heic": {}, ".heif": {},
	".tif": {}, ".tiff": {}, ".svg": {},
	".cr2": {}, ".nef": {}, ".arw": {}, ".dng": {}, ".raf": {}, ".orf": {}, ".rw2": {},
}

var docExts = map[string]struct{}{
	".pdf": {}, ".doc": {}, ".docx": {}, ".xls": {}, ".xlsx": {}, ".ppt": {}, ".pptx": {},
	".txt": {}, ".md": {}, ".mdx": {}, ".html": {}, ".htm": {}, ".csv": {}, ".rtf": {},
	".epub": {}, ".mobi": {}, ".azw": {}, ".azw3": {},
}

var docMIME = map[string]string{
	".pdf":  "application/pdf",
	".epub": "application/epub+zip",
	".txt":  "text/plain",
	".md":   "text/markdown",
	".mdx":  "text/markdown",
	".html": "text/html",
	".htm":  "text/html",
	".csv":  "text/csv",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".rtf":  "application/rtf",
	".mobi": "application/x-mobipocket-ebook",
}

func GuessFileType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case has(videoExts, ext):
		return "video"
	case has(audioExts, ext):
		return "audio"
	case has(imageExts, ext):
		return "image"
	case has(docExts, ext):
		return "document"
	default:
		return "other"
	}
}

func has(m map[string]struct{}, k string) bool {
	_, ok := m[k]
	return ok
}

// GuessDocumentFormat returns a short format label from file extension.
func GuessDocumentFormat(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return "unknown"
	}
	return strings.TrimPrefix(ext, ".")
}

// GuessDocumentMIME returns MIME type for known document extensions.
func GuessDocumentMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if m, ok := docMIME[ext]; ok {
		return m
	}
	return ""
}

// IsDocumentExtension reports whether the extension is a supported document type.
func IsDocumentExtension(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	_, ok := docExts[ext]
	return ok
}
