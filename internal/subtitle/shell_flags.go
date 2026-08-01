package subtitle

import (
	"strings"
)

// appendASRShellFlags adds first-class ASR engine/model/language/device flags when
// the shell template does not already include them. Existing shell flags win.
func appendASRShellFlags(shell string, asr ASRConfig) string {
	shell = strings.TrimSpace(shell)
	if shell == "" {
		return shell
	}
	if !hasShellFlag(shell, "--engine") {
		eng := strings.TrimSpace(asr.Engine)
		if eng == "" {
			eng = "whisper"
		}
		shell = appendShellArg(shell, "--engine", eng)
	}
	eng := shellFlagValue(shell, "--engine")
	if eng == "" {
		eng = strings.TrimSpace(asr.Engine)
	}
	switch strings.ToLower(eng) {
	case "whisper", "faster-whisper":
		if !hasShellFlag(shell, "--whisper-model") {
			if m := strings.TrimSpace(asr.Model); m != "" {
				shell = appendShellArg(shell, "--whisper-model", m)
			}
		}
		if !hasShellFlag(shell, "--whisper-language") {
			if lang := strings.TrimSpace(asr.Language); lang != "" {
				shell = appendShellArg(shell, "--whisper-language", lang)
			}
		}
		if !hasShellFlag(shell, "--whisper-device") {
			if dev := strings.TrimSpace(asr.Device); dev != "" {
				shell = appendShellArg(shell, "--whisper-device", dev)
			}
		}
	}
	return shell
}

func hasShellFlag(shell, flag string) bool {
	return strings.Contains(" "+shell+" ", " "+flag+" ") || strings.Contains(shell, " "+flag+"=")
}

func shellFlagValue(shell, flag string) string {
	parts := strings.Fields(shell)
	for i, p := range parts {
		if p == flag && i+1 < len(parts) {
			return strings.Trim(parts[i+1], `"'`)
		}
		if strings.HasPrefix(p, flag+"=") {
			return strings.Trim(strings.TrimPrefix(p, flag+"="), `"'`)
		}
	}
	return ""
}

func appendShellArg(shell, flag, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return shell
	}
	if strings.ContainsAny(value, " \t\"'") {
		value = `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return strings.TrimSpace(shell) + " " + flag + " " + value
}
