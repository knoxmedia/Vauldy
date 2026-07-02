package docparse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecodePDFStringLiteralUTF16BE(t *testing.T) {
	// \376\377 = FE FF BOM, then UTF-16BE for "Unknown"
	lit := `\376\377\000U\000n\000k\000n\000o\000w\000n`
	raw := decodePDFStringLiteral(lit)
	got := pdfBytesToText(raw)
	if got != "Unknown" {
		t.Fatalf("expected Unknown, got %q (raw=% x)", got, raw)
	}
}

func TestPDFBytesToTextUTF16BEHex(t *testing.T) {
	raw, err := parsePDFHexString("FEFF56FD5BB6")
	if err != nil {
		t.Fatal(err)
	}
	got := pdfBytesToText(raw)
	if got != "国家" {
		t.Fatalf("expected 国家, got %q", got)
	}
}

func TestFindPDFStringFieldLiteral(t *testing.T) {
	data := `/Type /Catalog /Title(\376\377\000U\000n\000k\000n\000o\000w\000n) /Author(Test)`
	got, ok := findPDFStringField(data, "Title")
	if !ok {
		t.Fatal("expected title")
	}
	if got != "Unknown" {
		t.Fatalf("got %q", got)
	}
}

func TestFindPDFStringFieldHex(t *testing.T) {
	data := `/Title<FEFF56FD5BB6> /Count 10`
	got, ok := findPDFStringField(data, "Title")
	if !ok {
		t.Fatal("expected title")
	}
	if got != "国家" {
		t.Fatalf("got %q", got)
	}
}

func TestIsGarbledText(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"国家为什么会失败", false},
		{"Unknown", false},
		{"", true},
		{"\ufffd\ufffdV\ufffd[", true},
		{"\u0000U\u0000n", true},
		{"text\u001abroken", true},
	}
	for _, tc := range cases {
		if got := IsGarbledText(tc.in); got != tc.want {
			t.Fatalf("IsGarbledText(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

func TestPickDocumentTitle(t *testing.T) {
	path := filepath.Join(`f:`, "电子书", "国家为什么会失败.pdf")
	if got := PickDocumentTitle(path, "\ufffd\ufffdV\ufffd["); got != "国家为什么会失败" {
		t.Fatalf("expected filename fallback, got %q", got)
	}
	if got := PickDocumentTitle(path, "内嵌标题"); got != "内嵌标题" {
		t.Fatalf("expected metadata title, got %q", got)
	}
}

func TestParseFromFilePDFUTF16Title(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "国家为什么会失败.pdf")
	content := []byte("%PDF-1.4\n/Title<FEFF56FD5BB64E3A4EC04E484F1A59318D25>\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	meta := ParseFromFile(path)
	want := "国家为什么会失败"
	if meta.Title != want {
		t.Fatalf("expected %q, got %q", want, meta.Title)
	}
}

func TestParseFromFilePDFGarbledTitleFallsBackToFilename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "国家为什么会失败.pdf")
	// Old mis-decode pattern: UTF-16BE bytes interpreted as Latin-1-ish title string.
	content := []byte("%PDF-1.4\n/Title(V\xfd[\xb6N:N\xfdNHO\x1aY1\xfd%)\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	meta := ParseFromFile(path)
	if meta.Title != "国家为什么会失败" {
		t.Fatalf("expected filename fallback, got %q", meta.Title)
	}
}
