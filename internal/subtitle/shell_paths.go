package subtitle

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (s *Service) resolveMediaPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	root := strings.TrimSpace(s.MediaRoot)
	if root == "" {
		return p
	}
	return filepath.Clean(filepath.Join(root, filepath.FromSlash(p)))
}

func (s *Service) toolWorkDir() string {
	return strings.TrimSpace(s.MediaRoot)
}

func (s *Service) applyToolEnv(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env,
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
	)
	if ff := s.resolveMediaPath(s.FFmpegPath); ff != "" {
		cmd.Env = append(cmd.Env, "FFMPEG_PATH="+ff)
	}
	if fp := s.resolveMediaPath(s.FFprobePath); fp != "" {
		cmd.Env = append(cmd.Env, "FFPROBE_PATH="+fp)
	}
}

func resolveShellMediaPaths(sh, mediaRoot string) string {
	mediaRoot = strings.TrimSpace(mediaRoot)
	if mediaRoot == "" {
		return sh
	}
	root := filepath.ToSlash(filepath.Clean(mediaRoot))
	for strings.Contains(sh, `"tools/`) {
		sh = strings.Replace(sh, `"tools/`, `"`+root+`/tools/`, 1)
	}
	return sh
}
