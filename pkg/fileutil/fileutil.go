package fileutil

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
)

var defaultVideoExts = map[string]struct{}{
	// Common containers / progressive
	".mp4": {}, ".m4v": {}, ".mkv": {}, ".webm": {}, ".avi": {}, ".mov": {}, ".wmv": {}, ".flv": {}, ".f4v": {},
	".mpeg": {}, ".mpg": {}, ".mpe": {}, ".m2v": {}, ".mpv": {},
	// Broadcast / optical / camcorder
	".ts": {}, ".m2ts": {}, ".mts": {}, ".tp": {}, ".trp": {}, ".vob": {}, ".mod": {}, ".tod": {},
	".3gp": {}, ".3g2": {}, ".ogv": {}, ".divx": {}, ".xvid": {}, ".asf": {},
	".rm": {}, ".rmvb": {}, ".mxf": {}, ".wtv": {}, ".dvr-ms": {},
}

var defaultAudioExts = map[string]struct{}{
	// Lossy / lossless common
	".mp3": {}, ".flac": {}, ".wav": {}, ".aac": {}, ".ogg": {}, ".oga": {}, ".opus": {},
	".m4a": {}, ".wma": {}, ".aiff": {}, ".aif": {}, ".ape": {}, ".wv": {}, ".mka": {},
	// Broadcast / high-res / niche
	".ac3": {}, ".eac3": {}, ".dts": {}, ".dtshd": {}, ".mp2": {}, ".amr": {},
	".ra": {}, ".tak": {}, ".tta": {}, ".caf": {}, ".dsf": {}, ".dff": {},
}

var defaultImageExts = map[string]struct{}{
	".jpg": {}, ".jpeg": {}, ".png": {}, ".gif": {}, ".webp": {}, ".bmp": {}, ".heic": {}, ".heif": {},
	".tif": {}, ".tiff": {}, ".svg": {},
	".cr2": {}, ".nef": {}, ".arw": {}, ".dng": {}, ".raf": {}, ".orf": {}, ".rw2": {},
}

var defaultDocExts = map[string]struct{}{
	".pdf": {}, ".doc": {}, ".docx": {}, ".xls": {}, ".xlsx": {}, ".ppt": {}, ".pptx": {},
	".txt": {}, ".md": {}, ".mdx": {}, ".html": {}, ".htm": {}, ".csv": {}, ".rtf": {},
	".epub": {}, ".mobi": {}, ".azw": {}, ".azw3": {},
}

var (
	mu        sync.RWMutex
	videoExts map[string]struct{}
	audioExts map[string]struct{}
	imageExts map[string]struct{}
	docExts   map[string]struct{}
)

func init() {
	ResetForTest()
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

// ExtensionConfig holds optional per-category extension overrides.
// A nil pointer keeps built-in defaults; a non-nil slice fully replaces that category (empty = match nothing).
type ExtensionConfig struct {
	Video    *[]string
	Audio    *[]string
	Image    *[]string
	Document *[]string
}

// ResetForTest restores built-in extension maps. Intended for t.Cleanup in tests.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	videoExts = copyExtMap(defaultVideoExts)
	audioExts = copyExtMap(defaultAudioExts)
	imageExts = copyExtMap(defaultImageExts)
	docExts = copyExtMap(defaultDocExts)
}

// Configure replaces active extension maps from cfg. Nil category pointers keep defaults.
func Configure(cfg ExtensionConfig) error {
	newVideo := copyExtMap(defaultVideoExts)
	newAudio := copyExtMap(defaultAudioExts)
	newImage := copyExtMap(defaultImageExts)
	newDoc := copyExtMap(defaultDocExts)

	if cfg.Video != nil {
		m, err := normalizeExts(*cfg.Video)
		if err != nil {
			return err
		}
		newVideo = m
	}
	if cfg.Audio != nil {
		m, err := normalizeExts(*cfg.Audio)
		if err != nil {
			return err
		}
		newAudio = m
	}
	if cfg.Image != nil {
		m, err := normalizeExts(*cfg.Image)
		if err != nil {
			return err
		}
		newImage = m
	}
	if cfg.Document != nil {
		m, err := normalizeExts(*cfg.Document)
		if err != nil {
			return err
		}
		newDoc = m
	}

	resolveDuplicateExts(newVideo, newAudio, newImage, newDoc)

	mu.Lock()
	defer mu.Unlock()
	videoExts = newVideo
	audioExts = newAudio
	imageExts = newImage
	docExts = newDoc
	return nil
}

func normalizeExts(list []string) (map[string]struct{}, error) {
	m := make(map[string]struct{}, len(list))
	for _, raw := range list {
		s := strings.TrimSpace(raw)
		if s == "" {
			return nil, fmt.Errorf("fileutil: blank extension entry")
		}
		s = strings.ToLower(s)
		if !strings.HasPrefix(s, ".") {
			s = "." + s
		}
		m[s] = struct{}{}
	}
	return m, nil
}

func resolveDuplicateExts(video, audio, image, doc map[string]struct{}) {
	type cat struct {
		name string
		m    map[string]struct{}
	}
	categories := []cat{
		{"video", video},
		{"audio", audio},
		{"image", image},
		{"document", doc},
	}
	seen := make(map[string]string)
	for _, c := range categories {
		for ext := range c.m {
			if prev, ok := seen[ext]; ok {
				log.Printf("fileutil: extension %q configured in both %s and %s; using %s", ext, prev, c.name, prev)
				delete(c.m, ext)
			} else {
				seen[ext] = c.name
			}
		}
	}
}

func copyExtMap(src map[string]struct{}) map[string]struct{} {
	dst := make(map[string]struct{}, len(src))
	for k := range src {
		dst[k] = struct{}{}
	}
	return dst
}

func GuessFileType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	mu.RLock()
	defer mu.RUnlock()
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
// It uses a static built-in MIME table and does not follow Configure() document
// overrides; newly added document extensions may return "".
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
	mu.RLock()
	defer mu.RUnlock()
	_, ok := docExts[ext]
	return ok
}
