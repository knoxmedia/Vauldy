package photoclass

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"knox-media/internal/config"
	"knox-media/internal/recognition"
)

const (
	classifyScriptRel   = "tools/photo_classify/classify.py"
	classifyModelRel    = "tools/photo_classify/models/mobilenetv2-7.onnx"
	classifyLabelsRel   = "tools/photo_classify/imagenet_labels.txt"
	mobilenetONNXURL    = "https://github.com/onnx/models/raw/main/validated/vision/classification/mobilenet/model/mobilenetv2-7.onnx"
	imagenetLabelsURL   = "https://raw.githubusercontent.com/pytorch/hub/master/imagenet_classes.txt"
)

// ClassifyDeploy holds recommended config paths after one-click install.
type ClassifyDeploy struct {
	AutoOnScan  bool
	Engine      string
	PythonPath  string
	ScriptPath  string
	ModelPath   string
	LabelsPath  string
}

// InstallPhotoClassify sets up Python deps, ONNX model, and label file under tools/photo_classify/.
func InstallPhotoClassify(ctx context.Context, mediaRoot string) (ClassifyDeploy, error) {
	mediaRoot = filepath.Clean(mediaRoot)
	if err := ensureRepoFile(mediaRoot, classifyScriptRel); err != nil {
		return ClassifyDeploy{}, err
	}
	py, err := recognition.EnsureVenv(ctx, mediaRoot)
	if err != nil {
		return ClassifyDeploy{}, err
	}
	if err := pipInstallClassify(ctx, py, mediaRoot); err != nil {
		return ClassifyDeploy{}, err
	}
	modelAbs := filepath.Join(mediaRoot, filepath.FromSlash(classifyModelRel))
	if err := os.MkdirAll(filepath.Dir(modelAbs), 0o755); err != nil {
		return ClassifyDeploy{}, err
	}
	if !fileExists(modelAbs) {
		if err := downloadClassifyFile(ctx, mobilenetONNXURL, modelAbs); err != nil {
			return ClassifyDeploy{}, fmt.Errorf("download ONNX model: %w", err)
		}
	}
	labelsAbs := filepath.Join(mediaRoot, filepath.FromSlash(classifyLabelsRel))
	if !fileExists(labelsAbs) {
		if err := downloadClassifyFile(ctx, imagenetLabelsURL, labelsAbs); err != nil {
			return ClassifyDeploy{}, fmt.Errorf("download ImageNet labels: %w", err)
		}
	}
	autoOn := true
	return ClassifyDeploy{
		AutoOnScan: autoOn,
		Engine:     "auto",
		PythonPath: relIfUnder(mediaRoot, py),
		ScriptPath: classifyScriptRel,
		ModelPath:  classifyModelRel,
		LabelsPath: classifyLabelsRel,
	}, nil
}

func pipInstallClassify(ctx context.Context, venvPython, mediaRoot string) error {
	// onnxruntime wheel is ~13MB; do not use the 60s probe timeout.
	base := []string{"-m", "pip", "install", "--default-timeout=600", "--upgrade", "pillow", "numpy"}
	out, err := runInstallPython(ctx, venvPython, mediaRoot, base...)
	if err != nil {
		return fmt.Errorf("pip install pillow/numpy: %w: %s", err, trimInstallOut(out))
	}
	out, err = runInstallPython(ctx, venvPython, mediaRoot, "-m", "pip", "install", "--default-timeout=600", "onnxruntime")
	if err != nil {
		return fmt.Errorf("pip install onnxruntime: %w: %s", err, trimInstallOut(out))
	}
	return nil
}

func runInstallPython(ctx context.Context, venvPython, mediaRoot string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, venvPython, args...)
	cmd.Dir = mediaRoot
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	return cmd.CombinedOutput()
}

func trimInstallOut(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= 4000 {
		return s
	}
	// pip errors usually appear at the end of the log.
	return "…\n" + s[len(s)-4000:]
}

func ensureRepoFile(mediaRoot, rel string) error {
	p := filepath.Join(mediaRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("缺少文件 %s（请确认在 media 目录运行服务）", rel)
	}
	return nil
}

func downloadClassifyFile(ctx context.Context, url, dest string) error {
	return recognition.DownloadFile(ctx, url, dest)
}

func relIfUnder(base, abs string) string {
	abs = filepath.Clean(abs)
	base = filepath.Clean(base)
	if rel, err := filepath.Rel(base, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return filepath.ToSlash(rel)
	}
	return abs
}

func DeployToConfig(d ClassifyDeploy) config.PhotoClassifyConfig {
	autoOn := d.AutoOnScan
	return config.PhotoClassifyConfig{
		AutoOnScan: &autoOn,
		Engine:     d.Engine,
		PythonPath: d.PythonPath,
		ScriptPath: d.ScriptPath,
		ModelPath:  d.ModelPath,
		LabelsPath: d.LabelsPath,
	}
}
