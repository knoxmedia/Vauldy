//go:build !windows && !linux

package hwenc

func detectAMDGPU() bool {
	return false
}
