package doctrans

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"knox-media/internal/config"
)

func detectWPS(mediaRoot string, cfg config.DocTransConfig) EngineStatus {
	st := EngineStatus{Kind: EngineWPS, Label: engineLabel(EngineWPS)}
	if runtime.GOOS != "windows" {
		st.Message = "WPS COM 转换当前仅支持 Windows"
		return st
	}
	dir := resolveWPSDir(mediaRoot, cfg)
	if dir == "" {
		st.Message = "未检测到 WPS Office（Kingsoft）"
		return st
	}
	st.Path = dir
	if testWPSCOM() {
		st.Available = true
		st.Message = "可用"
		st.Version = dir
		return st
	}
	st.Message = "已找到 WPS 但 COM 不可用"
	return st
}

func resolveWPSDir(mediaRoot string, cfg config.DocTransConfig) string {
	if p := ResolvePath(mediaRoot, strings.TrimSpace(cfg.WPSPath)); p != "" {
		if fileExists(filepath.Join(p, "wps.exe")) || fileExists(filepath.Join(p, "et.exe")) {
			return p
		}
		if fileExists(p) {
			return filepath.Dir(p)
		}
	}
	roots := []string{
		`C:\Program Files\Kingsoft\WPS Office`,
		`C:\Program Files (x86)\Kingsoft\WPS Office`,
		`C:\Program Files\WPS Office`,
		`C:\Program Files (x86)\WPS Office`,
	}
	for _, root := range roots {
		if dir := findWPSOffice6(root); dir != "" {
			return dir
		}
	}
	return findWPSOffice6(ResolvePath(mediaRoot, DefaultDirRel))
}

func findWPSOffice6(root string) string {
	if root == "" {
		return ""
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "office6") {
			if fileExists(filepath.Join(path, "wps.exe")) {
				found = path
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

func testWPSCOM() bool {
	script := `$ErrorActionPreference='Stop'; try { $w=New-Object -ComObject Kwps.Application; $w.Quit(); $true } catch { $false }`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run() == nil
}

func convertWPS(ctx context.Context, mediaRoot string, sourcePath, tmpDir string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("wps engine requires windows")
	}
	if err := ensureConvertScript(mediaRoot); err != nil {
		return "", err
	}
	script := ResolvePath(mediaRoot, "tools/doctran/convert_com.ps1")
	outPDF := filepath.Join(tmpDir, strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))+".pdf")
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", script, "-Engine", "wps", "-InputPath", sourcePath, "-OutputPath", outPDF)
	if mediaRoot != "" {
		cmd.Dir = mediaRoot
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("wps com: %w: %s", err, trimOut(out))
	}
	if !fileExists(outPDF) {
		return "", fmt.Errorf("wps com: no output: %s", trimOut(out))
	}
	return outPDF, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
