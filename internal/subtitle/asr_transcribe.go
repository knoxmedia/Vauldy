package subtitle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TranscribeToVTT runs configured ASR on an audio/video file and writes WebVTT to outputVTT.
func (s *Service) TranscribeToVTT(ctx context.Context, inputPath, outputVTT string) error {
	if !s.shouldRunASR() {
		return fmt.Errorf("ASR 未配置（请在系统选项中启用 subtitle.asr.provider）")
	}
	inputPath = strings.TrimSpace(inputPath)
	outputVTT = strings.TrimSpace(outputVTT)
	if inputPath == "" || outputVTT == "" {
		return fmt.Errorf("invalid paths")
	}
	if fi, err := os.Stat(inputPath); err != nil || fi.IsDir() {
		return fmt.Errorf("input missing")
	}
	if err := os.MkdirAll(filepath.Dir(outputVTT), 0o755); err != nil {
		return err
	}
	outDir := filepath.Dir(outputVTT)

	switch strings.ToLower(strings.TrimSpace(s.ASR.Provider)) {
	case "whisper_cli":
		wp := s.resolveMediaPath(strings.TrimSpace(s.ASR.WhisperPath))
		if wp == "" {
			wp = "whisper"
		}
		args := []string{inputPath, "--output_format", "vtt", "--output_dir", outDir}
		args = append(args, s.ASR.ExtraArgs...)
		cmd := exec.CommandContext(ctx, wp, args...)
		s.applyToolEnv(cmd)
		if root := s.toolWorkDir(); root != "" {
			cmd.Dir = root
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, trimBytes(out))
		}
		base := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
		gen := filepath.Join(outDir, base+".vtt")
		if err := os.Rename(gen, outputVTT); err != nil {
			if b, e := os.ReadFile(gen); e == nil {
				if wErr := os.WriteFile(outputVTT, b, 0o644); wErr != nil {
					return wErr
				}
			} else {
				return err
			}
		}
	case "shell":
		sh := strings.TrimSpace(s.ASR.Shell)
		if sh == "" {
			return fmt.Errorf("asr.shell empty")
		}
		sh = strings.ReplaceAll(sh, "{input}", inputPath)
		sh = strings.ReplaceAll(sh, "{output_dir}", outDir)
		sh = strings.ReplaceAll(sh, "{output_vtt}", outputVTT)
		sh = resolveShellMediaPaths(sh, s.MediaRoot)
		if _, err := s.runShellCommand(ctx, sh); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported asr provider")
	}

	b, err := os.ReadFile(outputVTT)
	if err != nil {
		return fmt.Errorf("asr output missing: %w", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return fmt.Errorf("asr output empty")
	}
	return nil
}
