package doctrans

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/config"
)

// ExportPageJPEG renders the first page of a document to JPEG using configured engine priority.
// Used for PDF covers and as a fallback when ffmpeg cannot read the source.
func ExportPageJPEG(ctx context.Context, mediaRoot string, cfg config.DocTransConfig, sourcePath, outPath string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	outPath = strings.TrimSpace(outPath)
	if sourcePath == "" || outPath == "" {
		return fmt.Errorf("export page jpg: invalid paths")
	}
	if IsOfficeFormat(sourcePath) {
		if err := ExportOfficeCoverJPEG(ctx, mediaRoot, cfg, sourcePath, outPath); err == nil {
			return nil
		}
	}
	if strings.EqualFold(filepath.Ext(sourcePath), ".pdf") {
		return rasterizePDFFirstPage(ctx, mediaRoot, cfg, sourcePath, outPath)
	}
	if st := detectLibreOffice(mediaRoot, cfg); st.Available {
		return exportDrawJPEGLibreOffice(ctx, mediaRoot, cfg, sourcePath, outPath)
	}
	return fmt.Errorf("export page jpg: no exporter for %s", filepath.Ext(sourcePath))
}

// ExportOfficeCoverJPEG exports a cover image from an Office document using engine priority.
func ExportOfficeCoverJPEG(ctx context.Context, mediaRoot string, cfg config.DocTransConfig, sourcePath, outPath string) error {
	if !IsOfficeFormat(sourcePath) {
		return fmt.Errorf("not an office document")
	}
	ext := strings.ToLower(filepath.Ext(sourcePath))
	isPPT := ext == ".ppt" || ext == ".pptx"

	var lastErr error
	for _, kind := range engineOrderFromConfig(cfg) {
		st := engineStatusFor(mediaRoot, cfg, kind)
		if !st.Available {
			continue
		}
		switch kind {
		case EngineWPS, EngineOffice:
			if isPPT {
				if err := exportCoverCOM(ctx, mediaRoot, kind, sourcePath, outPath); err == nil {
					return nil
				} else {
					lastErr = err
				}
			}
			tmpDir, err := os.MkdirTemp(filepath.Dir(outPath), "doccover-pdf-*")
			if err != nil {
				lastErr = err
				continue
			}
			pdfPath, convErr := convertWithEngine(ctx, mediaRoot, cfg, kind, sourcePath, tmpDir)
			if convErr != nil {
				os.RemoveAll(tmpDir)
				lastErr = convErr
				continue
			}
			rasterErr := rasterizePDFFirstPage(ctx, mediaRoot, cfg, pdfPath, outPath)
			os.RemoveAll(tmpDir)
			if rasterErr == nil {
				return nil
			}
			lastErr = rasterErr
		case EngineLibreOffice:
			if err := exportDrawJPEGLibreOffice(ctx, mediaRoot, cfg, sourcePath, outPath); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
	}
	if lastErr != nil {
		return fmt.Errorf("office cover: %w", lastErr)
	}
	return fmt.Errorf("office cover: no engine available")
}
