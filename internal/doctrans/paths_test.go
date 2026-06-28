package doctrans

import "testing"

func TestIsOfficeFormat(t *testing.T) {
	if !IsOfficeFormat("report.docx") {
		t.Fatal("docx should be office")
	}
	if !IsOfficeFormat("slides.ppt") {
		t.Fatal("ppt should be office")
	}
	if IsOfficeFormat("readme.pdf") {
		t.Fatal("pdf should not be office")
	}
}

func TestIsOfficeDocument(t *testing.T) {
	if !IsOfficeDocument("92a7a378.enc", "doc") {
		t.Fatal("encrypted doc catalog should be office via format")
	}
	if IsOfficeDocument("92a7a378.enc", "pdf") {
		t.Fatal("encrypted pdf should not be office")
	}
	if !IsOfficeDocument("report.docx", "") {
		t.Fatal("docx path should be office")
	}
}

func TestDefaultSofficeRel(t *testing.T) {
	if DefaultSofficeRel() == "" {
		t.Fatal("expected default soffice path")
	}
}
