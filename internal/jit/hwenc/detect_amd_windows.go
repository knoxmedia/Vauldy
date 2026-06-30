//go:build windows

package hwenc

func detectAMDGPU() bool {
	return gpuNamesContainAMD(windowsVideoControllerNames())
}
