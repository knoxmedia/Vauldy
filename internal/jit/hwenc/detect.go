// Package hwenc detects FFmpeg H.264 hardware encoders (Intel QSV, AMD AMF, NVIDIA NVENC, VAAPI).
package hwenc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ID names the selected FFmpeg video encoder codec.
type ID string

const (
	Libx264   ID = "libx264"
	H264QSV   ID = "h264_qsv"
	H264AMF   ID = "h264_amf"
	H264NVENC ID = "h264_nvenc"
	H264VAAPI ID = "h264_vaapi"
)

// HardwareAccelOption is the value stored in system options (transcoder.hardware_acceleration).
type HardwareAccelOption string

const (
	HWAccelNone  HardwareAccelOption = "none"
	HWAccelAMF   HardwareAccelOption = "amf"
	HWAccelNVENC HardwareAccelOption = "nvenc"
	HWAccelQSV   HardwareAccelOption = "qsv"
	HWAccelVAAPI HardwareAccelOption = "vaapi"
)

var hwAccelPriority = []HardwareAccelOption{
	HWAccelNVENC,
	HWAccelQSV,
	HWAccelVAAPI,
	HWAccelAMF,
}

type hwDetectContext struct {
	GOOS          string
	NvidiaPresent bool
	AMDPresent    bool
	IntelPresent  bool
	RenderNodeOK  bool
}

func currentDetectContext() hwDetectContext {
	return hwDetectContext{
		GOOS:          runtime.GOOS,
		NvidiaPresent: detectNvidiaGPU(),
		AMDPresent:    detectAMDGPU(),
		IntelPresent:  detectIntelGPU(),
		RenderNodeOK:  linuxRenderNodeOK(),
	}
}

// DetectHWAccel picks the best available hardware acceleration mode.
// Priority: NVIDIA NVENC → Intel QSV → VAAPI → AMD AMF → none (software).
func DetectHWAccel(ffmpegPath string) string {
	encoders, ok := ffmpegEncodersLower(ffmpegPath)
	if !ok {
		return string(HWAccelNone)
	}
	available := listAvailableHWAccel(encoders, currentDetectContext())
	for _, pref := range hwAccelPriority {
		for _, item := range available {
			if item == string(pref) {
				return string(pref)
			}
		}
	}
	return string(HWAccelNone)
}

// DetectH264Encoder picks the best available H.264 encoder from FFmpeg's build.
func DetectH264Encoder(ffmpegPath string) ID {
	if id, ok := HardwareAccelToEncoder(DetectHWAccel(ffmpegPath)); ok {
		return id
	}
	return Libx264
}

// ListAvailableHardwareAcceleration returns every hardware acceleration option
// validated on this host (for the admin UI dropdown).
func ListAvailableHardwareAcceleration(ffmpegPath string) []string {
	encoders, ok := ffmpegEncodersLower(ffmpegPath)
	if !ok {
		return nil
	}
	return listAvailableHWAccel(encoders, currentDetectContext())
}

func listAvailableHWAccel(encoders string, ctx hwDetectContext) []string {
	encoders = strings.ToLower(encoders)
	out := make([]string, 0, 4)

	// NVIDIA NVENC: nvidia-smi + ffmpeg encoder double verification.
	if ctx.NvidiaPresent && strings.Contains(encoders, " h264_nvenc") {
		out = append(out, string(HWAccelNVENC))
	}

	// Intel QSV: Intel GPU + ffmpeg encoder.
	if ctx.IntelPresent && strings.Contains(encoders, " h264_qsv") {
		out = append(out, string(HWAccelQSV))
	}

	// VAAPI: Linux render node + ffmpeg encoder.
	if ctx.GOOS == "linux" && ctx.RenderNodeOK && strings.Contains(encoders, " h264_vaapi") {
		out = append(out, string(HWAccelVAAPI))
	}

	// AMD AMF: AMD GPU + ffmpeg encoder (supports hybrid NVIDIA+AMD hosts).
	if ctx.AMDPresent && strings.Contains(encoders, " h264_amf") {
		out = append(out, string(HWAccelAMF))
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

func ffmpegEncodersLower(ffmpegPath string) (string, bool) {
	out, err := exec.Command(ffmpegPath, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		return "", false
	}
	return strings.ToLower(string(out)), true
}

// detectNvidiaGPU checks PATH, Windows common install paths, and direct execution.
func detectNvidiaGPU() bool {
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		return nvidiaSMIWorks("nvidia-smi")
	}

	if runtime.GOOS == "windows" {
		commonPaths := []string{
			filepath.Join(os.Getenv("SystemRoot"), "System32", "nvidia-smi.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "NVIDIA Corporation", "NVSMI", "nvidia-smi.exe"),
		}
		for _, p := range commonPaths {
			if _, err := os.Stat(p); err == nil && nvidiaSMIWorks(p) {
				return true
			}
		}
		return nvidiaSMIWorks("nvidia-smi")
	}

	return false
}

func nvidiaSMIWorks(path string) bool {
	out, err := exec.Command(path, "--query-gpu=name", "--format=csv,noheader").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func linuxRenderNodeOK() bool {
	if p := strings.TrimSpace(os.Getenv("KNOX_MEDIA_VAAPI_DEVICE")); p != "" {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}
	if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
		return true
	}
	matches, _ := filepath.Glob("/dev/dri/renderD*")
	return len(matches) > 0
}

// VAAPIDevice returns the render node path used for -vaapi_device (Linux).
func VAAPIDevice() string {
	if p := strings.TrimSpace(os.Getenv("KNOX_MEDIA_VAAPI_DEVICE")); p != "" {
		return p
	}
	if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
		return "/dev/dri/renderD128"
	}
	matches, _ := filepath.Glob("/dev/dri/renderD*")
	if len(matches) > 0 {
		return matches[0]
	}
	return "/dev/dri/renderD128"
}

// ParseEncoder maps env/config strings to ID; second return is false if unknown.
func ParseEncoder(s string) (ID, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return "", false
	case "libx264", "x264", "sw", "software":
		return Libx264, true
	case "h264_qsv", "qsv":
		return H264QSV, true
	case "h264_amf", "amf":
		return H264AMF, true
	case "h264_nvenc", "nvenc":
		return H264NVENC, true
	case "h264_vaapi", "vaapi":
		return H264VAAPI, true
	default:
		return "", false
	}
}

// HardwareAccelToEncoder maps a system-options hardware_acceleration value to FFmpeg encoder ID.
func HardwareAccelToEncoder(option string) (ID, bool) {
	switch HardwareAccelOption(strings.ToLower(strings.TrimSpace(option))) {
	case HWAccelAMF:
		return H264AMF, true
	case HWAccelNVENC:
		return H264NVENC, true
	case HWAccelQSV:
		return H264QSV, true
	case HWAccelVAAPI:
		return H264VAAPI, true
	default:
		return "", false
	}
}
