package pretranscode

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	OutputDirModeSource = "source"
	OutputDirModeCustom = "custom"
	OutputDirModeData   = "data"
)

// TaskOutputRootInput describes how to resolve the task-level output directory.
type TaskOutputRootInput struct {
	Mode         string
	CustomDir    string
	TranscodeDir string
	FileID       string
	PresetID     int64
	SourcePath   string
}

// NormalizeOutputDirMode returns a supported output directory mode.
func NormalizeOutputDirMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case OutputDirModeCustom:
		return OutputDirModeCustom
	case OutputDirModeData:
		return OutputDirModeData
	default:
		return OutputDirModeSource
	}
}

// ComputeTaskOutputRoot resolves the root directory for a pretranscode task.
// Default (source): {source_dir}/{stem}.pretranscode/preset{preset_id}/
func ComputeTaskOutputRoot(in TaskOutputRootInput) string {
	mode := NormalizeOutputDirMode(in.Mode)
	presetPart := fmt.Sprintf("preset%d", in.PresetID)
	fileID := strings.TrimSpace(in.FileID)
	if fileID == "" {
		fileID = "unknown"
	}

	switch mode {
	case OutputDirModeCustom:
		if root := strings.TrimSpace(in.CustomDir); root != "" {
			return filepath.Join(root, fileID, presetPart)
		}
	case OutputDirModeSource:
		if source := strings.TrimSpace(in.SourcePath); source != "" {
			dir := filepath.Dir(source)
			stem := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
			if stem == "" {
				stem = fileID
			}
			return filepath.Join(dir, stem+".pretranscode", presetPart)
		}
	}

	transcodeDir := strings.TrimSpace(in.TranscodeDir)
	if transcodeDir == "" {
		transcodeDir = "."
	}
	return filepath.Join(transcodeDir, fileID, presetPart)
}

// RenditionOutputDir returns the per-rendition output directory under a task root.
func RenditionOutputDir(taskRoot, renditionName string) string {
	return filepath.Join(taskRoot, renditionName)
}

// RenditionDeletePath returns the filesystem path to remove for a rendition output.
// HLS/DASH playlist paths delete the containing directory; MP4 deletes the file path.
func RenditionDeletePath(outputPath, outputFormat string) string {
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		return ""
	}
	lower := strings.ToLower(outputPath)
	format := strings.ToLower(strings.TrimSpace(outputFormat))
	if format == "hls" || format == "dash" || strings.HasSuffix(lower, ".m3u8") || strings.HasSuffix(lower, ".mpd") {
		return filepath.Dir(outputPath)
	}
	return outputPath
}

// RenditionSizePath returns the path used to calculate on-disk size for a rendition.
func RenditionSizePath(outputPath, outputFormat string) string {
	deletePath := RenditionDeletePath(outputPath, outputFormat)
	if deletePath != "" {
		return deletePath
	}
	return outputPath
}
