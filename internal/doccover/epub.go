package doccover

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ExtractEPUBCover copies the embedded cover image from an EPUB to cachePath.
func ExtractEPUBCover(epubPath, cachePath string) string {
	if strings.TrimSpace(epubPath) == "" || strings.TrimSpace(cachePath) == "" {
		return ""
	}
	r, err := zip.OpenReader(epubPath)
	if err != nil {
		return ""
	}
	defer r.Close()
	coverFile := findEPUBCoverFile(r)
	if coverFile == nil {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return ""
	}
	out, err := os.Create(cachePath)
	if err != nil {
		return ""
	}
	defer out.Close()
	rc, err := coverFile.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return ""
	}
	return cachePath
}

func findEPUBCoverFile(r *zip.ReadCloser) *zip.File {
	if r == nil {
		return nil
	}
	if href := resolveEPUBCoverHref(r); href != "" {
		if f := zipFileByPath(r.File, href); f != nil {
			return f
		}
	}
	var coverFile *zip.File
	for _, f := range r.File {
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "cover") && isImageExt(name) {
			coverFile = f
			break
		}
	}
	if coverFile == nil {
		for _, f := range r.File {
			if isImageExt(strings.ToLower(f.Name)) {
				coverFile = f
				break
			}
		}
	}
	return coverFile
}

func resolveEPUBCoverHref(r *zip.ReadCloser) string {
	opfPath := findEPUBOPFPath(r)
	if opfPath == "" {
		return ""
	}
	opfData := readZipText(r, opfPath, 512*1024)
	if len(opfData) == 0 {
		return ""
	}
	var pkg epubOPF
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return ""
	}
	coverID := ""
	for _, meta := range pkg.Metadata.Metas {
		name := strings.ToLower(strings.TrimSpace(meta.Name))
		if name == "cover" && strings.TrimSpace(meta.Content) != "" {
			coverID = strings.TrimSpace(meta.Content)
			break
		}
	}
	opfDir := filepath.ToSlash(filepath.Dir(opfPath))
	for _, item := range pkg.Manifest.Items {
		id := strings.TrimSpace(item.ID)
		props := strings.ToLower(item.Properties)
		if coverID != "" && id == coverID {
			return joinZipPath(opfDir, item.Href)
		}
		if strings.Contains(props, "cover-image") {
			return joinZipPath(opfDir, item.Href)
		}
	}
	return ""
}

type epubOPF struct {
	Metadata epubOPFMetadata `xml:"metadata"`
	Manifest epubOPFManifest `xml:"manifest"`
}

type epubOPFMetadata struct {
	Metas []epubOPFMeta `xml:"meta"`
}

type epubOPFMeta struct {
	Name    string `xml:"name,attr"`
	Content string `xml:"content,attr"`
}

type epubOPFManifest struct {
	Items []epubOPFItem `xml:"item"`
}

type epubOPFItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	Properties string `xml:"properties,attr"`
}

func findEPUBOPFPath(r *zip.ReadCloser) string {
	data := readZipText(r, "META-INF/container.xml", 32*1024)
	if len(data) == 0 {
		return ""
	}
	re := regexp.MustCompile(`full-path="([^"]+)"`)
	if m := re.FindSubmatch(data); len(m) == 2 {
		return string(m[1])
	}
	return ""
}

func readZipText(r *zip.ReadCloser, name string, limit int64) []byte {
	f := zipFileByPath(r.File, name)
	if f == nil {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit))
	if err != nil {
		return nil
	}
	return data
}

func zipFileByPath(files []*zip.File, want string) *zip.File {
	want = filepath.ToSlash(want)
	for _, f := range files {
		if f.Name == want {
			return f
		}
	}
	wantLower := strings.ToLower(want)
	for _, f := range files {
		if strings.ToLower(f.Name) == wantLower {
			return f
		}
	}
	return nil
}

func joinZipPath(dir, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if dir == "" || dir == "." {
		return filepath.ToSlash(href)
	}
	return filepath.ToSlash(filepath.Join(dir, href))
}

func isImageExt(name string) bool {
	return strings.HasSuffix(name, ".jpg") ||
		strings.HasSuffix(name, ".jpeg") ||
		strings.HasSuffix(name, ".png") ||
		strings.HasSuffix(name, ".webp")
}
