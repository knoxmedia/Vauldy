package doctrans

import (
	"context"
	"testing"

	"knox-media/internal/config"
)

func TestExportPageJPEGRejectsEmptyPaths(t *testing.T) {
	err := ExportPageJPEG(context.Background(), "", config.DocTransConfig{}, "", "")
	if err == nil {
		t.Fatal("expected error for empty paths")
	}
}

func TestExportOfficeCoverJPEGRejectsNonOffice(t *testing.T) {
	err := ExportOfficeCoverJPEG(context.Background(), t.TempDir(), config.DocTransConfig{}, "notes.txt", t.TempDir()+"/out.jpg")
	if err == nil {
		t.Fatal("expected error for non-office file")
	}
}

func TestNormalizeEngineOrderWPSFirst(t *testing.T) {
	got := NormalizeEngineOrder([]string{"wps", "libreoffice"})
	if len(got) < 2 || got[0] != EngineWPS {
		t.Fatalf("order = %v, want wps first", got)
	}
}
