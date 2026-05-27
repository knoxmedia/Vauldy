package docparse

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"regexp"
	"strings"
)

type epubMeta struct {
	Title       string
	Author      string
	Publisher   string
	Language    string
	Description string
	Year        int
	HasCover    bool
}

func parseEPUBMeta(path string) *epubMeta {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil
	}
	defer r.Close()
	opfPath := findOPFPath(r)
	if opfPath == "" {
		return nil
	}
	var opfFile *zip.File
	for _, f := range r.File {
		if f.Name == opfPath {
			opfFile = f
			break
		}
	}
	if opfFile == nil {
		return nil
	}
	rc, err := opfFile.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 512*1024))
	if err != nil {
		return nil
	}
	return parseOPF(data, r)
}

func findOPFPath(r *zip.ReadCloser) string {
	for _, f := range r.File {
		if f.Name == "META-INF/container.xml" {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, _ := io.ReadAll(io.LimitReader(rc, 32*1024))
			_ = rc.Close()
			re := regexp.MustCompile(`full-path="([^"]+)"`)
			if m := re.FindSubmatch(data); len(m) == 2 {
				return string(m[1])
			}
		}
	}
	return ""
}

type opfPackage struct {
	XMLName  xml.Name    `xml:"package"`
	Metadata opfMetadata `xml:"metadata"`
	Manifest opfManifest `xml:"manifest"`
}

type opfMetadata struct {
	Titles       []string `xml:"title"`
	Creators     []string `xml:"creator"`
	Publishers   []string `xml:"publisher"`
	Languages    []string `xml:"language"`
	Descriptions []string `xml:"description"`
	Dates        []string `xml:"date"`
}

type opfManifest struct {
	Items []opfItem `xml:"item"`
}

type opfItem struct {
	ID   string `xml:"id,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"media-type,attr"`
}

func parseOPF(data []byte, r *zip.ReadCloser) *epubMeta {
	var pkg opfPackage
	if err := xml.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	em := &epubMeta{}
	if len(pkg.Metadata.Titles) > 0 {
		em.Title = strings.TrimSpace(pkg.Metadata.Titles[0])
	}
	if len(pkg.Metadata.Creators) > 0 {
		em.Author = strings.TrimSpace(pkg.Metadata.Creators[0])
	}
	if len(pkg.Metadata.Publishers) > 0 {
		em.Publisher = strings.TrimSpace(pkg.Metadata.Publishers[0])
	}
	if len(pkg.Metadata.Languages) > 0 {
		em.Language = strings.TrimSpace(pkg.Metadata.Languages[0])
	}
	if len(pkg.Metadata.Descriptions) > 0 {
		em.Description = strings.TrimSpace(stripHTMLPreview(pkg.Metadata.Descriptions[0]))
	}
	for _, d := range pkg.Metadata.Dates {
		yearRe := regexp.MustCompile(`(\d{4})`)
		if m := yearRe.FindStringSubmatch(d); len(m) == 2 {
			em.Year = atoi(m[1])
			break
		}
	}
	for _, item := range pkg.Manifest.Items {
		t := strings.ToLower(item.Type)
		if strings.Contains(t, "image") && (strings.Contains(strings.ToLower(item.ID), "cover") || strings.Contains(strings.ToLower(item.Href), "cover")) {
			em.HasCover = true
			break
		}
	}
	return em
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
