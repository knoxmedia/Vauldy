package recognition

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	venvRelDir       = "tools/recognition/.venv"
	tesseractRelDir  = "tools/tesseract"
	asrScriptRel     = "tools/asr/asr_to_vtt.py"
	ocrScriptRel     = "tools/subtitle_ocr/bitmap_subtitle_ocr.py"
	tesseractWinURL  = "https://github.com/tesseract-ocr/tesseract/releases/download/5.5.0/tesseract-ocr-w64-setup-5.5.0.20241111.exe"
	tessdataBaseURL  = "https://github.com/tesseract-ocr/tessdata/raw/main"
)

// ASRDeploy holds recommended config paths after ASR tool installation.
type ASRDeploy struct {
	Provider    string
	WhisperPath string
	Shell       string
	ExtraArgs   []string
}

// OCRDeploy holds recommended config paths after OCR tool installation.
type OCRDeploy struct {
	Enabled        bool
	TesseractPath  string
	TessdataPrefix string
	Languages      string
	PythonPath     string
	ScriptPath     string
	PgsripPath     string
}

// MediaRoot returns the directory containing config.yml (typically media/).
func MediaRoot(configPath string) string {
	return filepath.Clean(filepath.Dir(strings.TrimSpace(configPath)))
}

// InstallASR creates a shared Python venv, installs openai-whisper, and returns deploy settings.
func InstallASR(ctx context.Context, mediaRoot string) (ASRDeploy, error) {
	mediaRoot = filepath.Clean(mediaRoot)
	if err := ensureRepoScript(mediaRoot, asrScriptRel); err != nil {
		return ASRDeploy{}, err
	}
	py, err := EnsureVenv(ctx, mediaRoot)
	if err != nil {
		return ASRDeploy{}, err
	}
	if err := pipInstall(ctx, py, mediaRoot, []string{"openai-whisper>=20231117"}); err != nil {
		return ASRDeploy{}, fmt.Errorf("pip install whisper: %w", err)
	}
	whisperBin := venvWhisperBin(mediaRoot)
	asrScript := relIfUnder(mediaRoot, filepath.Join(mediaRoot, asrScriptRel))
	pyRel := relIfUnder(mediaRoot, py)
	shell := defaultASRShell(pyRel, asrScript)
	whisperPath := relIfUnder(mediaRoot, whisperBin)
	if _, err := os.Stat(whisperBin); err != nil {
		whisperPath = "whisper"
	}
	return ASRDeploy{
		Provider:    "shell",
		WhisperPath: whisperPath,
		Shell:       shell,
		ExtraArgs:   []string{},
	}, nil
}

// DefaultASRShell returns the recommended shell template with all Knox placeholders.
func DefaultASRShell(pythonPath, scriptPath string) string {
	return defaultASRShell(pythonPath, scriptPath)
}

func defaultASRShell(pythonPath, scriptPath string) string {
	return fmt.Sprintf(
		`"%s" "%s" --engine whisper --input "{input}" --output-vtt "{output_vtt}" --whisper-model base --whisper-language zh`,
		pythonPath, scriptPath,
	)
}

// InstallOCR installs Python OCR deps, Tesseract (Windows bundle), tessdata, and returns deploy settings.
func InstallOCR(ctx context.Context, mediaRoot string) (OCRDeploy, error) {
	mediaRoot = filepath.Clean(mediaRoot)
	if err := ensureRepoScript(mediaRoot, ocrScriptRel); err != nil {
		return OCRDeploy{}, err
	}
	py, err := EnsureVenv(ctx, mediaRoot)
	if err != nil {
		return OCRDeploy{}, err
	}
	if err := pipInstall(ctx, py, mediaRoot, []string{"pgsrip>=0.1.12"}); err != nil {
		return OCRDeploy{}, fmt.Errorf("pip install pgsrip: %w", err)
	}
	tessExe, tessdataDir, err := ensureTesseract(ctx, mediaRoot)
	if err != nil {
		return OCRDeploy{}, err
	}
	if err := ensureTessdata(ctx, tessdataDir, []string{"chi_sim", "eng"}); err != nil {
		return OCRDeploy{}, err
	}
	pgsrip := venvScriptBin(mediaRoot, "pgsrip")
	return OCRDeploy{
		Enabled:        true,
		TesseractPath:  relIfUnder(mediaRoot, tessExe),
		TessdataPrefix: relIfUnder(mediaRoot, tessdataDir),
		Languages:      "chi_sim+eng",
		PythonPath:     relIfUnder(mediaRoot, py),
		ScriptPath:     relIfUnder(mediaRoot, filepath.Join(mediaRoot, ocrScriptRel)),
		PgsripPath:     relIfUnder(mediaRoot, pgsrip),
	}, nil
}

// EnsureVenv creates tools/recognition/.venv if needed and returns venv python path.
func EnsureVenv(ctx context.Context, mediaRoot string) (string, error) {
	venvDir := filepath.Join(mediaRoot, filepath.FromSlash(venvRelDir))
	py := venvPythonBin(venvDir)
	if st, err := os.Stat(py); err == nil && !st.IsDir() {
		return py, nil
	}
	sysPy, pyArgs, err := findSystemPython(ctx)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(venvDir), 0o755); err != nil {
		return "", err
	}
	out, err := runPython(ctx, mediaRoot, sysPy, pyArgs, "-m", "venv", venvDir)
	if err != nil {
		return "", fmt.Errorf("create venv: %w: %s", err, trimOut(out))
	}
	if _, err := os.Stat(py); err != nil {
		return "", fmt.Errorf("venv python missing after create: %s", py)
	}
	return py, nil
}

func findSystemPython(ctx context.Context) (cmd string, prefixArgs []string, err error) {
	try := []struct {
		cmd  string
		args []string
	}{
		{"python3", nil},
		{"python", nil},
	}
	if runtime.GOOS == "windows" {
		try = []struct {
			cmd  string
			args []string
		}{
			{"python", nil},
			{"py", []string{"-3"}},
			{"python3", nil},
		}
	}
	for _, c := range try {
		args := append(append([]string{}, c.args...), "--version")
		out, runErr := exec.CommandContext(ctx, c.cmd, args...).CombinedOutput()
		if runErr == nil || strings.Contains(trimOut(out), "Python") {
			return c.cmd, c.args, nil
		}
	}
	return "", nil, fmt.Errorf("未找到 Python，请先安装 Python 3.9+ 并加入 PATH")
}

func runPython(ctx context.Context, mediaRoot, cmd string, prefixArgs []string, args ...string) ([]byte, error) {
	all := append(append([]string{}, prefixArgs...), args...)
	c := exec.CommandContext(ctx, cmd, all...)
	c.Dir = mediaRoot
	c.Env = os.Environ()
	return c.CombinedOutput()
}

func pipInstall(ctx context.Context, venvPython, mediaRoot string, packages []string) error {
	args := append([]string{"-m", "pip", "install", "--upgrade", "pip"}, packages...)
	out, err := runVenvPython(ctx, venvPython, mediaRoot, args...)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOut(out))
	}
	return nil
}

func runVenvPython(ctx context.Context, venvPython, mediaRoot string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, venvPython, args...)
	cmd.Dir = mediaRoot
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

func venvPythonBin(venvDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "python.exe")
	}
	return filepath.Join(venvDir, "bin", "python")
}

func venvWhisperBin(mediaRoot string) string {
	venvDir := filepath.Join(mediaRoot, filepath.FromSlash(venvRelDir))
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "whisper.exe")
	}
	return filepath.Join(venvDir, "bin", "whisper")
}

func venvScriptBin(mediaRoot, name string) string {
	venvDir := filepath.Join(mediaRoot, filepath.FromSlash(venvRelDir))
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", name+".exe")
	}
	return filepath.Join(venvDir, "bin", name)
}

func ensureTesseract(ctx context.Context, mediaRoot string) (exe string, tessdataDir string, err error) {
	dest := filepath.Join(mediaRoot, filepath.FromSlash(tesseractRelDir))
	switch runtime.GOOS {
	case "windows":
		return installTesseractWindows(ctx, dest)
	default:
		if p, err := exec.LookPath("tesseract"); err == nil {
			td := os.Getenv("TESSDATA_PREFIX")
			if td == "" {
				td = filepath.Join(filepath.Dir(p), "..", "share", "tessdata")
				td, _ = filepath.Abs(td)
			}
			return p, td, nil
		}
		if p := filepath.Join(dest, "tesseract"); fileExists(p) {
			return p, filepath.Join(dest, "tessdata"), nil
		}
		return "", "", fmt.Errorf("未找到 Tesseract，请安装系统包 (如 apt install tesseract-ocr tesseract-ocr-chi-sim) 或在 Windows 服务器上使用一键安装")
	}
}

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
	// NSIS: /D= must be last argument, no trailing backslash issues.
	cmd := exec.CommandContext(ctx, installer, "/S", "/D="+destDir)
	out, err := cmd.CombinedOutput()
	_ = os.Remove(installer)
	if err != nil && !fileExists(exe) {
		return "", "", fmt.Errorf("tesseract silent install: %w: %s", err, trimOut(out))
	}
	if !fileExists(exe) {
		// Some installers nest one level down.
		alt := filepath.Join(destDir, "tesseract.exe")
		if fileExists(alt) {
			exe = alt
		} else {
			return "", "", fmt.Errorf("tesseract.exe not found under %s after install", destDir)
		}
	}
	if err := os.MkdirAll(tessdata, 0o755); err != nil {
		return "", "", err
	}
	return exe, tessdata, nil
}

func ensureTessdata(ctx context.Context, tessdataDir string, langs []string) error {
	if err := os.MkdirAll(tessdataDir, 0o755); err != nil {
		return err
	}
	for _, lang := range langs {
		dest := filepath.Join(tessdataDir, lang+".traineddata")
		if fileExists(dest) {
			continue
		}
		url := fmt.Sprintf("%s/%s.traineddata", tessdataBaseURL, lang)
		if err := downloadFile(ctx, url, dest); err != nil {
			return fmt.Errorf("download tessdata %s: %w", lang, err)
		}
	}
	return nil
}

func ensureRepoScript(mediaRoot, rel string) error {
	p := filepath.Join(mediaRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("缺少脚本 %s（请确认在 media 目录运行服务）", rel)
	}
	return nil
}

func downloadFile(ctx context.Context, url, dest string) error {
	return DownloadFile(ctx, url, dest)
}

// DownloadFile fetches url to dest (atomic via .part rename).
func DownloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, trimOut(b))
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

func relIfUnder(base, abs string) string {
	abs = filepath.Clean(abs)
	base = filepath.Clean(base)
	if rel, err := filepath.Rel(base, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return filepath.ToSlash(rel)
	}
	return abs
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func trimOut(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 2000 {
		return s[:2000]
	}
	return s
}
