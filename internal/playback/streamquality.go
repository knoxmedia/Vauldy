// Package playback helpers interpret system playback options (e.g. home stream quality).
package playback

import (
	"fmt"
	"strconv"
	"strings"
)

// HomeStreamLimit describes the configured maximum streaming quality cap.
type HomeStreamLimit struct {
	Auto          bool
	MaxHeight     int
	MaxBitrateBps int64
	BitrateFFmpeg string // e.g. "30000k"
	Resolution    string // e.g. "1920:1080"
}

// ParseHomeStreamQuality converts a system-options value such as "1080p-30mbps" or "auto".
func ParseHomeStreamQuality(value string) HomeStreamLimit {
	value = strings.TrimSpace(value)
	if value == "" || value == "auto" {
		return HomeStreamLimit{Auto: true}
	}
	parts := strings.SplitN(value, "-", 2)
	if len(parts) != 2 {
		return HomeStreamLimit{Auto: true}
	}
	height := resolutionHeight(strings.ToLower(strings.TrimSpace(parts[0])))
	if height <= 0 {
		return HomeStreamLimit{Auto: true}
	}
	mbpsPart := strings.TrimSpace(strings.ToLower(parts[1]))
	mbpsPart = strings.TrimSuffix(mbpsPart, "mbps")
	mbpsPart = strings.ReplaceAll(mbpsPart, "_", ".")
	mbps, err := strconv.ParseFloat(mbpsPart, 64)
	if err != nil || mbps <= 0 {
		return HomeStreamLimit{Auto: true}
	}
	bitrateK := int(mbps * 1000)
	if bitrateK <= 0 {
		return HomeStreamLimit{Auto: true}
	}
	return HomeStreamLimit{
		MaxHeight:     height,
		MaxBitrateBps: int64(mbps * 1_000_000),
		BitrateFFmpeg: fmt.Sprintf("%dk", bitrateK),
		Resolution:    resolutionForHeight(height),
	}
}

// SourceExceedsLimit reports whether the source exceeds an explicit home-stream cap.
func SourceExceedsLimit(srcHeight, srcWidth int, srcBitrateBps int64, limit HomeStreamLimit) bool {
	if limit.Auto {
		return false
	}
	_ = srcWidth
	if limit.MaxHeight > 0 && srcHeight > limit.MaxHeight {
		return true
	}
	if limit.MaxBitrateBps > 0 && srcBitrateBps > 0 && srcBitrateBps > limit.MaxBitrateBps {
		return true
	}
	return false
}

// PickJITParams selects ffmpeg bitrate/resolution for a JIT session.
func PickJITParams(srcHeight, srcWidth, clientMaxHeight int, limit HomeStreamLimit) (bitrate, resolution string) {
	if limit.Auto {
		return pickAutoJITParams(srcHeight, srcWidth, clientMaxHeight)
	}
	targetH := limit.MaxHeight
	if srcHeight > 0 && srcHeight < targetH {
		targetH = srcHeight
	}
	if clientMaxHeight > 0 && clientMaxHeight < targetH {
		targetH = clientMaxHeight
	}
	return limit.BitrateFFmpeg, resolutionForHeight(targetH)
}

func pickAutoJITParams(srcHeight, srcWidth, clientMaxHeight int) (bitrate, resolution string) {
	maxH := srcHeight
	if srcWidth > maxH {
		maxH = srcWidth
	}
	if clientMaxHeight > 0 && (maxH <= 0 || clientMaxHeight < maxH) {
		maxH = clientMaxHeight
	}
	switch {
	case maxH >= 1080:
		return "4000k", "1920:1080"
	case maxH >= 720:
		return "2000k", "1280:720"
	case maxH >= 480:
		return "1000k", "854:480"
	default:
		return "500k", "640:360"
	}
}

func resolutionHeight(resKey string) int {
	switch resKey {
	case "4k":
		return 2160
	case "1080p":
		return 1080
	case "720p":
		return 720
	case "480p":
		return 480
	default:
		return 0
	}
}

func resolutionForHeight(height int) string {
	switch {
	case height >= 2160:
		return "3840:2160"
	case height >= 1080:
		return "1920:1080"
	case height >= 720:
		return "1280:720"
	case height >= 480:
		return "854:480"
	default:
		return "640:360"
	}
}
