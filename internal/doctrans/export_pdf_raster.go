package doctrans

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"knox-media/internal/config"
)

func rasterizePDFFirstPage(ctx context.Context, mediaRoot string, cfg config.DocTransConfig, pdfPath, outPath string) error {
	pdfPath = strings.TrimSpace(pdfPath)
	outPath = strings.TrimSpace(outPath)
	if pdfPath == "" || outPath == "" {
		return fmt.Errorf("pdf raster: invalid paths")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	workPath, cleanup, err := stagePDFRasterInput(pdfPath)
	if err != nil {
		return err
	}
	defer cleanup()

	try := []func() error{
		func() error { return exportDrawJPEGLibreOffice(ctx, mediaRoot, cfg, workPath, outPath) },
		func() error { return rasterizePDFWithMutool(ctx, mediaRoot, workPath, outPath) },
		func() error { return rasterizePDFWithPdftoppm(ctx, mediaRoot, workPath, outPath) },
		func() error { return rasterizePDFWithGhostscript(ctx, workPath, outPath) },
		func() error { return rasterizePDFWithMagick(ctx, workPath, outPath) },
	}

	var lastErr error
	for _, fn := range try {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("pdf raster: %w (install LibreOffice via 系统选项→文档转换, or add mutool/pdftoppm to PATH)", lastErr)
	}
	return fmt.Errorf("pdf raster: no renderer available")
}

// stagePDFRasterInput copies PDFs to a short ASCII temp path on Windows for external renderers.
func stagePDFRasterInput(pdfPath string) (string, func(), error) {
	pdfPath = filepath.Clean(strings.TrimSpace(pdfPath))
	st, err := os.Stat(pdfPath)
	if err != nil {
		return "", nil, fmt.Errorf("pdf missing: %w", err)
	}
	if st.IsDir() || st.Size() == 0 {
		return "", nil, fmt.Errorf("pdf empty")
	}
	tmp, err := os.CreateTemp("", "knox-pdf-raster-*.pdf")
	if err != nil {
		return "", nil, err
	}
	name := tmp.Name()
	_ = tmp.Close()
	if err := copyFile(pdfPath, name); err != nil {
		_ = os.Remove(name)
		return "", nil, err
	}
	return name, func() { _ = os.Remove(name) }, nil
}

func resolveMutool(mediaRoot string) string {
	for _, name := range []string{"mutool", "mutool.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, rel := range []string{
		"tools/doctran/mupdf/mutool.exe",
		"tools/mupdf/mutool.exe",
	} {
		for _, root := range mediaRoots(mediaRoot) {
			p := ResolvePath(root, rel)
			if fileExists(p) {
				return p
			}
		}
	}
	return ""
}

func rasterizePDFWithMutool(ctx context.Context, mediaRoot, pdfPath, outPath string) error {
	bin := resolveMutool(mediaRoot)
	if bin == "" {
		return fmt.Errorf("mutool not found")
	}
	cmd := exec.CommandContext(ctx, bin,
		"draw",
		"-o", outPath,
		"-w", "480",
		"-h", "640",
		"-F", "jpeg",
		pdfPath,
		"1",
	)
	setHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mutool: %w: %s", err, trimOut(out))
	}
	if st, err := os.Stat(outPath); err != nil || st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("mutool: empty output")
	}
	return nil
}

func resolvePdftoppm(mediaRoot string) string {
	for _, name := range []string{"pdftoppm", "pdftoppm.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, rel := range []string{
		"tools/doctran/poppler/Library/bin/pdftoppm.exe",
		"tools/doctran/poppler/bin/pdftoppm.exe",
		"tools/doctran/poppler/pdftoppm.exe",
		`C:\Program Files\poppler\Library\bin\pdftoppm.exe`,
		`C:\Program Files (x86)\poppler\Library\bin\pdftoppm.exe`,
	} {
		for _, root := range mediaRoots(mediaRoot) {
			p := ResolvePath(root, rel)
			if fileExists(p) {
				return p
			}
		}
		if fileExists(rel) {
			return rel
		}
	}
	return ""
}

func rasterizePDFWithPdftoppm(ctx context.Context, mediaRoot, pdfPath, outPath string) error {
	bin := resolvePdftoppm(mediaRoot)
	if bin == "" {
		return fmt.Errorf("pdftoppm not found")
	}
	tmpDir, err := os.MkdirTemp(filepath.Dir(outPath), "pdftoppm-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	prefix := filepath.Join(tmpDir, "page")
	cmd := exec.CommandContext(ctx, bin,
		"-jpeg", "-f", "1", "-l", "1",
		"-scale-to", "480",
		pdfPath, prefix,
	)
	setHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pdftoppm: %w: %s", err, trimOut(out))
	}
	candidates := []string{
		prefix + "-1.jpg",
		prefix + "-01.jpg",
		prefix + ".jpg",
		prefix + "1.jpg",
	}
	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".jpg" || ext == ".jpeg" {
			candidates = append(candidates, filepath.Join(tmpDir, e.Name()))
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Size() > 0 {
			return copyFile(c, outPath)
		}
	}
	return fmt.Errorf("pdftoppm: no output")
}

func resolveGhostscript() string {
	for _, name := range []string{"gswin64c", "gswin32c", "gs", "gs.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{
		`C:\Program Files\gs\gs10.04.0\bin\gswin64c.exe`,
		`C:\Program Files\gs\gs10.03.1\bin\gswin64c.exe`,
		`C:\Program Files (x86)\gs\gs10.04.0\bin\gswin32c.exe`,
	} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func rasterizePDFWithGhostscript(ctx context.Context, pdfPath, outPath string) error {
	bin := resolveGhostscript()
	if bin == "" {
		return fmt.Errorf("ghostscript not found")
	}
	cmd := exec.CommandContext(ctx, bin,
		"-dSAFER", "-dBATCH", "-dNOPAUSE",
		"-sDEVICE=jpeg",
		"-dJPEGQ=85",
		"-dFirstPage=1", "-dLastPage=1",
		"-r150",
		"-sOutputFile="+outPath,
		pdfPath,
	)
	setHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ghostscript: %w: %s", err, trimOut(out))
	}
	if st, err := os.Stat(outPath); err != nil || st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("ghostscript: empty output")
	}
	return nil
}

func resolveMagick() string {
	for _, name := range []string{"magick", "magick.exe", "convert", "convert.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func rasterizePDFWithMagick(ctx context.Context, pdfPath, outPath string) error {
	bin := resolveMagick()
	if bin == "" {
		return fmt.Errorf("imagemagick not found")
	}
	page := pdfPath + "[0]"
	var cmd *exec.Cmd
	base := strings.ToLower(filepath.Base(bin))
	if base == "magick.exe" || base == "magick" {
		cmd = exec.CommandContext(ctx, bin, "convert", page, "-quality", "85", "-resize", "480x480>", outPath)
	} else {
		cmd = exec.CommandContext(ctx, bin, page, "-quality", "85", "-resize", "480x480>", outPath)
	}
	setHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("imagemagick: %w: %s", err, trimOut(out))
	}
	if st, err := os.Stat(outPath); err != nil || st.IsDir() || st.Size() == 0 {
		return fmt.Errorf("imagemagick: empty output")
	}
	return nil
}
