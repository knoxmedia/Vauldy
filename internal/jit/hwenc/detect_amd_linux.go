//go:build linux

package hwenc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func detectAMDGPU() bool {
	return detectAMDGPULinux()
}

func detectAMDGPULinux() bool {
	if out, err := exec.Command("lspci").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.ToLower(line)
			if !strings.Contains(line, "vga") && !strings.Contains(line, "3d") && !strings.Contains(line, "display") {
				continue
			}
			if strings.Contains(line, "amd") || strings.Contains(line, "advanced micro devices") || strings.Contains(line, "radeon") {
				return true
			}
		}
	}
	matches, _ := filepath.Glob("/sys/class/drm/card*/device/vendor")
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == "0x1002" {
			return true
		}
	}
	return false
}
