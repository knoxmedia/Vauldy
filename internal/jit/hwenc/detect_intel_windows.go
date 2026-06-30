//go:build windows

package hwenc

func detectIntelGPU() bool {
	return gpuNamesContainIntel(windowsVideoControllerNames())
}
