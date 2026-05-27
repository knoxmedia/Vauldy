package photoclass

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"knox-media/internal/config"
)

// ClassifyTestResult is returned by photo classify connectivity checks.
type ClassifyTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// CheckPhotoClassifyConfig verifies engine, Python, script, and optional ONNX deps.
func CheckPhotoClassifyConfig(ctx context.Context, mediaRoot string, cfg config.PhotoClassifyConfig) ClassifyTestResult {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	if engine == "" {
		engine = "auto"
	}
	switch engine {
	case "auto", "heuristic", "onnx":
	default:
		return ClassifyTestResult{OK: false, Message: fmt.Sprintf("未知引擎: %s", cfg.Engine)}
	}

	mediaRoot = strings.TrimSpace(mediaRoot)
	script := ResolveScriptPath(mediaRoot, cfg.ScriptPath)
	if script == "" {
		return ClassifyTestResult{OK: false, Message: "分类脚本路径 (script_path) 未设置"}
	}
	if _, err := os.Stat(script); err != nil {
		return ClassifyTestResult{OK: false, Message: fmt.Sprintf("分类脚本不存在: %s", script)}
	}

	if engine == "heuristic" {
		return ClassifyTestResult{OK: true, Message: "启发式引擎可用（无需 Python 依赖）"}
	}

	py := resolvePython(cfg.PythonPath)
	if err := runProbe(ctx, py, "--version"); err != nil {
		return ClassifyTestResult{OK: false, Message: fmt.Sprintf("Python 不可用 (%s): %v", py, err)}
	}

	model := ResolveScriptPath(mediaRoot, cfg.ModelPath)
	labels := ResolveScriptPath(mediaRoot, cfg.LabelsPath)
	needONNX := engine == "onnx" || (engine == "auto" && fileExists(model))

	if needONNX {
		if !fileExists(model) {
			return ClassifyTestResult{OK: false, Message: fmt.Sprintf("ONNX 模型不存在: %s（可使用一键安装）", model)}
		}
		if err := runProbe(ctx, py, "-c", "import PIL, numpy, onnxruntime"); err != nil {
			return ClassifyTestResult{OK: false, Message: fmt.Sprintf("Python 依赖缺失 (Pillow/numpy/onnxruntime): %v", err)}
		}
		if labels != "" && !fileExists(labels) {
			return ClassifyTestResult{OK: false, Message: fmt.Sprintf("标签文件不存在: %s", labels)}
		}
	} else if engine == "auto" {
		// auto without model falls back to heuristic; still verify script runs.
	}

	tmp, err := writeProbePNG(mediaRoot)
	if err != nil {
		return ClassifyTestResult{OK: false, Message: fmt.Sprintf("创建测试图片失败: %v", err)}
	}
	defer os.Remove(tmp)

	args := []string{script, "--input", tmp, "--engine", engine}
	if model != "" {
		args = append(args, "--model", model)
	}
	if labels != "" {
		args = append(args, "--labels", labels)
	}
	out, err := runProbeOutput(ctx, mediaRoot, py, args...)
	if err != nil {
		return ClassifyTestResult{OK: false, Message: fmt.Sprintf("分类脚本测试失败: %v: %s", err, trimProbeOut(out))}
	}
	if !strings.Contains(string(out), `"tags"`) {
		return ClassifyTestResult{OK: false, Message: "分类脚本输出无效: " + trimProbeOut(out)}
	}

	msg := "分类引擎连接正常"
	switch engine {
	case "auto":
		if fileExists(model) {
			msg = "分类引擎连接正常（auto：已检测到 ONNX 模型）"
		} else {
			msg = "分类引擎连接正常（auto：将使用启发式分类）"
		}
	case "onnx":
		msg = "ONNX 分类引擎连接正常"
	}
	return ClassifyTestResult{OK: true, Message: msg}
}

func resolvePython(cfgPath string) string {
	py := strings.TrimSpace(cfgPath)
	if py != "" {
		return py
	}
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func writeProbePNG(dir string) (string, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, "knox-classify-probe-*.png")
	if err != nil {
		return "", err
	}
	path := f.Name()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 120, B: 80, A: 255})
		}
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func runProbe(ctx context.Context, name string, args ...string) error {
	_, err := runProbeOutput(ctx, "", name, args...)
	return err
}

func runProbeOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, err
	}
	return out, nil
}

func trimProbeOut(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		return s[:500]
	}
	return s
}
