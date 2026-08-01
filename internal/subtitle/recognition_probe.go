package subtitle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const defaultOCRScriptRel = "tools/subtitle_ocr/bitmap_subtitle_ocr.py"

func resolveOCRPath(mediaRoot, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultOCRScriptRel
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if mediaRoot != "" {
		return filepath.Clean(filepath.Join(mediaRoot, filepath.FromSlash(path)))
	}
	return filepath.Clean(filepath.FromSlash(path))
}

func resolveToolPath(mediaRoot, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	// Bare command names (tesseract, python) are resolved via PATH in runProbe.
	if !strings.Contains(path, "/") && !strings.Contains(path, `\`) {
		return path
	}
	if mediaRoot != "" {
		return filepath.Clean(filepath.Join(mediaRoot, filepath.FromSlash(path)))
	}
	return filepath.Clean(filepath.FromSlash(path))
}

// RecognitionTestResult is returned by ASR/OCR connectivity checks.
type RecognitionTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// CheckASRConfig verifies ASR settings without running full transcription.
// mediaRoot resolves relative python/shell paths (may be empty).
func CheckASRConfig(ctx context.Context, mediaRoot string, asr ASRConfig) RecognitionTestResult {
	provider := strings.ToLower(strings.TrimSpace(asr.Provider))
	switch provider {
	case "", "none":
		return RecognitionTestResult{OK: true, Message: "ASR 未启用"}
	case "whisper_cli":
		wp := strings.TrimSpace(asr.WhisperPath)
		if wp == "" {
			wp = "whisper"
		}
		wp = resolveToolPath(mediaRoot, wp)
		if err := runProbe(ctx, wp, "--help"); err != nil {
			if err2 := runProbe(ctx, wp, "-h"); err2 != nil {
				return RecognitionTestResult{OK: false, Message: fmt.Sprintf("Whisper 不可用: %v", err)}
			}
		}
		return RecognitionTestResult{OK: true, Message: "Whisper CLI 可用"}
	case "shell":
		sh := strings.TrimSpace(asr.Shell)
		if sh == "" {
			return RecognitionTestResult{OK: false, Message: "Shell 命令为空"}
		}
		for _, ph := range []string{"{input}", "{output_vtt}"} {
			if !strings.Contains(sh, ph) {
				return RecognitionTestResult{OK: false, Message: fmt.Sprintf("Shell 命令缺少占位符 %s", ph)}
			}
		}
		engine := strings.ToLower(strings.TrimSpace(asr.Engine))
		if engine == "" {
			engine = shellFlagValue(sh, "--engine")
		}
		if engine == "" {
			engine = "whisper"
		}
		mod, ok := asrImportModule(engine)
		if !ok {
			return RecognitionTestResult{OK: true, Message: fmt.Sprintf("Shell 命令格式正确（引擎 %s 未做依赖探测）", engine)}
		}
		py := resolveASRPython(mediaRoot, sh)
		if py == "" {
			return RecognitionTestResult{OK: true, Message: "Shell 命令格式正确（未找到 Python，跳过依赖探测）"}
		}
		if err := runProbe(ctx, py, "-c", "import "+mod); err != nil {
			return RecognitionTestResult{OK: false, Message: fmt.Sprintf("引擎 %s 依赖不可用 (%s): %v", engine, mod, err)}
		}
		return RecognitionTestResult{OK: true, Message: fmt.Sprintf("Shell 可用；引擎 %s（%s）已安装", engine, mod)}
	default:
		return RecognitionTestResult{OK: false, Message: fmt.Sprintf("未知 ASR provider: %s", provider)}
	}
}

func asrImportModule(engine string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "whisper", "openai-whisper":
		return "whisper", true
	case "faster-whisper":
		return "faster_whisper", true
	default:
		return "", false
	}
}

func resolveASRPython(mediaRoot, shell string) string {
	if candidate := firstQuotedPath(shell); looksLikePython(candidate) {
		return resolveToolPath(mediaRoot, candidate)
	}
	return defaultVenvPython(mediaRoot)
}

func looksLikePython(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	return strings.Contains(base, "python")
}

func firstQuotedPath(shell string) string {
	s := strings.TrimSpace(shell)
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		if end := strings.Index(s[1:], `"`); end >= 0 {
			return s[1 : 1+end]
		}
	}
	if s[0] == '\'' {
		if end := strings.Index(s[1:], `'`); end >= 0 {
			return s[1 : 1+end]
		}
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], `"'`)
}

func defaultVenvPython(mediaRoot string) string {
	mediaRoot = strings.TrimSpace(mediaRoot)
	if mediaRoot == "" {
		return ""
	}
	candidates := []string{
		filepath.Join(mediaRoot, "tools", "recognition", ".venv", "Scripts", "python.exe"),
		filepath.Join(mediaRoot, "tools", "recognition", ".venv", "bin", "python"),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// CheckOCRConfig verifies OCR tool paths and helper script.
func CheckOCRConfig(ctx context.Context, mediaRoot string, ocr OCRConfig) RecognitionTestResult {
	if !ocr.Enabled {
		return RecognitionTestResult{OK: true, Message: "OCR 未启用"}
	}
	mediaRoot = strings.TrimSpace(mediaRoot)
	script := resolveOCRPath(mediaRoot, ocr.ScriptPath)
	if _, err := os.Stat(script); err != nil {
		return RecognitionTestResult{OK: false, Message: fmt.Sprintf("OCR 脚本不存在: %s", script)}
	}
	py := defaultString(ocr.PythonPath, "python3")
	if runtime.GOOS == "windows" {
		py = defaultString(ocr.PythonPath, "python")
	}
	py = resolveToolPath(mediaRoot, py)
	if py == "" {
		py = defaultString(ocr.PythonPath, "python3")
		if runtime.GOOS == "windows" {
			py = defaultString(ocr.PythonPath, "python")
		}
	}
	if err := runProbe(ctx, py, "--version"); err != nil {
		return RecognitionTestResult{OK: false, Message: fmt.Sprintf("Python 不可用 (%s): %v", py, err)}
	}
	tess := defaultString(ocr.TesseractPath, "tesseract")
	tess = resolveToolPath(mediaRoot, tess)
	if tess == "" {
		tess = defaultString(ocr.TesseractPath, "tesseract")
	}
	if err := runProbe(ctx, tess, "--version"); err != nil {
		return RecognitionTestResult{OK: false, Message: fmt.Sprintf("Tesseract 不可用 (%s): %v", tess, err)}
	}
	if p := strings.TrimSpace(ocr.TessdataPrefix); p != "" {
		p = resolveToolPath(mediaRoot, p)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return RecognitionTestResult{OK: false, Message: fmt.Sprintf("tessdata_prefix 目录无效: %s", p)}
		}
	}
	for _, pair := range []struct {
		path, label string
	}{
		{ocr.PgsripPath, "pgsrip"},
		{ocr.MkvextractPath, "mkvextract"},
		{ocr.MkvmergePath, "mkvmerge"},
	} {
		p := resolveToolPath(mediaRoot, pair.path)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			return RecognitionTestResult{OK: false, Message: fmt.Sprintf("%s 路径无效: %s", pair.label, p)}
		}
	}
	lang := strings.TrimSpace(ocr.Languages)
	if lang == "" {
		return RecognitionTestResult{OK: true, Message: "OCR 工具链可用（未指定 languages，将使用 Tesseract 默认语言）"}
	}
	return RecognitionTestResult{OK: true, Message: fmt.Sprintf("OCR 工具链可用（语言: %s）", lang)}
}

func runProbe(ctx context.Context, name string, args ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%w: %s", err, trimBytes(out))
		}
		return err
	}
	return nil
}
