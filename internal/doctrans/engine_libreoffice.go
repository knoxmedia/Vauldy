package doctrans

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"knox-media/internal/config"
)

func detectLibreOffice(mediaRoot string, cfg config.DocTransConfig) EngineStatus {
	st := EngineStatus{Kind: EngineLibreOffice, Label: engineLabel(EngineLibreOffice)}
	p := resolveSofficePath(mediaRoot, cfg)
	if p == "" {
		conv := &Converter{MediaRoot: mediaRoot, Config: cfg}
		p = absSofficePath(mediaRoot, conv.resolveLibreOffice())
	}
	if p == "" {
		st.Message = fmt.Sprintf("未找到 LibreOffice，可一键安装到 %s", DefaultDirRel)
		return st
	}
	if _, err := os.Stat(p); err != nil {
		st.Message = fmt.Sprintf("路径无效: %s", p)
		st.Path = p
		return st
	}
	st.Path = p
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	profileDir, err := os.MkdirTemp("", "lo-probe-*")
	if err != nil {
		st.Message = fmt.Sprintf("无法创建临时目录: %v", err)
		return st
	}
	defer os.RemoveAll(profileDir)

	bin := libreOfficeExecBinary(p)
	args := libreOfficeHeadlessArgs("-env:UserInstallation="+profileURL(profileDir), "--version")
	cmd := exec.CommandContext(cctx, bin, args...)
	prepareLibreOfficeCmd(cmd, p)
	out, err := cmd.CombinedOutput()
	st.Version = strings.TrimSpace(string(out))
	if err != nil {
		st.Message = fmt.Sprintf("无法运行: %v", err)
		return st
	}
	st.Available = true
	st.Message = "可用"
	return st
}

func convertLibreOffice(ctx context.Context, mediaRoot string, cfg config.DocTransConfig, sourcePath, tmpDir string) (string, error) {
	libreOfficeMu.Lock()
	defer libreOfficeMu.Unlock()
	conv := &Converter{MediaRoot: mediaRoot, Config: cfg}
	soffice := absSofficePath(mediaRoot, conv.resolveLibreOffice())
	if soffice == "" {
		return "", fmt.Errorf("libreoffice not found")
	}
	profileDir, err := os.MkdirTemp("", "lo-profile-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(profileDir)

	userInstall := profileURL(profileDir)
	args := libreOfficeHeadlessArgs(
		"-env:UserInstallation="+userInstall,
		"--convert-to", "pdf",
		"--outdir", tmpDir,
		sourcePath,
	)
	bin := libreOfficeExecBinary(soffice)
	cmd := exec.CommandContext(ctx, bin, args...)
	prepareLibreOfficeCmd(cmd, soffice)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("libreoffice: %w: %s", err, trimOut(out))
	}
	return pickPDFInDir(tmpDir, sourcePath), nil
}

func (c *Converter) resolveLibreOffice() string {
	if c == nil {
		return ""
	}
	if p := strings.TrimSpace(libreOfficePath(c.Config)); p != "" && filepath.IsAbs(p) {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	p := ResolvePath(c.MediaRoot, libreOfficePath(c.Config))
	if p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	def := ResolvePath(c.MediaRoot, DefaultSofficeRel())
	if st, err := os.Stat(def); err == nil && !st.IsDir() {
		return def
	}
	if found := findSofficeUnder(c.MediaRoot, DefaultDirRel); found != "" {
		return found
	}
	if name := lookupOnPath(); name != "" {
		return name
	}
	if runtime.GOOS == "windows" {
		for _, cpath := range []string{
			`C:\Program Files\LibreOffice\program\soffice.exe`,
			`C:\Program Files (x86)\LibreOffice\program\soffice.exe`,
		} {
			if st, err := os.Stat(cpath); err == nil && !st.IsDir() {
				return cpath
			}
		}
	}
	return ""
}

func pickPDFInDir(dir, sourcePath string) string {
	base := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath)) + ".pdf"
	converted := filepath.Join(dir, base)
	if st, err := os.Stat(converted); err == nil && !st.IsDir() && st.Size() > 0 {
		return converted
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
			p := filepath.Join(dir, e.Name())
			if st, err := os.Stat(p); err == nil && st.Size() > 0 {
				return p
			}
		}
	}
	return converted
}
