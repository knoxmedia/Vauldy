package photoclass

import (
	"path/filepath"
	"strings"
)

// Input holds image context for classification.
type Input struct {
	FilePath     string
	ThumbPath    string
	Title        string
	Width        int
	Height       int
	CameraMake   string
	CameraModel  string
	Format       string
}

// Result holds classification output.
type Result struct {
	Tags   []string           `json:"tags"`
	Scores map[string]float64 `json:"scores,omitempty"`
	Engine string             `json:"engine"`
}

func classifyHeuristic(in Input, stats colorStats, hasColor bool) Result {
	scores := map[string]float64{}
	var tags []string

	base := strings.ToLower(filepath.Base(in.FilePath))
	title := strings.ToLower(in.Title)
	combined := base + " " + title

	hasCamera := strings.TrimSpace(in.CameraMake) != "" || strings.TrimSpace(in.CameraModel) != ""

	// Source tags
	switch {
	case strings.Contains(combined, "screenshot") || strings.Contains(combined, "截图") || strings.Contains(combined, "screen"):
		addTag(&tags, scores, "手机截图", 0.85)
		addTag(&tags, scores, "文档/截图", 0.8)
	case hasCamera:
		addTag(&tags, scores, "相机拍摄", 0.75)
	default:
		addTag(&tags, scores, "下载保存", 0.55)
	}

	// Scene: screenshot / document
	if strings.HasSuffix(strings.ToLower(in.FilePath), ".png") && !hasCamera {
		if isScreenAspect(in.Width, in.Height) {
			addTag(&tags, scores, "文档/截图", 0.7)
		}
	}

	// Night scene
	if hasColor && stats.avgBright < 0.18 {
		addTag(&tags, scores, "夜景", 0.72)
	}

	// Landscape vs portrait heuristics
	if in.Width > 0 && in.Height > 0 {
		ratio := float64(in.Width) / float64(in.Height)
		if ratio >= 1.25 && hasColor {
			if stats.avgG > stats.avgR && stats.avgB >= stats.avgR*0.9 {
				addTag(&tags, scores, "风景", 0.6)
			}
		}
		if ratio <= 0.85 {
			if strings.Contains(combined, "selfie") || strings.Contains(combined, "自拍") {
				addTag(&tags, scores, "自拍", 0.8)
				addTag(&tags, scores, "人物", 0.75)
			}
		}
	}

	// Food: warm + saturated center bias approximated by global warm+sat
	if hasColor && stats.avgR > stats.avgB+0.05 && stats.avgSat > 0.35 {
		addTag(&tags, scores, "美食", 0.45)
	}

	// Architecture: moderate saturation, not night, landscape-ish
	if hasColor && stats.avgBright > 0.25 && stats.avgSat < 0.4 && in.Width > in.Height {
		addTag(&tags, scores, "建筑", 0.35)
	}

	// People proxy from filename
	if strings.Contains(combined, "portrait") || strings.Contains(combined, "人物") || strings.Contains(combined, "合影") {
		addTag(&tags, scores, "人物", 0.7)
	}

	// Animals from filename
	for _, kw := range []string{"cat", "dog", "bird", "动物", "宠物"} {
		if strings.Contains(combined, kw) {
			addTag(&tags, scores, "动物", 0.65)
			break
		}
	}

	return Result{Tags: dedupeTags(tags), Scores: scores, Engine: "heuristic"}
}

func isScreenAspect(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	ratio := float64(w) / float64(h)
	for _, target := range []float64{16.0 / 9, 9.0 / 16, 16.0 / 10, 4.0 / 3, 3.0 / 2} {
		if mathAbs(ratio-target) < 0.08 {
			return true
		}
	}
	return false
}

func mathAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func addTag(tags *[]string, scores map[string]float64, tag string, score float64) {
	*tags = append(*tags, tag)
	if prev, ok := scores[tag]; !ok || score > prev {
		scores[tag] = score
	}
}

func dedupeTags(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func mergeTags(parts ...[]string) []string {
	var all []string
	for _, p := range parts {
		all = append(all, p...)
	}
	return dedupeTags(all)
}
