//go:build !windows && !linux

package hwenc

func detectIntelGPU() bool {
	return false
}
