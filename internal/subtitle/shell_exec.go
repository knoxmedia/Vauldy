package subtitle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// stripShellCDPrefix removes a leading "cd /d ... &&" segment from Knox ASR shell templates.
// output_dir is already created and cmd.Dir is set to MediaRoot, so cd is unnecessary and
// often breaks on Windows when passed through cmd /C.
func stripShellCDPrefix(sh string) string {
	sh = strings.TrimSpace(sh)
	lower := strings.ToLower(sh)
	if !strings.HasPrefix(lower, "cd ") {
		return sh
	}
	if idx := strings.Index(sh, "&&"); idx >= 0 {
		return strings.TrimSpace(sh[idx+2:])
	}
	return sh
}

// splitShellCommandLine splits a command line on spaces outside double quotes.
func splitShellCommandLine(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var parts []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			inQuote = !inQuote
		case ' ':
			if inQuote {
				cur.WriteByte(c)
			} else if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

func shouldDirectExecWindows(exe string) bool {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return false
	}
	lower := strings.ToLower(exe)
	if strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".bat") || strings.HasSuffix(lower, ".cmd") {
		return true
	}
	if filepath.IsAbs(exe) {
		if st, err := os.Stat(exe); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

func buildShellCommand(ctx context.Context, sh string) (*exec.Cmd, bool) {
	sh = stripShellCDPrefix(strings.TrimSpace(sh))
	if sh == "" {
		return nil, false
	}
	parts := splitShellCommandLine(sh)
	if len(parts) == 0 {
		return nil, false
	}
	if runtime.GOOS == "windows" && len(parts) >= 2 && shouldDirectExecWindows(parts[0]) {
		// Avoid cmd /C quoting issues for Knox ASR (python.exe + script + args).
		return exec.CommandContext(ctx, parts[0], parts[1:]...), true
	}
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", sh), true
	}
	return exec.CommandContext(ctx, "sh", "-c", sh), true
}

func (s *Service) runShellCommand(ctx context.Context, sh string) ([]byte, error) {
	cmd, ok := buildShellCommand(ctx, sh)
	if !ok {
		return nil, fmt.Errorf("empty shell command")
	}
	s.applyToolEnv(cmd)
	if root := s.toolWorkDir(); root != "" {
		cmd.Dir = root
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := trimBytes(out)
		if detail != "" {
			return out, fmt.Errorf("%w: %s", err, detail)
		}
	}
	return out, err
}
