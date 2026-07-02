package hwenc

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// PipelineMode selects how much of the decode/scale/encode chain runs on hardware.
type PipelineMode int

const (
	PipelineSoftware PipelineMode = iota
	PipelineHWEncodeOnly
	PipelineHWFull
)

// InstantVideoPlan describes live/JIT segment video encoding parameters.
type InstantVideoPlan struct {
	Encoder    ID
	Mode       PipelineMode
	Resolution string
	Bitrate    string
	X264Preset string
	CRF        int
	SessionGOP bool
}

// InputAccelArgs returns ffmpeg arguments inserted before -i for hardware decode.
func InputAccelArgs(enc ID) []string {
	switch enc {
	case H264NVENC:
		return []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
	case H264QSV:
		return []string{"-hwaccel", "qsv", "-hwaccel_output_format", "qsv"}
	case H264AMF:
		if runtime.GOOS == "windows" {
			return []string{"-hwaccel", "d3d11va"}
		}
		return []string{"-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"}
	case H264VAAPI:
		return []string{"-vaapi_device", VAAPIDevice(), "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"}
	default:
		return nil
	}
}

// BuildInstantVideoArgs returns -vf / codec arguments for JIT instant transcode.
func BuildInstantVideoArgs(plan InstantVideoPlan) []string {
	wPx, hPx := ParseResolutionWH(plan.Resolution)
	preset := strings.TrimSpace(plan.X264Preset)
	if preset == "" {
		preset = "veryfast"
	}
	crf := plan.CRF
	if crf <= 0 {
		crf = 23
	}
	gops := instantGOPArgs(plan.SessionGOP)

	if plan.Mode == PipelineSoftware || plan.Encoder == Libx264 || plan.Encoder == "" {
		return append([]string{
			"-vf", fmt.Sprintf("scale=%s:%s,format=yuv420p", wPx, hPx),
			"-c:v", "libx264",
			"-preset", preset,
			"-b:v", plan.Bitrate, "-maxrate", plan.Bitrate, "-bufsize", "2M",
			"-profile:v", "high",
			"-x264opts:v:0", "subme=0:me_range=4:rc_lookahead=10:partitions=none",
			"-crf:v:0", strconv.Itoa(crf),
		}, gops...)
	}

	switch plan.Encoder {
	case H264QSV:
		vf := "scale=" + wPx + ":" + hPx + ",format=nv12"
		if plan.Mode == PipelineHWFull {
			vf = "scale_qsv=" + wPx + ":" + hPx
		}
		return append([]string{
			"-vf", vf,
			"-c:v", "h264_qsv",
			"-preset", mapX264PresetToQSV(preset),
			"-b:v", plan.Bitrate, "-maxrate", plan.Bitrate, "-bufsize", "2M",
			"-profile:v", "high",
		}, gops...)
	case H264AMF:
		vf := "scale=" + wPx + ":" + hPx + ",format=nv12"
		return append([]string{
			"-vf", vf,
			"-c:v", "h264_amf",
			"-quality", mapX264PresetToAMF(preset),
			"-rc", "vbr_peak",
			"-b:v", plan.Bitrate, "-maxrate", plan.Bitrate, "-bufsize", "2M",
		}, gops...)
	case H264NVENC:
		vf := "scale=" + wPx + ":" + hPx + ",format=yuv420p"
		if plan.Mode == PipelineHWFull {
			vf = "scale_cuda=" + wPx + ":" + hPx + ":format=nv12"
		}
		return append([]string{
			"-vf", vf,
			"-c:v", "h264_nvenc",
			"-preset", mapX264PresetToNVENC(preset),
			"-b:v", plan.Bitrate, "-maxrate", plan.Bitrate, "-bufsize", "2M",
			"-profile:v", "high",
		}, gops...)
	case H264VAAPI:
		vf := "format=nv12,hwupload,scale_vaapi=w=" + wPx + ":h=" + hPx
		return append([]string{
			"-vf", vf,
			"-c:v", "h264_vaapi",
			"-b:v", plan.Bitrate, "-maxrate", plan.Bitrate, "-bufsize", "2M",
		}, gops...)
	default:
		return BuildInstantVideoArgs(InstantVideoPlan{
			Encoder:    Libx264,
			Mode:       PipelineSoftware,
			Resolution: plan.Resolution,
			Bitrate:    plan.Bitrate,
			X264Preset: plan.X264Preset,
			CRF:        plan.CRF,
			SessionGOP: plan.SessionGOP,
		})
	}
}

func instantGOPArgs(sessionGOP bool) []string {
	if sessionGOP {
		return []string{"-g:v:0", "72", "-sc_threshold:v:0", "0", "-keyint_min:v:0", "72", "-r:v:0", "23.976043701171875"}
	}
	return []string{"-g", "48", "-keyint_min", "48", "-sc_threshold", "0"}
}

// ParseResolutionWH splits "WxH" or "W:H" into width/height strings.
func ParseResolutionWH(res string) (string, string) {
	parts := strings.Split(res, ":")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	parts = strings.Split(strings.ToLower(res), "x")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "iw", "ih"
}

func mapX264PresetToQSV(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "ultrafast", "veryfast":
		return "veryfast"
	case "fast":
		return "faster"
	case "slow":
		return "slow"
	default:
		return "medium"
	}
}

func mapX264PresetToAMF(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "ultrafast", "veryfast", "fast":
		return "speed"
	case "slow":
		return "quality"
	default:
		return "balanced"
	}
}

func mapX264PresetToNVENC(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "ultrafast":
		return "p1"
	case "veryfast":
		return "p2"
	case "fast":
		return "p3"
	default:
		return "p4"
	}
}

// PipelineModeForInput picks HW full pipeline for local files, encode-only for pipes.
func PipelineModeForInput(useHW bool, localFile bool) PipelineMode {
	if !useHW {
		return PipelineSoftware
	}
	if localFile {
		return PipelineHWFull
	}
	return PipelineHWEncodeOnly
}
