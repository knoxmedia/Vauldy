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

func TestDefaultSofficeRel(t *testing.T) {
	if DefaultSofficeRel() == "" {
		t.Fatal("expected default soffice path")
	}
}
