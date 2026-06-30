package transcode

import (
	"encoding/json"
	"strconv"
	"strings"

	"knox-media/internal/jit/hwenc"
)

// Settings mirrors transcoder fields from system_options.options_json.
type Settings struct {
	Quality                   string
	BackgroundX264Preset      string
	HardwareAcceleration      string
	EnableHardwareEncoding    bool
	DisableVideoStream        bool
	MaxCPUConcurrent          int // 0 = unlimited
	MaxBackgroundConcurrent   int // 0 = unlimited
}

type systemOptionsTranscoder struct {
	Quality                       string `json:"quality"`
	BackgroundX264Preset          string `json:"background_x264_preset"`
	HardwareAcceleration          string `json:"hardware_acceleration"`
	EnableHardwareEncoding        bool   `json:"enable_hardware_encoding"`
	DisableVideoStreamTranscoding bool   `json:"disable_video_stream_transcoding"`
	MaxCPUConcurrent              string `json:"max_cpu_concurrent"`
	MaxBackgroundConcurrent       string `json:"max_background_concurrent"`
}

// SettingsFromOptionsJSON parses the full system_options document.
func SettingsFromOptionsJSON(raw string) Settings {
	def := DefaultSettings()
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return def
	}
	var doc struct {
		Transcoder systemOptionsTranscoder `json:"transcoder"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return def
	}
	return normalizeSettings(doc.Transcoder, def)
}

func DefaultSettings() Settings {
	return Settings{
		Quality:                 "auto",
		BackgroundX264Preset:    "veryfast",
		MaxCPUConcurrent:        0,
		MaxBackgroundConcurrent: 0,
	}
}

func normalizeSettings(in systemOptionsTranscoder, def Settings) Settings {
	out := def
	if q := strings.TrimSpace(in.Quality); q != "" {
		out.Quality = q
	}
	switch out.Quality {
	case "auto", "max", "high", "medium", "low":
	default:
		out.Quality = "auto"
	}
	if p := strings.TrimSpace(in.BackgroundX264Preset); p != "" {
		out.BackgroundX264Preset = p
	}
	if !validX264Preset(out.BackgroundX264Preset) {
		out.BackgroundX264Preset = def.BackgroundX264Preset
	}
	if accel := strings.TrimSpace(in.HardwareAcceleration); accel != "" {
		out.HardwareAcceleration = accel
	}
	switch out.HardwareAcceleration {
	case "none", "amf", "nvenc", "qsv", "vaapi":
	default:
		out.HardwareAcceleration = "none"
	}
	out.EnableHardwareEncoding = in.EnableHardwareEncoding
	if out.HardwareAcceleration == "none" {
		out.EnableHardwareEncoding = false
	}
	out.DisableVideoStream = in.DisableVideoStreamTranscoding
	out.MaxCPUConcurrent = parseConcurrentLimit(in.MaxCPUConcurrent)
	out.MaxBackgroundConcurrent = parseConcurrentLimit(in.MaxBackgroundConcurrent)
	return out
}

func parseConcurrentLimit(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "unlimited" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func validX264Preset(p string) bool {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow":
		return true
	default:
		return false
	}
}

// InstantX264Preset maps transcoder quality to an x264 preset for live/JIT sessions.
func (s Settings) InstantX264Preset() string {
	switch strings.ToLower(strings.TrimSpace(s.Quality)) {
	case "max":
		return "medium"
	case "high":
		return "faster"
	case "medium":
		return "veryfast"
	case "low":
		return "ultrafast"
	default:
		return "veryfast"
	}
}

// InstantCRF maps transcoder quality to x264 CRF for live/JIT sessions.
func (s Settings) InstantCRF() int {
	switch strings.ToLower(strings.TrimSpace(s.Quality)) {
	case "max":
		return 18
	case "high":
		return 20
	case "medium":
		return 23
	case "low":
		return 28
	default:
		return 23
	}
}

// EffectiveBackgroundPreset returns the validated background x264 preset string.
func (s Settings) EffectiveBackgroundPreset() string {
	if validX264Preset(s.BackgroundX264Preset) {
		return strings.ToLower(strings.TrimSpace(s.BackgroundX264Preset))
	}
	return "veryfast"
}

// InstantSlots reports whether a new real-time playback transcode session may start.
func InstantSlots(settings Settings, activeSessions int) bool {
	if settings.MaxCPUConcurrent <= 0 {
		return true
	}
	return activeSessions < settings.MaxCPUConcurrent
}

// BackgroundSlots computes how many background transcode jobs may start.
func BackgroundSlots(settings Settings, runningBackground, waiting int) int {
	if waiting <= 0 {
		return 0
	}
	slots := waiting
	if settings.MaxBackgroundConcurrent > 0 {
		bg := settings.MaxBackgroundConcurrent - runningBackground
		if bg < slots {
			slots = bg
		}
	}
	if slots < 0 {
		return 0
	}
	return slots
}

// EffectiveHWEncoderID returns the FFmpeg encoder selected by system options.
func (s Settings) EffectiveHWEncoderID() hwenc.ID {
	if !s.EnableHardwareEncoding {
		return hwenc.Libx264
	}
	accel := strings.TrimSpace(s.HardwareAcceleration)
	if accel == "" || accel == "none" {
		return hwenc.Libx264
	}
	if id, ok := hwenc.HardwareAccelToEncoder(accel); ok {
		return id
	}
	return hwenc.Libx264
}

// EffectiveEncoderBackend maps system options to the package worker encoder enum.
func (s Settings) EffectiveEncoderBackend() EncoderBackend {
	switch s.EffectiveHWEncoderID() {
	case hwenc.H264QSV:
		return EncoderQSV
	case hwenc.H264AMF:
		return EncoderAMF
	case hwenc.H264NVENC:
		return EncoderNVENC
	case hwenc.H264VAAPI:
		return EncoderVAAPI
	default:
		return EncoderX264
	}
}
