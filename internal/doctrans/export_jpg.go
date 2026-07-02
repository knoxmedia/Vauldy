package doctrans

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"knox-media/internal/config"
)

var libreOfficeMu sync.Mutex

// ExportDrawJPEG renders the first page of a PDF or other Draw-supported document to JPEG via LibreOffice.
func ExportDrawJPEG(ctx context.Context, mediaRoot string, cfg config.DocTransConfig, sourcePath, outPath string) error {
	return exportDrawJPEGLibreOffice(ctx, mediaRoot, cfg, sourcePath, outPath)
}

func exportDrawJPEGLibreOffice(ctx context.Context, mediaRoot string, cfg config.DocTransConfig, sourcePath, outPath string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	outPath = strings.TrimSpace(outPath)
	if sourcePath == "" || outPath == "" {
		return fmt.Errorf("export jpg: invalid paths")
	}
	if abs, err := filepath.Abs(sourcePath); err == nil {
		sourcePath = abs
	}
	if abs, err := filepath.Abs(outPath); err == nil {
		outPath = abs
	}
	libreOfficeMu.Lock()
	defer libreOfficeMu.Unlock()

	conv := &Converter{MediaRoot: mediaRoot, Config: cfg}
	soffice := resolveSofficePath(mediaRoot, cfg)
	if soffice == "" {
		soffice = absSofficePath(mediaRoot, conv.resolveLibreOffice())
	}
	if soffice == "" {
		return fmt.Errorf("libreoffice not found")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(filepath.Dir(outPath), "doccover-lo-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	profileDir, err := os.MkdirTemp("", "lo-cover-profile-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profileDir)

	userInstall := profileURL(profileDir)
	args := libreOfficeHeadlessArgs(
		"-env:UserInstallation="+userInstall,
		"--convert-to", "jpg",
		"--outdir", tmpDir,
		sourcePath,
	)
	bin := libreOfficeExecBinary(soffice)
	cmd := exec.CommandContext(ctx, bin, args...)
	prepareLibreOfficeCmd(cmd, soffice)
	out, err := cmd.CombinedOutput()
	converted := pickJPEGInDir(tmpDir, sourcePath)
	if converted == "" {
		if err != nil {
			return fmt.Errorf("libreoffice jpg: %w: %s", err, trimOut(out))
		}
		return fmt.Errorf("libreoffice jpg: no output")
	}
	if err != nil {
		// Portable LibreOffice may print bootstrap warnings on stderr yet still export JPEG.
		if !strings.Contains(trimOut(out), "Could not find platform independent libraries") {
			return fmt.Errorf("libreoffice jpg: %w: %s", err, trimOut(out))
		}
	}
	return copyFile(converted, outPath)
}

func pickJPEGInDir(dir, sourcePath string) string {
	base := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath)) + ".jpg"
	converted := filepath.Join(dir, base)
	if st, err := os.Stat(converted); err == nil && !st.IsDir() && st.Size() > 0 {
		return converted
	}
	entries, _ := os.ReadDir(dir)
	var first string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".jpg" && ext != ".jpeg" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			if first == "" {
				first = p
			}
			if strings.EqualFold(e.Name(), base) {
				return p
			}
		}
	}
	return first
}
