package photoface

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
	faceScriptRel = "tools/photo_face/detect.py"
	faceReqRel    = "tools/photo_face/requirements-face.txt"
)

// FaceDeploy holds recommended config paths after one-click install.
type FaceDeploy struct {
	AutoOnScan          bool
	PythonPath          string
	ScriptPath          string
	SimilarityThreshold float32
}

// InstallPhotoFace sets up Python deps for InsightFace face detection.
func InstallPhotoFace(ctx context.Context, mediaRoot string) (FaceDeploy, error) {
	mediaRoot = filepath.Clean(mediaRoot)
	if err := ensureRepoFile(mediaRoot, faceScriptRel); err != nil {
		return FaceDeploy{}, err
	}
	py, err := recognition.EnsureVenv(ctx, mediaRoot)
	if err != nil {
		return FaceDeploy{}, err
	}
	reqPath := filepath.Join(mediaRoot, filepath.FromSlash(faceReqRel))
	if _, err := os.Stat(reqPath); err != nil {
		return FaceDeploy{}, fmt.Errorf("缺少 %s", faceReqRel)
	}
	out, err := runInstallPython(ctx, py, mediaRoot, "-m", "pip", "install", "--default-timeout=600", "-r", reqPath)
	if err != nil {
		return FaceDeploy{}, fmt.Errorf("pip install face deps: %w: %s", err, trimInstallOut(out))
	}
	autoOn := true
	return FaceDeploy{
		AutoOnScan:          autoOn,
		PythonPath:          relIfUnder(mediaRoot, py),
		ScriptPath:          faceScriptRel,
		SimilarityThreshold: 0.45,
	}, nil
}

func DeployToConfig(d FaceDeploy) config.PhotoFaceConfig {
	autoOn := d.AutoOnScan
	th := d.SimilarityThreshold
	if th <= 0 {
		th = 0.45
	}
	return config.PhotoFaceConfig{
		AutoOnScan:          &autoOn,
		PythonPath:          d.PythonPath,
		ScriptPath:          d.ScriptPath,
		SimilarityThreshold: th,
	}
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
	return "…\n" + s[len(s)-4000:]
}

func ensureRepoFile(mediaRoot, rel string) error {
	p := filepath.Join(mediaRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("缺少文件 %s（请确认在 media 目录运行服务）", rel)
	}
	return nil
}

func relIfUnder(base, abs string) string {
	abs = filepath.Clean(abs)
	base = filepath.Clean(base)
	if rel, err := filepath.Rel(base, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return filepath.ToSlash(rel)
	}
	return abs
}
