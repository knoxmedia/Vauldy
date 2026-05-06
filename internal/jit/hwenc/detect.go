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

// DetectH264Encoder picks the best available H.264 encoder from FFmpeg's build.
// Priority: Intel QSV → AMD AMF → NVIDIA NVENC → VAAPI (Linux render node only) → libx264.
func DetectH264Encoder(ffmpegPath string) ID {
	text, ok := ffmpegEncodersLower(ffmpegPath)
	if !ok {
		return Libx264
	}
	checks := []struct {
		substr string
		id     ID
		skip   func() bool
	}{
		{" h264_nvenc", H264NVENC, nil},
		{" h264_qsv", H264QSV, nil},
		{" h264_amf", H264AMF, nil},
		{" h264_vaapi", H264VAAPI, func() bool {
			return runtime.GOOS == "linux" && !vaapiRenderNodeOK()
		}},
	}
	for _, c := range checks {
		if c.skip != nil && c.skip() {
			continue
		}
		if strings.Contains(text, c.substr) {
			return c.id
		}
	}
	return Libx264
}

func ffmpegEncodersLower(ffmpegPath string) (string, bool) {
	out, err := exec.Command(ffmpegPath, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		return "", false
	}
	return strings.ToLower(string(out)), true
}

func vaapiRenderNodeOK() bool {
	if p := strings.TrimSpace(os.Getenv("KNOX_MEDIA_VAAPI_DEVICE")); p != "" {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}
	matches, _ := filepath.Glob("/dev/dri/renderD*")
	return len(matches) > 0
}

// VAAPIDevice returns the render node path used for -vaapi_device (Linux).
func VAAPIDevice() string {
	if p := strings.TrimSpace(os.Getenv("KNOX_MEDIA_VAAPI_DEVICE")); p != "" {
		return p
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
