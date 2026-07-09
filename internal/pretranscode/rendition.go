package pretranscode

import "sort"

// isPortraitSource reports whether the source video is taller than it is wide.
func isPortraitSource(sourceWidth, sourceHeight int) bool {
	return sourceWidth > 0 && sourceHeight > 0 && sourceWidth < sourceHeight
}

// renditionTargetSize resolves the landscape-oriented output dimensions implied
// by a preset rendition. When width is unset, a 16:9 width is inferred from height.
func renditionTargetSize(r Rendition) (width, height int) {
	height = r.Height
	width = r.Width
	if height <= 0 {
		return 0, 0
	}
	if width > 0 {
		return width, height
	}
	return defaultLandscapeWidthForHeight(height), height
}

func defaultLandscapeWidthForHeight(height int) int {
	switch height {
	case 360:
		return 640
	case 480:
		return 854
	case 540:
		return 960
	case 720:
		return 1280
	case 1080:
		return 1920
	case 1440:
		return 2560
	case 2160:
		return 3840
	default:
		w := height * 16 / 9
		if w%2 != 0 {
			w++
		}
		return w
	}
}

// AdaptRenditionForSource swaps landscape preset dimensions when the source
// video is portrait. For example, 1280x720 becomes 720x1280.
func AdaptRenditionForSource(r Rendition, sourceWidth, sourceHeight int) Rendition {
	if !isPortraitSource(sourceWidth, sourceHeight) {
		return r
	}
	targetW, targetH := renditionTargetSize(r)
	if targetW <= 0 || targetH <= 0 || targetW <= targetH {
		return r
	}
	out := r
	out.Width = targetH
	out.Height = targetW
	return out
}

// ShouldSkipRenditionAboveSource implements SRS REN-05 using adapted output
// dimensions so portrait sources compare against the correct long edge.
func ShouldSkipRenditionAboveSource(r Rendition, sourceWidth, sourceHeight int) bool {
	if sourceWidth <= 0 && sourceHeight <= 0 {
		return false
	}
	adapted := AdaptRenditionForSource(r, sourceWidth, sourceHeight)
	outW, outH := renditionTargetSize(adapted)
	if outW <= 0 || outH <= 0 {
		return false
	}
	outLong := outH
	if outW > outLong {
		outLong = outW
	}
	srcLong := sourceHeight
	if sourceWidth > srcLong {
		srcLong = sourceWidth
	}
	return outLong > srcLong
}

// SkipRenditionsAboveSource returns renditions that will not upscale the source.
func SkipRenditionsAboveSource(renditions []Rendition, sourceWidth, sourceHeight int) []Rendition {
	out := make([]Rendition, 0, len(renditions))
	for _, r := range renditions {
		if !ShouldSkipRenditionAboveSource(r, sourceWidth, sourceHeight) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Height < out[j].Height })
	return out
}
