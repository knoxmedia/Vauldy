//go:build windows

package recognition

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// 7-Zip bootstrap URLs used when no system 7-Zip is installed.
// 7zr.exe is a single-file console binary that only handles the 7z format; it can
// extract the 7z-extra package, which contains 7zz.exe (Alone2 bundle) — the only
// standalone 7-Zip binary that supports NSIS installer extraction.
const (
	sevenZipStandaloneURL = "https://www.7-zip.org/a/7zr.exe"
	sevenZipExtraURL      = "https://github.com/ip7z/7zip/releases/download/26.01/7z2601-extra.7z"
)

func installTesseractWindows(ctx context.Context, destDir string) (string, string, error) {
	exe := filepath.Join(destDir, "tesseract.exe")
	tessdata := filepath.Join(destDir, "tessdata")
	if fileExists(exe) {
		return exe, tessdata, nil
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", "", err
	}
	installer := filepath.Join(destDir, "tesseract-setup.exe")
	if err := downloadFile(ctx, tesseractWinURL, installer); err != nil {
		return "", "", fmt.Errorf("download tesseract: %w", err)
	}
	defer os.Remove(installer)

	if err := extractTesseractInstaller7z(ctx, installer, destDir); err == nil && fileExists(exe) {
		if err := os.MkdirAll(tessdata, 0o755); err != nil {
			return "", "", err
		}
		return exe, tessdata, nil
	}

	cmd := exec.CommandContext(ctx, installer, "/S", "/D="+destDir)
	out, err := cmd.CombinedOutput()
	if err != nil && !fileExists(exe) {
		if sysExe, sysData := findWindowsSystemTesseract(); sysExe != "" {
			if err := os.MkdirAll(tessdata, 0o755); err != nil {
				return "", "", err
			}
			if sysData != "" && sysData != tessdata {
				_ = copyTessdataLanguages(ctx, sysData, tessdata, []string{"chi_sim", "eng"})
			}
			return sysExe, preferTessdataDir(sysData, tessdata), nil
		}
		if isElevationError(err, out) {
			return "", "", fmt.Errorf("tesseract 安装需要管理员权限；一键安装已尝试自动下载 7-Zip 解压但失败（%s）。请检查网络后重试、以管理员运行服务，或手动安装 Tesseract 并加入 PATH: %w", sevenZipExtraURL, err)
		}
		return "", "", fmt.Errorf("tesseract silent install: %w: %s", err, trimOut(out))
	}
	if !fileExists(exe) {
		return "", "", fmt.Errorf("tesseract.exe not found under %s after install", destDir)
	}
	if err := os.MkdirAll(tessdata, 0o755); err != nil {
		return "", "", err
	}
	return exe, tessdata, nil
}

func isElevationError(err error, out []byte) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error() + " " + string(out))
	return strings.Contains(s, "elevation") || strings.Contains(s, "requires administrator") || strings.Contains(s, "740")
}

// find7zip returns a 7-Zip executable capable of extracting NSIS installers.
// 7za.exe is intentionally excluded because it cannot handle the NSIS format;
// only the full 7z.exe (with 7z.dll) supports NSIS.
func find7zip() string {
	candidates := []string{
		"7z",
		`C:\Program Files\7-Zip\7z.exe`,
		`C:\Program Files (x86)\7-Zip\7z.exe`,
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// ensure7zip returns a 7-Zip executable that supports NSIS extraction.
// It prefers a system-installed 7-Zip (find7zip). When none is found it
// bootstraps 7zz.exe by downloading 7zr.exe plus the 7-Zip extra package —
// no administrator privileges required. The bootstrapped 7zz.exe is cached
// under cacheDir for reuse.
func ensure7zip(ctx context.Context, cacheDir string) (string, error) {
	if found := find7zip(); found != "" {
		return found, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	sevenZZ := filepath.Join(cacheDir, "7zz.exe")
	if fileExists(sevenZZ) {
		return sevenZZ, nil
	}
	sevenZR := filepath.Join(cacheDir, "7zr.exe")
	if !fileExists(sevenZR) {
		if err := downloadFile(ctx, sevenZipStandaloneURL, sevenZR); err != nil {
			return "", fmt.Errorf("download 7zr.exe: %w", err)
		}
	}
	extraArchive := filepath.Join(cacheDir, "7z-extra.7z")
	if !fileExists(extraArchive) {
		if err := downloadFile(ctx, sevenZipExtraURL, extraArchive); err != nil {
			return "", fmt.Errorf("download 7z-extra: %w", err)
		}
	}
	extractDir := filepath.Join(cacheDir, ".extra")
	_ = os.RemoveAll(extractDir)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", err
	}
	defer os.RemoveAll(extractDir)
	cmd := exec.CommandContext(ctx, sevenZR, "x", "-y", "-o"+extractDir, extraArchive)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("7zr extract 7z-extra: %w: %s", err, trimOut(out))
	}
	found, err := findFileInTree(extractDir, "7zz.exe")
	if err != nil {
		return "", err
	}
	if err := copyFile(found, sevenZZ); err != nil {
		return "", err
	}
	return sevenZZ, nil
}

func extractTesseractInstaller7z(ctx context.Context, installer, destDir string) error {
	cacheDir := filepath.Join(destDir, ".7zip")
	sevenZip, err := ensure7zip(ctx, cacheDir)
	if err != nil {
		return fmt.Errorf("7-Zip unavailable: %w", err)
	}
	extractDir := filepath.Join(destDir, ".extract")
	_ = os.RemoveAll(extractDir)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)

	cmd := exec.CommandContext(ctx, sevenZip, "x", "-y", "-o"+extractDir, installer)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("7z extract: %w: %s", err, trimOut(out))
	}
	exePath, err := findFileInTree(extractDir, "tesseract.exe")
	if err != nil {
		return err
	}
	srcDir := filepath.Dir(exePath)
	if err := copyDirFiles(srcDir, destDir); err != nil {
		return err
	}
	return nil
}

func findFileInTree(root, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), name) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s not found in extracted installer", name)
	}
	return found, nil
}

func copyDirFiles(srcDir, destDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		dest := filepath.Join(destDir, e.Name())
		if err := copyFile(src, dest); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func findWindowsSystemTesseract() (exe string, tessdata string) {
	if p, err := exec.LookPath("tesseract"); err == nil {
		return p, tessdataBesideExe(p)
	}
	for _, p := range []string{
		`C:\Program Files\Tesseract-OCR\tesseract.exe`,
		`C:\Program Files (x86)\Tesseract-OCR\tesseract.exe`,
	} {
		if fileExists(p) {
			return p, tessdataBesideExe(p)
		}
	}
	return "", ""
}

func tessdataBesideExe(exe string) string {
	dir := filepath.Dir(exe)
	for _, name := range []string{"tessdata", filepath.Join("..", "tessdata")} {
		p := filepath.Clean(filepath.Join(dir, name))
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

func preferTessdataDir(systemDir, localDir string) string {
	if systemDir != "" {
		if st, err := os.Stat(systemDir); err == nil && st.IsDir() {
			return systemDir
		}
	}
	return localDir
}

func copyTessdataLanguages(ctx context.Context, srcDir, destDir string, langs []string) error {
	_ = ctx
	for _, lang := range langs {
		name := lang + ".traineddata"
		src := filepath.Join(srcDir, name)
		dest := filepath.Join(destDir, name)
		if fileExists(dest) {
			continue
		}
		if !fileExists(src) {
			continue
		}
		if err := copyFile(src, dest); err != nil {
			return err
		}
	}
	return nil
}
