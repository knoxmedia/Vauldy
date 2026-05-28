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
			return "", "", fmt.Errorf("tesseract 安装需要管理员权限；请安装 7-Zip 后重试一键安装、以管理员运行服务，或手动安装 Tesseract 并加入 PATH: %w", err)
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

func find7zip() string {
	candidates := []string{
		"7z",
		"7za",
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

func extractTesseractInstaller7z(ctx context.Context, installer, destDir string) error {
	sevenZip := find7zip()
	if sevenZip == "" {
		return fmt.Errorf("7-Zip not found")
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
