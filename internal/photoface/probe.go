package photoface

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"knox-media/internal/config"
)

// FaceTestResult is returned by face detect connectivity checks.
type FaceTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// CheckPhotoFaceConfig verifies Python, script, and InsightFace dependencies.
func CheckPhotoFaceConfig(ctx context.Context, mediaRoot string, cfg config.PhotoFaceConfig) FaceTestResult {
	mediaRoot = strings.TrimSpace(mediaRoot)
	script := ResolveScriptPath(mediaRoot, cfg.ScriptPath)
	if script == "" {
		script = ResolveScriptPath(mediaRoot, "tools/photo_face/detect.py")
	}
	if _, err := os.Stat(script); err != nil {
		return FaceTestResult{OK: false, Message: fmt.Sprintf("人脸检测脚本不存在: %s", script)}
	}

	py := resolvePython(cfg.PythonPath, mediaRoot)
	if err := runFaceProbe(ctx, py, "--version"); err != nil {
		return FaceTestResult{OK: false, Message: fmt.Sprintf("Python 不可用 (%s): %v", py, err)}
	}
	if err := runFaceProbe(ctx, py, "-c", "import cv2, numpy; import insightface"); err != nil {
		return FaceTestResult{OK: false, Message: fmt.Sprintf("Python 依赖缺失 (opencv/numpy/insightface): %v", err)}
	}

	tmp, err := writeProbePNG(mediaRoot)
	if err != nil {
		return FaceTestResult{OK: false, Message: fmt.Sprintf("创建测试图片失败: %v", err)}
	}
	defer os.Remove(tmp)

	out, err := runFaceProbeOutput(ctx, mediaRoot, py, script, "--input", tmp)
	if err != nil {
		return FaceTestResult{OK: false, Message: fmt.Sprintf("人脸检测脚本测试失败: %v: %s", err, trimFaceProbeOut(out))}
	}
	if !strings.Contains(string(out), `"faces"`) {
		return FaceTestResult{OK: false, Message: "人脸检测脚本输出无效: " + trimFaceProbeOut(out)}
	}
	return FaceTestResult{OK: true, Message: "人脸检测引擎连接正常（InsightFace）"}
}

func resolvePython(cfgPath, mediaRoot string) string {
	py := strings.TrimSpace(cfgPath)
	if py != "" && !filepath.IsAbs(py) && mediaRoot != "" {
		py = ResolveScriptPath(mediaRoot, py)
	}
	if py != "" {
		return py
	}
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

func writeProbePNG(dir string) (string, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, "knox-face-probe-*.png")
	if err != nil {
		return "", err
	}
	path := f.Name()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 210, G: 180, B: 160, A: 255})
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

func runFaceProbe(ctx context.Context, name string, args ...string) error {
	_, err := runFaceProbeOutput(ctx, "", name, args...)
	return err
}

func runFaceProbeOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
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

func trimFaceProbeOut(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		return s[:500]
	}
	return s
}
