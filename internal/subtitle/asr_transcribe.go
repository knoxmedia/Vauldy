package subtitle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"knox-media/internal/storage"
)

// TranscribeToVTT runs configured ASR on an audio/video file and writes WebVTT to outputVTT.
// mediaID is used to decrypt Knox .enc inputs before ffmpeg/ASR (same path as subtitle ASR).
func (s *Service) TranscribeToVTT(ctx context.Context, mediaID int64, inputPath, outputVTT string) error {
	if !s.shouldRunASR() {
		return fmt.Errorf("ASR 未配置（请在系统选项中启用 subtitle.asr.provider）")
	}
	inputPath = strings.TrimSpace(inputPath)
	outputVTT = strings.TrimSpace(outputVTT)
	if inputPath == "" || outputVTT == "" {
		return fmt.Errorf("invalid paths")
	}
	if err := os.MkdirAll(filepath.Dir(outputVTT), 0o755); err != nil {
		return err
	}
	outDir := filepath.Dir(outputVTT)

	asrInput, asrCleanup, err := s.asrInputPath(ctx, mediaID, inputPath, outDir)
	if err != nil {
		return err
	}
	defer asrCleanup()

	switch strings.ToLower(strings.TrimSpace(s.ASR.Provider)) {
	case "whisper_cli":
		wp := s.resolveMediaPath(strings.TrimSpace(s.ASR.WhisperPath))
		if wp == "" {
			wp = "whisper"
		}
		args := []string{asrInput, "--output_format", "vtt", "--output_dir", outDir}
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
		base := strings.TrimSuffix(filepath.Base(asrInput), filepath.Ext(asrInput))
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
		shellInput := asrInput
		shellCleanup := func() {}
		if storage.InputNeedsPipe(s.DB, mediaID, inputPath) {
			var matErr error
			shellInput, shellCleanup, matErr = storage.MaterializePlaintextTemp(s.DB, s.Vault, mediaID, inputPath)
			if matErr != nil {
				return matErr
			}
		}
		defer shellCleanup()
		sh = strings.ReplaceAll(sh, "{input}", shellInput)
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
