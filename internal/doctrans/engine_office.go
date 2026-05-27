package doctrans

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"knox-media/internal/config"
)

func detectOffice(mediaRoot string, cfg config.DocTransConfig) EngineStatus {
	st := EngineStatus{Kind: EngineOffice, Label: engineLabel(EngineOffice)}
	if runtime.GOOS != "windows" {
		st.Message = "仅 Windows 支持 Microsoft Office COM 转换"
		return st
	}
	p := resolveOfficePath(mediaRoot, cfg)
	if p == "" {
		st.Message = "未检测到 Microsoft Office（Word/Excel/PowerPoint）"
		return st
	}
	st.Path = p
	if testOfficeCOM() {
		st.Available = true
		st.Message = "可用"
		st.Version = p
		return st
	}
	st.Message = "已找到 Office 程序但 COM 自动化不可用"
	return st
}

func resolveOfficePath(mediaRoot string, cfg config.DocTransConfig) string {
	if p := ResolvePath(mediaRoot, strings.TrimSpace(cfg.OfficePath)); p != "" {
		if fileExists(p) {
			return p
		}
	}
	candidates := []string{
		`C:\Program Files\Microsoft Office\root\Office16\WINWORD.EXE`,
		`C:\Program Files (x86)\Microsoft Office\root\Office16\WINWORD.EXE`,
		`C:\Program Files\Microsoft Office\Office16\WINWORD.EXE`,
		`C:\Program Files (x86)\Microsoft Office\Office15\WINWORD.EXE`,
	}
	for _, c := range candidates {
		if fileExists(c) {
			return filepath.Dir(c)
		}
	}
	return ""
}

func testOfficeCOM() bool {
	script := `$ErrorActionPreference='Stop'; $w=New-Object -ComObject Word.Application; $w.Quit(); $true`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run() == nil
}

func convertOffice(ctx context.Context, mediaRoot string, sourcePath, tmpDir string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("office engine requires windows")
	}
	if err := ensureConvertScript(mediaRoot); err != nil {
		return "", err
	}
	script := ResolvePath(mediaRoot, "tools/doctran/convert_com.ps1")
	outPDF := filepath.Join(tmpDir, strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))+".pdf")
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", script, "-Engine", "office", "-InputPath", sourcePath, "-OutputPath", outPDF)
	if mediaRoot != "" {
		cmd.Dir = mediaRoot
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("office com: %w: %s", err, trimOut(out))
	}
	if !fileExists(outPDF) {
		return "", fmt.Errorf("office com: no output: %s", trimOut(out))
	}
	return outPDF, nil
}
