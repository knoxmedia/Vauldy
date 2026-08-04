package pretranscode

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/jit/hwenc"
)

// KeyInfo describes the HLS AES-128 key material FFmpeg consumes via
// -hls_key_info_file (SRS 6.5). The file format is three lines:
//
//	<key-uri>      # served URL clients fetch to obtain the key
//	<key-path>     # local file holding the 16-byte key
//	<iv-hex>       # 16-byte IV as 32 hex chars
type KeyInfo struct {
	KeyURI      string
	KeyPath     string
	IVHex       string
	KeyHex      string
	KeyInfoPath string
}

// FFmpegArgs is the parsed argument vector for a single rendition.
type FFmpegArgs struct {
	Args         []string
	OutDir       string
	OutFile      string
	EncoderUsed  string
	UsesHardware bool
}

// FFmpegGlobalOpts returns the standard quiet/global ffmpeg flags used by pretranscode.
func FFmpegGlobalOpts() []string {
	return []string{"-y", "-hide_banner", "-nostats", "-loglevel", "error"}
}

// AttachFFmpegInput inserts -i <path> after the global opts. Tests use this to
// build a complete argv vector; production code wires input via storage.ApplyFFmpegInput.
func AttachFFmpegInput(args []string, input string) []string {
	prefixLen := len(FFmpegGlobalOpts())
	if len(args) < prefixLen {
		return args
	}
	out := append([]string{}, args[:prefixLen]...)
	out = append(out, "-i", input)
	out = append(out, args[prefixLen:]...)
	return out
}

// BuildHLSArgs constructs the FFmpeg argument vector for an HLS rendition
// (SRS 6.1, 6.5). When keyInfo is non-nil the output is AES-128 encrypted.
// resolvedEncoder, when non-empty, is the encoder chosen by ResolveEncoder.
func BuildHLSArgs(outDir string, p *Preset, r *Rendition, keyInfo *KeyInfo, resolvedEncoder string) FFmpegArgs {
	_ = os.MkdirAll(outDir, 0o755)
	encoder := effectiveEncoder(p, resolvedEncoder)
	usesHW := isHardwareEncoder(encoder)
	args := append(FFmpegGlobalOpts(),
		"-map", "0:v:0", "-map", "0:a:0?",
		"-vf", buildVideoFilter(encoder, r.Height),
		"-c:v", encoder,
	)
	args = appendVideoEncoderOpts(args, encoder, p)
	args = append(args, "-b:v", r.VideoBitrate)
	if p.VideoMaxrate != "" {
		args = append(args, "-maxrate", p.VideoMaxrate)
	}
	if p.VideoBufsize != "" {
		args = append(args, "-bufsize", p.VideoBufsize)
	}
	if p.VideoGOP > 0 {
		args = append(args, "-g", fmt.Sprintf("%d", p.VideoGOP*25))
	}
	if p.VideoPixFmt != "" && !hardwareEncoderSetsPixFmt(encoder) {
		args = append(args, "-pix_fmt", p.VideoPixFmt)
	}
	if p.AudioCodec == "copy" {
		args = append(args, "-c:a", "copy")
	} else {
		audioCodec := p.AudioCodec
		if audioCodec == "" {
			audioCodec = "aac"
		}
		args = append(args, "-c:a", audioCodec)
		if r.AudioBitrate != "" {
			args = append(args, "-b:a", r.AudioBitrate)
		} else if p.AudioBitrate != "" {
			args = append(args, "-b:a", p.AudioBitrate)
		}
		if p.AudioChannels > 0 {
			args = append(args, "-ac", fmt.Sprintf("%d", p.AudioChannels))
		}
		if p.AudioSampleRate > 0 {
			args = append(args, "-ar", fmt.Sprintf("%d", p.AudioSampleRate))
		}
	}
	args = append(args,
		"-f", "hls",
		"-hls_time", "4",
		"-hls_playlist_type", "vod",
	)
	if keyInfo != nil {
		args = append(args, "-hls_key_info_file", keyInfo.KeyInfoPath)
	}
	args = append(args,
		"-hls_segment_filename", filepath.Join(outDir, r.Name+"_%03d.ts"),
		filepath.Join(outDir, r.Name+".m3u8"),
	)
	return FFmpegArgs{Args: args, OutDir: outDir, OutFile: filepath.Join(outDir, r.Name+".m3u8"), EncoderUsed: encoder, UsesHardware: usesHW}
}

// BuildMP4Args constructs the FFmpeg argument vector for an MP4 rendition
// (SRS 6.2). MP4 outputs are never encrypted.
func BuildMP4Args(outDir string, p *Preset, r *Rendition, resolvedEncoder string) FFmpegArgs {
	_ = os.MkdirAll(outDir, 0o755)
	encoder := effectiveEncoder(p, resolvedEncoder)
	out := filepath.Join(outDir, r.Name+".mp4")
	args := append(FFmpegGlobalOpts(),
		"-map", "0:v:0", "-map", "0:a:0?",
		"-vf", buildVideoFilter(encoder, r.Height),
		"-c:v", encoder,
	)
	args = appendVideoEncoderOpts(args, encoder, p)
	args = append(args, "-b:v", r.VideoBitrate)
	if p.VideoMaxrate != "" {
		args = append(args, "-maxrate", p.VideoMaxrate)
	}
	if p.VideoBufsize != "" {
		args = append(args, "-bufsize", p.VideoBufsize)
	}
	if p.AudioCodec == "copy" {
		args = append(args, "-c:a", "copy")
	} else {
		audioCodec := p.AudioCodec
		if audioCodec == "" {
			audioCodec = "aac"
		}
		args = append(args, "-c:a", audioCodec)
		if r.AudioBitrate != "" {
			args = append(args, "-b:a", r.AudioBitrate)
		} else if p.AudioBitrate != "" {
			args = append(args, "-b:a", p.AudioBitrate)
		}
		if p.AudioChannels > 0 {
			args = append(args, "-ac", fmt.Sprintf("%d", p.AudioChannels))
		}
		if p.AudioSampleRate > 0 {
			args = append(args, "-ar", fmt.Sprintf("%d", p.AudioSampleRate))
		}
	}
	args = append(args, "-movflags", "+faststart", out)
	return FFmpegArgs{Args: args, OutDir: outDir, OutFile: out, EncoderUsed: encoder, UsesHardware: isHardwareEncoder(encoder)}
}

// GenerateAES128KeyInfo creates a 16-byte key + 16-byte IV, writes the key
// file and the keyinfo file, and returns the descriptor. Caller persists
// KeyHex/IVHex to drm_key_material (SRS ENC-06).
func GenerateAES128KeyInfo(outDir string, taskID int64, fileID string) (*KeyInfo, error) {
	key := make([]byte, 16)
	iv := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("key rand: %w", err)
	}
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("iv rand: %w", err)
	}
	_ = os.MkdirAll(outDir, 0o755)
	keyPath := filepath.Join(outDir, "enc.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	keyHex := hex.EncodeToString(key)
	ivHex := hex.EncodeToString(iv)
	keyURI := fmt.Sprintf("/api/v1/drm/hls-key/%d", taskID)
	keyInfoPath := filepath.Join(outDir, "enc.keyinfo")
	content := fmt.Sprintf("%s\n%s\n%s\n", keyURI, keyPath, ivHex)
	if err := os.WriteFile(keyInfoPath, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("write keyinfo: %w", err)
	}
	return &KeyInfo{
		KeyURI:      keyURI,
		KeyPath:     keyPath,
		IVHex:       ivHex,
		KeyHex:      keyHex,
		KeyInfoPath: keyInfoPath,
	}, nil
}

func effectiveEncoder(p *Preset, resolvedEncoder string) string {
	if enc := strings.TrimSpace(resolvedEncoder); enc != "" {
		return enc
	}
	return chooseEncoder(p)
}

func buildVideoFilter(encoder string, height int) string {
	switch encoder {
	case "h264_vaapi", "hevc_vaapi":
		return fmt.Sprintf("format=nv12,hwupload,scale_vaapi=w=-2:h=%d", height)
	case "h264_amf", "hevc_amf", "h264_qsv", "hevc_qsv":
		return fmt.Sprintf("scale=-2:%d,format=nv12", height)
	default:
		return fmt.Sprintf("scale=-2:%d", height)
	}
}

func appendVideoEncoderOpts(args []string, encoder string, p *Preset) []string {
	switch encoder {
	case "libx264", "libx265":
		if p.VideoPreset != "" {
			args = append(args, "-preset", p.VideoPreset)
		}
		if p.VideoCRF > 0 {
			args = append(args, "-crf", fmt.Sprintf("%d", p.VideoCRF))
		}
		if p.VideoProfile != "" {
			args = append(args, "-profile:v", p.VideoProfile)
		}
	case "h264_nvenc", "hevc_nvenc":
		args = append(args, "-preset", nvencPreset(p.VideoPreset))
		if p.VideoProfile != "" {
			args = append(args, "-profile:v", p.VideoProfile)
		}
	case "h264_amf", "hevc_amf":
		args = append(args, "-quality", amfQuality(p.VideoPreset), "-rc", "vbr_peak")
		if p.VideoProfile != "" && encoder == "h264_amf" {
			args = append(args, "-profile:v", p.VideoProfile)
		}
	case "h264_qsv", "hevc_qsv":
		args = append(args, "-preset", qsvPreset(p.VideoPreset))
		if p.VideoProfile != "" {
			args = append(args, "-profile:v", p.VideoProfile)
		}
	}
	return args
}

func hardwareEncoderSetsPixFmt(encoder string) bool {
	switch encoder {
	case "h264_amf", "hevc_amf", "h264_qsv", "hevc_qsv", "h264_vaapi", "hevc_vaapi":
		return true
	default:
		return false
	}
}

// chooseEncoder resolves the effective video codec from the preset.
func chooseEncoder(p *Preset) string {
	codec := p.VideoCodec
	if codec == "" {
		return "libx264"
	}
	return codec
}

// ResolveEncoder applies host encoder availability to pick the final codec.
func ResolveEncoder(p *Preset, available []hwenc.EncoderInfo, ffmpegPath string) string {
	codec := strings.TrimSpace(p.VideoCodec)
	if codec == "" {
		return "libx264"
	}
	for _, e := range available {
		if strings.EqualFold(e.ID, codec) {
			return codec
		}
	}
	if isHardwareEncoder(codec) && ffmpegPath != "" && hwenc.EncoderListedInFFmpeg(ffmpegPath, codec) {
		return codec
	}
	if !isHardwareEncoder(codec) {
		return codec
	}
	if p.HWFallback {
		return softwareFallbackEncoder(codec)
	}
	return codec
}

func softwareFallbackEncoder(codec string) string {
	if strings.HasPrefix(strings.ToLower(codec), "hevc_") {
		return "libx265"
	}
	return "libx264"
}

func nvencPreset(videoPreset string) string {
	switch strings.ToLower(strings.TrimSpace(videoPreset)) {
	case "p1", "p2", "p3", "p4", "p5", "p6", "p7":
		return strings.ToLower(videoPreset)
	default:
		return "p4"
	}
}

func amfQuality(videoPreset string) string {
	switch strings.ToLower(strings.TrimSpace(videoPreset)) {
	case "ultrafast", "veryfast", "fast":
		return "speed"
	case "slow", "slower", "veryslow":
		return "quality"
	default:
		return "balanced"
	}
}

func qsvPreset(videoPreset string) string {
	switch strings.ToLower(strings.TrimSpace(videoPreset)) {
	case "ultrafast", "veryfast":
		return "veryfast"
	case "fast":
		return "faster"
	case "slow", "slower", "veryslow":
		return "slow"
	default:
		return "medium"
	}
}

func isHardwareEncoder(codec string) bool {
	switch codec {
	case "h264_nvenc", "h264_qsv", "h264_amf", "h264_vaapi", "hevc_nvenc", "hevc_qsv", "hevc_amf", "hevc_vaapi":
		return true
	}
	return false
}
