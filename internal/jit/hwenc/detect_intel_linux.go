//go:build linux

package hwenc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func detectIntelGPU() bool {
	return detectIntelGPULinux()
}

func detectIntelGPULinux() bool {
	if out, err := exec.Command("lspci").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.ToLower(line)
			if !strings.Contains(line, "vga") && !strings.Contains(line, "3d") && !strings.Contains(line, "display") {
				continue
			}
			if strings.Contains(line, "intel") {
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
		if strings.TrimSpace(string(b)) == "0x8086" {
			return true
		}
	}
	return false
}
