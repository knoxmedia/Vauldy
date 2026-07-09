package profile

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-redis/redis/v8"
)

// IsPortraitSource reports whether the source video is taller than it is wide.
func IsPortraitSource(sourceWidth, sourceHeight int) bool {
	return sourceWidth > 0 && sourceHeight > 0 && sourceWidth < sourceHeight
}

// AdaptLandscapeDimensions swaps landscape target dimensions for portrait sources.
func AdaptLandscapeDimensions(sourceWidth, sourceHeight, targetWidth, targetHeight int) (int, int) {
	if !IsPortraitSource(sourceWidth, sourceHeight) {
		return targetWidth, targetHeight
	}
	if targetWidth <= 0 || targetHeight <= 0 || targetWidth <= targetHeight {
		return targetWidth, targetHeight
	}
	return targetHeight, targetWidth
}

// AdaptVariantForPortrait returns a copy of v with dimensions swapped for portrait sources.
func AdaptVariantForPortrait(v Variant, sourceWidth, sourceHeight int) Variant {
	w, h := AdaptLandscapeDimensions(sourceWidth, sourceHeight, v.Width, v.Height)
	out := v
	out.Width = w
	out.Height = h
	return out
}

// AdaptResolutionString swaps WxH in a resolution token for portrait sources.
func AdaptResolutionString(sourceWidth, sourceHeight int, resolution string) string {
	w, h, ok := parseResolutionPair(resolution)
	if !ok {
		return resolution
	}
	aw, ah := AdaptLandscapeDimensions(sourceWidth, sourceHeight, w, h)
	sep := "x"
	if strings.Contains(resolution, ":") {
		sep = ":"
	}
	return fmt.Sprintf("%d%s%d", aw, sep, ah)
}

// ResolutionForBitrate maps a ladder bitrate to an output resolution adapted for source orientation.
func ResolutionForBitrate(bitrate string, sourceWidth, sourceHeight int) string {
	v := VariantForBitrate(bitrate)
	adapted := AdaptVariantForPortrait(v, sourceWidth, sourceHeight)
	return fmt.Sprintf("%dx%d", adapted.Width, adapted.Height)
}

// VariantForBitrate returns the built-in ladder entry for a bitrate string.
func VariantForBitrate(bitrate string) Variant {
	bitrate = strings.TrimSpace(bitrate)
	for _, v := range builtinRungs {
		if v.Bitrate == bitrate {
			return v
		}
	}
	return builtinRungs[2]
}

func variantLongEdge(v Variant) int {
	if v.Width > v.Height {
		return v.Width
	}
	return v.Height
}

func sourceLongEdge(sourceWidth, sourceHeight int) int {
	if sourceWidth > 0 && sourceHeight > 0 {
		if sourceWidth > sourceHeight {
			return sourceWidth
		}
		return sourceHeight
	}
	return sourceHeight
}

func parseResolutionPair(resolution string) (int, int, bool) {
	resolution = strings.TrimSpace(resolution)
	if resolution == "" {
		return 0, 0, false
	}
	sep := "x"
	if strings.Contains(resolution, ":") {
		sep = ":"
	}
	parts := strings.Split(resolution, sep)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func pickPortrait(ctx context.Context, rdb *redis.Client, sourceWidth, sourceHeight, maxClientHeight int) Variant {
	cap := sourceLongEdge(sourceWidth, sourceHeight)
	if maxClientHeight > 0 && maxClientHeight < cap {
		cap = maxClientHeight
	}
	cap = applyLoadLimit(ctx, rdb, cap)
	for _, v := range builtinRungs {
		adapted := AdaptVariantForPortrait(v, sourceWidth, sourceHeight)
		if variantLongEdge(adapted) <= cap {
			return adapted
		}
	}
	return AdaptVariantForPortrait(builtinRungs[len(builtinRungs)-1], sourceWidth, sourceHeight)
}
