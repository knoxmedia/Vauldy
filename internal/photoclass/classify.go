package photoclass

import (
	"encoding/json"
	"os"
	"strings"
)

// Classify runs color + heuristic analysis on a local image (prefers thumb for speed).
func Classify(in Input) Result {
	imagePath := strings.TrimSpace(in.ThumbPath)
	if imagePath == "" {
		imagePath = in.FilePath
	}
	var colorTagList []string
	stats, hasColor := analyzeColor(imagePath)
	if hasColor {
		colorTagList = colorTagsFromStats(stats)
	}
	heur := classifyHeuristic(in, stats, hasColor)
	all := mergeTags(heur.Tags, colorTagList)
	return Result{
		Tags:   all,
		Scores: heur.Scores,
		Engine: heur.Engine,
	}
}

// MergeTagsIntoMetaJSON writes effective tags into meta_json.photo.
func MergeTagsIntoMetaJSON(raw string, aiTags []string, engine string, manualOverride bool, manualTags []string) string {
	var root map[string]any
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	photo, _ := root["photo"].(map[string]any)
	if photo == nil {
		photo = map[string]any{}
	}
	photo["ai_tags"] = NormalizeTags(aiTags)
	photo["ai_engine"] = engine
	effective := NormalizeTags(aiTags)
	if manualOverride && len(manualTags) > 0 {
		effective = NormalizeTags(manualTags)
	}
	photo["tags"] = effective
	if manualOverride {
		photo["manual_override"] = true
		photo["manual_tags"] = NormalizeTags(manualTags)
	}
	root["photo"] = photo
	b, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(b)
}

// ReadPhotoTags extracts effective tags from meta_json.
func ReadPhotoTags(raw string) (tags []string, aiTags []string, manualOverride bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, false
	}
	var root struct {
		Photo struct {
			Tags           []string `json:"tags"`
			AITags         []string `json:"ai_tags"`
			ManualTags     []string `json:"manual_tags"`
			ManualOverride bool     `json:"manual_override"`
		} `json:"photo"`
	}
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, nil, false
	}
	manualOverride = root.Photo.ManualOverride
	aiTags = NormalizeTags(root.Photo.AITags)
	if manualOverride && len(root.Photo.ManualTags) > 0 {
		return NormalizeTags(root.Photo.ManualTags), aiTags, true
	}
	if len(root.Photo.Tags) > 0 {
		return NormalizeTags(root.Photo.Tags), aiTags, manualOverride
	}
	return aiTags, aiTags, manualOverride
}

// PickImagePath returns thumb if present else original.
func PickImagePath(thumbPath, filePath string) string {
	if p := strings.TrimSpace(thumbPath); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p
		}
	}
	return strings.TrimSpace(filePath)
}
