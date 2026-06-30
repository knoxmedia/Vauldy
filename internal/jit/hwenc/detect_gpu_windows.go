//go:build windows

package hwenc

import (
	"os/exec"
	"strings"
)

func windowsVideoControllerNames() string {
	if out, err := exec.Command("wmic", "path", "win32_VideoController", "get", "name").Output(); err == nil {
		if text := strings.TrimSpace(string(out)); text != "" {
			return text
		}
	}
	out, err := exec.Command(
		"powershell", "-NoProfile", "-Command",
		"Get-CimInstance Win32_VideoController | Select-Object -ExpandProperty Name",
	).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func gpuNamesContainAMD(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" || line == "name" {
			continue
		}
		if strings.Contains(line, "amd") || strings.Contains(line, "radeon") {
			return true
		}
	}
	return false
}

func gpuNamesContainIntel(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" || line == "name" {
			continue
		}
		if gpuNameLooksLikeIntel(line) {
			return true
		}
	}
	return false
}

func gpuNameLooksLikeIntel(name string) bool {
	if strings.Contains(name, "idd") || strings.Contains(name, "indirect") || strings.Contains(name, "virtual") {
		return false
	}
	return strings.Contains(name, "intel")
}
