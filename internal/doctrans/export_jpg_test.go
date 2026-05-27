//go:build integration

package doctrans

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"knox-media/internal/config"
)

func TestExportDrawJPEGSamplePDF(t *testing.T) {
	pdf := `k:\电子书\流媒体\books\ffplay源码和书籍\ffdoc.pdf`
	if _, err := os.Stat(pdf); err != nil {
		t.Skip("sample PDF not available on this machine")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(root, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "cover.jpg")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := ExportDrawJPEG(ctx, root, cfg.DocTrans, pdf, out); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() == 0 {
		t.Fatalf("empty output: %v", err)
	}
}
