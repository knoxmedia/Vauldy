package doctrans

import (
	"path/filepath"
	"runtime"
	"strings"
)

const (
	DefaultDirRel     = "tools/doctran"
	DefaultSofficeWin = "tools/doctran/LibreOfficePortable/App/libreoffice/program/soffice.exe"
	DefaultSofficeLin = "tools/doctran/libreoffice/program/soffice"
)

var officeExts = map[string]struct{}{
	".doc": {}, ".docx": {}, ".xls": {}, ".xlsx": {}, ".ppt": {}, ".pptx": {},
}

var officeFormats = map[string]struct{}{
	"doc": {}, "docx": {}, "xls": {}, "xlsx": {}, "ppt": {}, "pptx": {},
}

// IsOfficeFormat reports whether the extension needs LibreOffice conversion for preview.
func IsOfficeFormat(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	_, ok := officeExts[ext]
	return ok
}

// IsOfficeDocument reports office format from path extension and/or catalog format (e.g. doc on .enc).
func IsOfficeDocument(path, format string) bool {
	if IsOfficeFormat(path) {
		return true
	}
	f := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
	_, ok := officeFormats[f]
	return ok
}

func DefaultSofficeRel() string {
	if runtime.GOOS == "windows" {
		return DefaultSofficeWin
	}
	return DefaultSofficeLin
}

// ResolvePath resolves a configured path relative to mediaRoot.
func ResolvePath(mediaRoot, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if mediaRoot == "" {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(mediaRoot, filepath.FromSlash(p)))
}
