//go:build windows

package hwenc

import "testing"

func TestGPUNamesContainAMD(t *testing.T) {
	if !gpuNamesContainAMD("Name\nAMD Radeon(TM) Graphics\nNVIDIA GeForce GTX 1650\n") {
		t.Fatal("expected AMD GPU name to match")
	}
	if gpuNamesContainAMD("Name\nNVIDIA GeForce GTX 1650\n") {
		t.Fatal("expected NVIDIA-only list to not match AMD")
	}
}

func TestGPUNamesContainIntel(t *testing.T) {
	if !gpuNamesContainIntel("Name\nIntel(R) UHD Graphics 630\nNVIDIA GeForce GTX 1650\n") {
		t.Fatal("expected Intel GPU name to match")
	}
	if gpuNamesContainIntel("Name\nOrayIddDriver Device\nNVIDIA GeForce GTX 1650\n") {
		t.Fatal("expected virtual display adapter to not match Intel QSV")
	}
	if gpuNamesContainIntel("Name\nNVIDIA GeForce GTX 1650\n") {
		t.Fatal("expected NVIDIA-only list to not match Intel")
	}
}
