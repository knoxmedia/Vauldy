package photoclass

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ONNXConfig optional Python/onnxruntime scene refinement.
type ONNXConfig struct {
	Engine     string // auto | heuristic | onnx
	PythonPath string
	ScriptPath string
	ModelPath  string
	LabelsPath string
}

// RunONNX invokes the Python classifier when configured; returns nil if skipped.
func RunONNX(ctx context.Context, cfg ONNXConfig, imagePath string) (*Result, error) {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	if engine == "heuristic" {
		return nil, nil
	}
	script := strings.TrimSpace(cfg.ScriptPath)
	if script == "" {
		return nil, nil
	}
	if _, err := os.Stat(script); err != nil {
		return nil, nil
	}
	py := strings.TrimSpace(cfg.PythonPath)
	if py == "" {
		if runtime.GOOS == "windows" {
			py = "python"
		} else {
			py = "python3"
		}
	}
	args := []string{script, "--input", imagePath, "--engine", cfg.Engine}
	if mp := strings.TrimSpace(cfg.ModelPath); mp != "" {
		args = append(args, "--model", mp)
	}
	if lp := strings.TrimSpace(cfg.LabelsPath); lp != "" {
		args = append(args, "--labels", lp)
	}
	cmd := exec.CommandContext(ctx, py, args...)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("photo classify script: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var res Result
	if err := json.Unmarshal(out, &res); err != nil {
		// Script may log to stderr; try last line as JSON
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "{") {
				if e2 := json.Unmarshal([]byte(lines[i]), &res); e2 == nil {
					return &res, nil
				}
			}
		}
		return nil, fmt.Errorf("parse classify output: %w", err)
	}
	return &res, nil
}

// ResolveScriptPath makes script path absolute relative to media root.
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

func mergeResults(base Result, extra *Result) Result {
	if extra == nil || len(extra.Tags) == 0 {
		return base
	}
	base.Tags = mergeTags(base.Tags, extra.Tags)
	if base.Scores == nil {
		base.Scores = map[string]float64{}
	}
	for k, v := range extra.Scores {
		if prev, ok := base.Scores[k]; !ok || v > prev {
			base.Scores[k] = v
		}
	}
	if extra.Engine != "" {
		base.Engine = extra.Engine
	}
	return base
}

// ClassifyWithONNX runs Go heuristics then optional ONNX refinement.
func ClassifyWithONNX(ctx context.Context, cfg ONNXConfig, in Input) (Result, error) {
	base := Classify(in)
	imagePath := PickImagePath(in.ThumbPath, in.FilePath)
	if imagePath == "" {
		return base, fmt.Errorf("no image path")
	}
	onnxRes, err := RunONNX(ctx, cfg, imagePath)
	if err != nil {
		return base, err
	}
	if onnxRes != nil {
		onnxRes.Tags = NormalizeTags(onnxRes.Tags)
		base = mergeResults(base, onnxRes)
	}
	return base, nil
}
