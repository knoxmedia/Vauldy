package photoface

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"knox-media/internal/config"
)

// DetectConfig controls the Python face detection script.
type DetectConfig struct {
	PythonPath string
	ScriptPath string
}

// DetectedFace is one face from the detector script.
type DetectedFace struct {
	BBox      [4]float64 `json:"bbox"`
	Embedding []float64  `json:"embedding"`
	Score     float64    `json:"score"`
}

// DetectResult is JSON output from detect.py.
type DetectResult struct {
	Faces  []DetectedFace `json:"faces"`
	Engine string         `json:"engine"`
	Error  string         `json:"error,omitempty"`
}

func ResolveScriptPath(mediaRoot, scriptPath string) string {
	scriptPath = strings.TrimSpace(scriptPath)
	if scriptPath == "" {
		return ""
	}
	if filepath.IsAbs(scriptPath) {
		return scriptPath
	}
	if mediaRoot != "" {
		return filepath.Clean(filepath.Join(mediaRoot, scriptPath))
	}
	return filepath.Clean(scriptPath)
}

func ConfigFrom(cfg config.PhotoFaceConfig, mediaRoot string) DetectConfig {
	py := strings.TrimSpace(cfg.PythonPath)
	if py == "" {
		if runtime.GOOS == "windows" {
			py = "python"
		} else {
			py = "python3"
		}
	}
	script := ResolveScriptPath(mediaRoot, cfg.ScriptPath)
	if !filepath.IsAbs(py) && mediaRoot != "" {
		py = ResolveScriptPath(mediaRoot, py)
	}
	return DetectConfig{PythonPath: py, ScriptPath: script}
}

// RunDetect invokes detect.py on a local image file.
func RunDetect(ctx context.Context, cfg DetectConfig, imagePath string) (*DetectResult, error) {
	script := strings.TrimSpace(cfg.ScriptPath)
	if script == "" {
		return nil, fmt.Errorf("face detect script not configured")
	}
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("face detect script missing: %w", err)
	}
	imagePath = strings.TrimSpace(imagePath)
	if imagePath == "" {
		return nil, fmt.Errorf("empty image path")
	}
	if _, err := os.Stat(imagePath); err != nil {
		return nil, fmt.Errorf("image not readable: %w", err)
	}
	py := strings.TrimSpace(cfg.PythonPath)
	if py == "" {
		if runtime.GOOS == "windows" {
			py = "python"
		} else {
			py = "python3"
		}
	}
	cmd := exec.CommandContext(ctx, py, script, "--input", imagePath)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	out, err := cmd.CombinedOutput()
	res, parseErr := parseDetectOutput(out)
	if parseErr != nil {
		if err != nil {
			return nil, fmt.Errorf("face detect script: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil, parseErr
	}
	if err != nil {
		// Script may exit non-zero after printing JSON error payload.
		if res.Error != "" {
			return nil, fmt.Errorf("%s", res.Error)
		}
		return nil, fmt.Errorf("face detect script: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if res.Error != "" {
		return nil, fmt.Errorf("%s", res.Error)
	}
	return res, nil
}

func parseDetectOutput(out []byte) (*DetectResult, error) {
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, fmt.Errorf("empty face detect output")
	}
	var res DetectResult
	if err := json.Unmarshal(out, &res); err == nil {
		return &res, nil
	}
	lines := strings.Split(raw, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var lineRes DetectResult
		if err := json.Unmarshal([]byte(line), &lineRes); err != nil {
			continue
		}
		return &lineRes, nil
	}
	return nil, fmt.Errorf("parse face detect output: %s", trimDetectOut(out))
}

func trimDetectOut(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		return s[:500]
	}
	return s
}
