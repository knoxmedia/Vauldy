// Package hwenc detects FFmpeg H.264 hardware encoders (Intel QSV, AMD AMF, NVIDIA NVENC, VAAPI).
package hwenc

import (
	"knox-media/internal/processmetrics"
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

	Libx265   ID = "libx265"
	HEVCQSV   ID = "hevc_qsv"
	HEVCAMF   ID = "hevc_amf"
	HEVCNVENC ID = "hevc_nvenc"
	HEVCVAAPI ID = "hevc_vaapi"
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
	out, err := processmetrics.NewFFmpegCommand(ffmpegPath, "-hide_banner", "-encoders").CombinedOutput()
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

// EncoderInfo describes a single available encoder for the admin UI.
type EncoderInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Family string `json:"family"` // "h264" or "h265"
	Type   string `json:"type"`   // "software" or "hardware"
}

// ListAvailableEncoders returns every H.264 / H.265 encoder (software + detected
// hardware) that FFmpeg on this host supports.  Designed for the admin UI codec
// dropdown so users only see codecs that will actually work.
func ListAvailableEncoders(ffmpegPath string) []EncoderInfo {
	encoders := make([]EncoderInfo, 0, 10)
	// Software encoders are always available if FFmpeg exists.
	encoders = append(encoders,
		EncoderInfo{ID: string(Libx264), Name: "H.264 (libx264)", Family: "h264", Type: "software"},
		EncoderInfo{ID: string(Libx265), Name: "H.265 (libx265)", Family: "h265", Type: "software"},
	)

	ffout, ok := ffmpegEncodersLower(ffmpegPath)
	if !ok {
		return encoders
	}
	ctx := currentDetectContext()

	// H.264 hardware encoders
	if ctx.NvidiaPresent && strings.Contains(ffout, " h264_nvenc") {
		encoders = append(encoders, EncoderInfo{ID: string(H264NVENC), Name: "H.264 NVENC", Family: "h264", Type: "hardware"})
	}
	if ctx.IntelPresent && strings.Contains(ffout, " h264_qsv") {
		encoders = append(encoders, EncoderInfo{ID: string(H264QSV), Name: "H.264 QSV", Family: "h264", Type: "hardware"})
	}
	if ctx.GOOS == "linux" && ctx.RenderNodeOK && strings.Contains(ffout, " h264_vaapi") {
		encoders = append(encoders, EncoderInfo{ID: string(H264VAAPI), Name: "H.264 VAAPI", Family: "h264", Type: "hardware"})
	}
	if ctx.AMDPresent && strings.Contains(ffout, " h264_amf") {
		encoders = append(encoders, EncoderInfo{ID: string(H264AMF), Name: "H.264 AMF", Family: "h264", Type: "hardware"})
	}

	// H.265 hardware encoders
	if ctx.NvidiaPresent && strings.Contains(ffout, " hevc_nvenc") {
		encoders = append(encoders, EncoderInfo{ID: string(HEVCNVENC), Name: "H.265 NVENC", Family: "h265", Type: "hardware"})
	}
	if ctx.IntelPresent && strings.Contains(ffout, " hevc_qsv") {
		encoders = append(encoders, EncoderInfo{ID: string(HEVCQSV), Name: "H.265 QSV", Family: "h265", Type: "hardware"})
	}
	if ctx.GOOS == "linux" && ctx.RenderNodeOK && strings.Contains(ffout, " hevc_vaapi") {
		encoders = append(encoders, EncoderInfo{ID: string(HEVCVAAPI), Name: "H.265 VAAPI", Family: "h265", Type: "hardware"})
	}
	if ctx.AMDPresent && strings.Contains(ffout, " hevc_amf") {
		encoders = append(encoders, EncoderInfo{ID: string(HEVCAMF), Name: "H.265 AMF", Family: "h265", Type: "hardware"})
	}

	return encoders
}

// EncoderListedInFFmpeg reports whether ffmpeg -encoders lists the given codec id.
func EncoderListedInFFmpeg(ffmpegPath, encoderID string) bool {
	encoderID = strings.ToLower(strings.TrimSpace(encoderID))
	if encoderID == "" {
		return false
	}
	ffout, ok := ffmpegEncodersLower(ffmpegPath)
	if !ok {
		return false
	}
	return strings.Contains(ffout, " "+encoderID)
}

// HardwareDecoderAvailable reports whether ffmpeg's build ships a hardware
// decoder that can feed the full-hardware pipeline (PipelineHWFull) of the
// given encoder for a source stream using sourceCodec. NVENC uses NVDEC
// decoders (named *_cuvid) and QSV uses *_qsv decoders. Encoders whose
// full-hardware chain does not rely on hardware decoding (AMF scales on the
// CPU; VAAPI uploads explicitly before scale_vaapi) always report true, so
// their pipeline is never downgraded.
func HardwareDecoderAvailable(ffmpegPath, sourceCodec string, encoder ID) bool {
	decoders, ok := ffmpegDecodersLower(ffmpegPath)
	if !ok {
		return false
	}
	return hardwareDecoderAvailableInList(decoders, sourceCodec, encoder)
}

// hardwareDecoderAvailableInList decides whether a decoder list produced by
// ffmpeg -decoders provides a hardware decoder for sourceCodec on the given
// encoder's acceleration family. Encoders whose full-hardware chain does not
// rely on hardware decoding (AMF scales on the CPU; VAAPI uploads explicitly
// before scale_vaapi) always report true so their pipeline is never downgraded.
func hardwareDecoderAvailableInList(decoders, sourceCodec string, encoder ID) bool {
	sourceCodec = strings.ToLower(strings.TrimSpace(sourceCodec))
	if sourceCodec == "" {
		return false
	}
	switch encoder {
	case H264NVENC, HEVCNVENC:
		return strings.Contains(decoders, " "+sourceCodec+"_cuvid")
	case H264QSV, HEVCQSV:
		return strings.Contains(decoders, " "+sourceCodec+"_qsv")
	default:
		return true
	}
}

func ffmpegDecodersLower(ffmpegPath string) (string, bool) {
	out, err := processmetrics.NewFFmpegCommand(ffmpegPath, "-hide_banner", "-decoders").CombinedOutput()
	if err != nil {
		return "", false
	}
	return strings.ToLower(string(out)), true
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
	case "hevc_nvenc":
		return HEVCNVENC, true
	case "hevc_amf":
		return HEVCAMF, true
	case "hevc_qsv":
		return HEVCQSV, true
	case "hevc_vaapi":
		return HEVCVAAPI, true
	case "libx265", "x265", "hevc":
		return Libx265, true
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
