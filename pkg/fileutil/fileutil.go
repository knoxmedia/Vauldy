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
}

var imageExts = map[string]struct{}{
	".jpg": {}, ".jpeg": {}, ".png": {}, ".gif": {}, ".webp": {}, ".bmp": {}, ".heic": {},
}

var docExts = map[string]struct{}{
	".pdf": {}, ".doc": {}, ".docx": {}, ".xls": {}, ".xlsx": {}, ".ppt": {}, ".pptx": {}, ".txt": {}, ".md": {},
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
